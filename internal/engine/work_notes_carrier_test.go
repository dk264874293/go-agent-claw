package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func carrierFixture(t *testing.T) (context.Context, *runState) {
	t.Helper()
	ctx, r, _ := analysisContextFixture(t)
	a := r.state.Analysis
	r.state.Spec.Analysis = false
	r.state.Spec.ReadContextVersion = 2
	r.state.ReadMemory = &ReadMemoryState{Version: 1, Evidence: a.Evidence, Notes: a.Reports, ProcessedReads: a.Processed, Summaries: a.Summaries}
	r.state.Analysis = nil
	r.markAnalysisDelivered(ctx, []string{"E1", "E2"})
	return ctx, r
}

func carrierCall(id string, notes any) schema.ToolCall {
	raw, _ := json.Marshal(map[string]any{"path": "E2.md", "_work_notes": notes})
	return schema.ToolCall{ID: id, Name: "read_file", Arguments: raw}
}

func carrierSummary(id, text string) AnalysisReport {
	return AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{id}, Summaries: map[string]string{id: text}}
}

func TestWorkNotesCarrierSchemaIsOptionalScopedAndTypePreserving(t *testing.T) {
	ctx, r := carrierFixture(t)
	registry := NewWorkspaceRegistry(tools.ScopeFromContext(ctx).WorkDir)
	ctx = tools.WithScope(ctx, tools.ExecutionScope{WorkDir: tools.ScopeFromContext(ctx).WorkDir, Mode: tools.ModeWorkspace})
	original := registry.VisibleTools(ctx)
	for _, def := range r.visibleTools(ctx, registry) {
		input := def.InputSchema.(map[string]any)
		props := input["properties"].(map[string]any)
		_, present := props["_work_notes"]
		if present != workNotesCarrierTool(def.Name) {
			t.Fatalf("工具%s的笔记载体可见性错误", def.Name)
		}
		if required, ok := input["required"].([]string); ok {
			for _, field := range required {
				if field == "_work_notes" {
					t.Fatal("可选笔记变成必填参数")
				}
			}
			if present && len(required) > 0 {
				required[0] = "副本修改"
			}
		} else if present {
			t.Fatalf("%s required丢失[]string类型: %T", def.Name, input["required"])
		}
		if present {
			props["path"].(map[string]any)["description"] = "副本修改"
		}
	}
	for _, def := range original {
		input := def.InputSchema.(map[string]any)
		props := input["properties"].(map[string]any)
		if _, exists := props["_work_notes"]; exists || strings.Contains(fmt.Sprint(input), "副本修改") {
			t.Fatalf("注册表schema受到污染: %s", def.Name)
		}
	}
	for _, spec := range []TaskSpec{{ReadContextVersion: 0}, {ReadContextVersion: 1}, {ReadContextVersion: 2, Analysis: true}, {ReadContextVersion: 2, Report: &ReportSpec{}}} {
		r.state.Spec = spec
		for _, def := range r.visibleTools(ctx, registry) {
			if _, exists := def.InputSchema.(map[string]any)["properties"].(map[string]any)["_work_notes"]; exists {
				t.Fatalf("旧任务或专用协议泄露载体: %+v", spec)
			}
		}
	}
}

func TestWorkNotesCarrierSoftRejectionPersistsWithoutGrantingProcessing(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		value        any
	}{
		{"null", "invalid_shape", nil},
		{"类型错误", "invalid_shape", "不是对象"},
		{"空更新", "empty_update", AnalysisReport{SchemaVersion: 1}},
		{"未读取来源", "invalid_evidence", carrierSummary("E99", "虚构来源")},
		{"超限", "too_large", map[string]any{"schema_version": 1, "findings": []any{}, "unverified": []string{strings.Repeat("x", workNotesCarrierLimit)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r := carrierFixture(t)
			persisted := 0
			r.persist = func(kind string, _ taskState) error {
				if kind == "work_notes_rejected" {
					persisted++
				}
				return nil
			}
			call := carrierCall("call", tc.value)
			batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{call})
			if err != nil || batch.valid || persisted != 1 || !strings.Contains(batch.receipts[0], tc.reason) || len(batch.receipts[0]) > 512 {
				t.Fatalf("软拒收错误: %+v persisted=%d err=%v", batch, persisted, err)
			}
			if len(r.state.ReadMemory.ProcessedReads["root"]) != 0 || !r.isConsumedWorkNotesCarrier("root", "run", call) || strings.Contains(string(batch.calls[0].Arguments), "_work_notes") {
				t.Fatal("拒收授予处理资格，或载体没有隔离执行/投影")
			}
		})
	}
}

func TestWorkNotesCarrierUsesCurrentDeliveredAgentEvidence(t *testing.T) {
	for _, mode := range []string{"future", "stale", "other_run", "other_agent", "invalidated"} {
		t.Run(mode, func(t *testing.T) {
			ctx, r := carrierFixture(t)
			entry := r.analysisRuntime.sources["E1"]
			switch mode {
			case "future":
				r.analysisRuntime.delivered["root"]["E1"] = false
			case "stale":
				writeFixture(t, tools.ScopeFromContext(ctx).WorkDir, "E1.md", "新版本")
			case "other_run":
				entry.RunID = "旧运行"
			case "other_agent":
				ctx = observe.WithAgent(ctx, "child", "root")
				r.analysisRuntime.delivered["child"] = map[string]bool{"E1": true}
			case "invalidated":
				r.analysisRuntime.invalidated = map[string]string{"E1": "副作用"}
			}
			r.analysisRuntime.sources["E1"] = entry
			batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("read", carrierSummary("E1", "不可采纳"))})
			if err != nil || batch.valid || !strings.Contains(batch.receipts[0], "invalid_evidence") {
				t.Fatalf("越权或过期依据被接受: %+v %v", batch, err)
			}
		})
	}
}

func TestWorkNotesCarrierFirstValidUpdateWinsAndIdentityBindsOriginalArguments(t *testing.T) {
	ctx, r := carrierFixture(t)
	bad := carrierCall("bad", carrierSummary("E99", "无效不能抢占"))
	first := carrierCall("first", carrierSummary("E1", "首个有效摘要"))
	second := carrierCall("second", carrierSummary("E1", "冲突摘要"))
	duplicate := first
	duplicate.ID = "duplicate"
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{bad, first, second, duplicate})
	if err != nil || !batch.valid || r.state.ReadMemory.Summaries["root"]["E1"] != "首个有效摘要" || !containsString(r.state.ReadMemory.ProcessedReads["root"], "E1") {
		t.Fatalf("顺序或去重错误: %+v state=%+v err=%v", batch, r.state.ReadMemory, err)
	}
	for _, call := range []schema.ToolCall{first, second, duplicate} {
		if !r.isConsumedWorkNotesCarrier("root", "run", call) || r.isConsumedWorkNotesCarrier("other", "run", call) || r.isConsumedWorkNotesCarrier("root", "other", call) {
			t.Fatal("消费身份未绑定Agent和Run")
		}
		changed := call
		changed.Arguments = append(append(json.RawMessage(nil), call.Arguments...), ' ')
		if r.isConsumedWorkNotesCarrier("root", "run", changed) || r.isConsumedWorkNotesCarrier("root", "run", stripWorkNotesCarrier(call)) {
			t.Fatal("消费身份没有绑定原始参数hash")
		}
	}
	if !strings.Contains(batch.receipts[3], "duplicate") || strings.Contains(batch.receipts[1], "首个有效摘要") {
		t.Fatal("重复回执或无正文回执错误")
	}
}

func TestWorkNotesCarrierInvalidFindingCannotClaimFirstValidID(t *testing.T) {
	ctx, r := carrierFixture(t)
	finding := AnalysisFinding{ID: "F1", Kind: "fact", Statement: "首个有效发现", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}
	invalid := finding
	invalid.Evidence = []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "不存在的原文"}}
	invalid.Statement = "无效发现不能抢占ID"
	conflict := finding
	conflict.Statement = "后续冲突不能覆盖"
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{
		carrierCall("invalid", AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{invalid}}),
		carrierCall("valid", AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{finding}}),
		carrierCall("conflict", AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{conflict}}),
	})
	accepted := r.state.ReadMemory.Notes["root"].Findings
	if err != nil || !batch.valid || len(accepted) != 1 || accepted[0].Statement != finding.Statement || !strings.Contains(batch.receipts[0], "invalid_evidence") {
		t.Fatalf("首有效ID选择错误: %+v accepted=%+v err=%v", batch, accepted, err)
	}
}

func TestWorkNotesCarrierSummaryAndDeferredDoNotProcessRead(t *testing.T) {
	for _, report := range []AnalysisReport{
		{SchemaVersion: 1, Summaries: map[string]string{"E1": "只有摘要"}},
		{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "需对照E2"}}},
		{SchemaVersion: 1, Unverified: []string{"仅静态观察，仍需实际执行验证"}},
		{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "U1", Kind: "unverified", Statement: "材料尚不能确认执行结果"}}},
	} {
		ctx, r := carrierFixture(t)
		batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("read", report)})
		if err != nil || !batch.valid || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 {
			t.Fatalf("合法摘要或暂缓未接受，或冒充已处理: %+v %v", batch, err)
		}
		if (len(report.Unverified) > 0 || len(report.Findings) > 0) && len(r.state.ReadMemory.Notes["root"].Unverified) == 0 {
			t.Fatal("待核实项没有持久保存")
		}
		// 历史合法暂缓不能使后续空载体成为有效新更新。
		batch, err = r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("empty", AnalysisReport{SchemaVersion: 1})})
		if err != nil || batch.valid || !strings.Contains(batch.receipts[0], "empty_update") {
			t.Fatalf("历史条目使空更新误判有效: %+v %v", batch, err)
		}
	}
}

func TestWorkNotesCarrierForeignDeferredIsRejectedAfterRuntimeValidation(t *testing.T) {
	ctx, r := carrierFixture(t)
	entry := r.analysisRuntime.sources["E1"]
	entry.AgentID, entry.Receipt.AgentID = "child", "child"
	r.analysisRuntime.sources["E1"] = entry
	r.analysisRuntime.parents["child"] = "root"
	r.analysisRuntime.delivered["child"] = map[string]bool{"E1": true}
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("foreign", AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "父任务不能替子任务声明暂缓"}}})})
	if err != nil || batch.valid || !strings.Contains(batch.receipts[0], "invalid_evidence") || len(r.state.ReadMemory.Notes["root"].DeferredReads) != 0 {
		t.Fatalf("预校验的父Agent证据复用误授暂缓资格: %+v %v", batch, err)
	}
}

func TestWorkNotesCarrierEightKiBBoundary(t *testing.T) {
	for _, size := range []int{workNotesCarrierLimit, workNotesCarrierLimit + 1} {
		ctx, r := carrierFixture(t)
		value := map[string]any{"schema_version": 1, "findings": []any{}, "unverified": []string{"待实测"}, "padding": ""}
		base, _ := json.Marshal(value)
		value["padding"] = strings.Repeat("x", size-len(base))
		raw, _ := json.Marshal(value)
		batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("boundary", json.RawMessage(raw))})
		if err != nil || len(raw) != size || batch.valid != (size == workNotesCarrierLimit) {
			t.Fatalf("8KiB字节边界错误: size=%d valid=%v err=%v", len(raw), batch.valid, err)
		}
	}
}

func TestWorkNotesCarrierOnlyTopLevelIsRemoved(t *testing.T) {
	raw := json.RawMessage(`{"content":"{\"_work_notes\":\"业务内容\"}","unknown":{"_work_notes":null},"number":9007199254740993,"_work_notes":null,"path":"a.md"}`)
	call := schema.ToolCall{ID: "write", Name: "write_file", Arguments: raw}
	clean := stripWorkNotesCarrier(call)
	want := `{"content":"{\"_work_notes\":\"业务内容\"}","unknown":{"_work_notes":null},"number":9007199254740993,"path":"a.md"}`
	if string(clean.Arguments) != want || string(call.Arguments) != string(raw) {
		t.Fatalf("业务值被改写，或原始调用被修改: %s", clean.Arguments)
	}
	for _, broken := range []string{`{"path":"a","_work_notes":null`, `{"path":"a","path":"b","_work_notes":null}`} {
		ctx, r := carrierFixture(t)
		call.Arguments = json.RawMessage(broken)
		batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{call})
		if err != nil || batch.valid || string(batch.calls[0].Arguments) != broken || r.isConsumedWorkNotesCarrier("root", "run", call) {
			t.Fatal("无法安全抽取的业务参数不应被消费")
		}
	}
}

func TestWorkNotesCarrierStorageFailureStopsBeforeConsumption(t *testing.T) {
	for _, value := range []any{nil, carrierSummary("E1", "有效摘要")} {
		ctx, r := carrierFixture(t)
		r.persist = func(string, taskState) error { return errors.New("磁盘故障") }
		call := carrierCall("call", value)
		_, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{call})
		if !errors.Is(err, errStorage) || r.isConsumedWorkNotesCarrier("root", "run", call) {
			t.Fatalf("持久化故障被忽略或提前消费: %v", err)
		}
	}
}

func TestWorkNotesCarrierBatchIDsAreCheckedBeforeNotes(t *testing.T) {
	ctx, r := carrierFixture(t)
	session := ctxpkg.NewSession("IDs", tools.ScopeFromContext(ctx).WorkDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: "检查"})
	providerCalls := 0
	e := NewAgentEngine(scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		providerCalls++
		return modelCalls(carrierCall("duplicate", carrierSummary("E1", "不能保存")), carrierCall("duplicate", nil))
	}), NewWorkspaceRegistry(session.WorkDir), EngineOptions{})
	_, err := e.converse(ctx, r, session, schema.Message{Role: schema.RoleSystem, Content: "任务"}, e.registry, nil, "", "", 0)
	if !errors.Is(err, errBlocked) || providerCalls != 1 || r.state.Usage.ModelCalls != 1 || r.state.Usage.ToolCalls != 0 || len(r.carriers.consumed) != 0 || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 {
		t.Fatalf("ID校验前产生了笔记或工具副作用: err=%v usage=%+v", err, r.state.Usage)
	}
}

type carrierReporter struct{ arguments []string }

func (*carrierReporter) OnThinking(context.Context)                         {}
func (*carrierReporter) OnMessage(context.Context, string)                  {}
func (*carrierReporter) OnToolResult(context.Context, string, string, bool) {}
func (r *carrierReporter) OnToolCall(_ context.Context, _ string, args string) {
	r.arguments = append(r.arguments, args)
}

func TestWorkNotesCarrierExecutionBoundaryKeepsBusinessDataAndExactBudgets(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "源材料\n")
	registry := NewWorkspaceRegistry(dir)
	middlewareCalls := 0
	registry.Use(func(_ context.Context, call schema.ToolCall) (bool, string) {
		middlewareCalls++
		var args map[string]json.RawMessage
		_ = json.Unmarshal(call.Arguments, &args)
		if _, present := args["_work_notes"]; present {
			t.Fatal("审批或Handler收到引擎笔记元数据")
		}
		return true, ""
	})
	const business = `{"_work_notes":"用户业务数据"}`
	providerCalls := 0
	var originalWrite, executionWrite schema.ToolCall
	pendingChecked := false
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			return modelCalls(readCall("source", "source.md"))
		case 2:
			evidence := analysisIntegrationEvidence(ctx)
			args, _ := json.Marshal(map[string]any{"path": "result.json", "content": business, "_work_notes": carrierSummary(evidence[0].ID, "内部笔记正文不应泄露给审批")})
			originalWrite = schema.ToolCall{ID: "write", Name: "write_file", Arguments: args}
			executionWrite = stripWorkNotesCarrier(originalWrite)
			r := currentRun(ctx)
			persist := r.persist
			r.persist = func(kind string, state taskState) error {
				if kind == "action_started" {
					for _, action := range state.Pending {
						if action.Call.ID == "write" {
							pendingChecked = string(action.Call.Arguments) == string(executionWrite.Arguments)
							if !pendingChecked {
								t.Fatal("pending持久化收到原始笔记元数据")
							}
						}
					}
				}
				return persist(kind, state)
			}
			return modelCalls(originalWrite)
		default:
			for _, message := range messages {
				for _, call := range message.ToolCalls {
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(call.Arguments, &fields)
					if _, exists := fields["_work_notes"]; exists {
						t.Fatal("已消费metadata没有从请求副本移除")
					}
				}
			}
			return reply("结果已保存")
		}
	})
	reporter := &carrierReporter{}
	session := ctxpkg.NewSession("边界", dir)
	e := NewAgentEngine(model, registry, EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 3, MaxToolCalls: 3}})
	result, err := e.Run(context.Background(), session, TaskSpec{Goal: "读取并输出业务JSON"}, reporter)
	if err != nil || result.Status != NeedsVerification || result.Usage.ModelCalls != 3 || result.Usage.ToolCalls != 2 || providerCalls != 3 || middlewareCalls != 2 {
		t.Fatalf("额外调用或执行错误: %+v calls=%d middleware=%d err=%v", result, providerCalls, middlewareCalls, err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil || string(contents) != business {
		t.Fatalf("业务内容被修改: %s %v", contents, err)
	}
	for _, args := range reporter.arguments {
		if strings.Contains(args, "内部笔记正文") {
			t.Fatal("Reporter泄露笔记正文")
		}
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	if !pendingChecked || state.LastAction == nil || state.LastAction.Digest != actionKey("", executionWrite) || state.Completed[actionKey("", executionWrite)].Status != schema.ToolSucceeded {
		t.Fatalf("动作去重或持久化key未采用执行参数: %+v", state.LastAction)
	}
	found := false
	for _, message := range session.GetWorkingMemory(0) {
		for _, call := range message.ToolCalls {
			if call.ID == "write" {
				found = string(call.Arguments) == string(originalWrite.Arguments)
			}
		}
	}
	if !found {
		t.Fatal("Session原始工具调用被改写")
	}
}

func TestWorkNotesCarrierTakesPriorityAndLegacyTextIsOnlyFallback(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "来源材料\n")
			writeFixture(t, dir, "next.md", "后续材料\n")
			providerCalls := 0
			model := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				providerCalls++
				switch providerCalls {
				case 1:
					return modelCalls(readCall("source", "source.md"))
				case 2:
					evidence := analysisIntegrationEvidence(ctx)
					var value any
					if valid {
						value = carrierSummary(evidence[0].ID, "载体有效摘要")
					}
					text, _ := json.Marshal(map[string]any{"work_notes": carrierSummary(evidence[0].ID, "旧文字摘要")})
					raw, _ := json.Marshal(map[string]any{"path": "next.md", "_work_notes": value})
					return &schema.Message{Role: schema.RoleAssistant, Content: string(text), ToolCalls: []schema.ToolCall{{ID: "next", Name: "read_file", Arguments: raw}}}, nil
				default:
					return reply("完成")
				}
			})
			e := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
			result, err := e.Run(context.Background(), ctxpkg.NewSession("兼容", dir), TaskSpec{Goal: "读取材料"}, nil)
			if err != nil || providerCalls != 3 || result.Usage.ToolCalls != 2 || result.Usage.ModelCalls != 3 {
				t.Fatalf("兼容回退增加调用或阻断工具: %+v %v", result, err)
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			want := "旧文字摘要"
			if valid {
				want = "载体有效摘要"
			}
			for _, summaries := range state.ReadMemory.Summaries {
				if summaries["E1"] != want {
					t.Fatalf("新旧双写或回退错误: got=%s want=%s", summaries["E1"], want)
				}
			}
		})
	}
}

func TestWorkNotesCarrierReceiptPreservesReadExposureWithoutProcessedNotes(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "先前依据\n")
	writeFixture(t, dir, "second.md", "本轮读取原文\n")
	providerCalls := 0
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			return modelCalls(readCall("first", "source.md"))
		case 2:
			evidence := analysisIntegrationEvidence(ctx)
			raw, _ := json.Marshal(map[string]any{"path": "second.md", "_work_notes": AnalysisReport{SchemaVersion: 1, Summaries: map[string]string{evidence[0].ID: "只有摘要，没有处理声明"}}})
			return modelCalls(schema.ToolCall{ID: "second", Name: "read_file", Arguments: raw})
		case 3:
			return modelCalls(readCall("repeat", "second.md"))
		default:
			last := messages[len(messages)-1]
			if last.ToolCallID != "repeat" || last.ToolStatus != schema.ToolRejected || !strings.Contains(last.Content, "duplicate_read") {
				t.Fatalf("载体回执破坏完整原文曝光: %+v", last)
			}
			return reply("复用已读原文")
		}
	})
	e := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := e.Run(context.Background(), ctxpkg.NewSession("曝光", dir), TaskSpec{Goal: "核对已读取材料"}, nil)
	if err != nil || providerCalls != 4 || result.Usage.ToolCalls != 3 || result.Usage.ModelCalls != 4 {
		t.Fatalf("回执或预算错误: %+v err=%v", result, err)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	for _, ids := range state.ReadMemory.ProcessedReads {
		if len(ids) != 0 {
			t.Fatal("测试中的摘要被误记为已处理")
		}
	}
}
