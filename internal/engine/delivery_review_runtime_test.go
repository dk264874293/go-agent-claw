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
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func reviewWriteCall(id, content string, notes any) schema.ToolCall {
	body := map[string]any{"path": "result.md", "content": content, "expected_version": "missing"}
	if notes != nil {
		body["_work_notes"] = notes
	}
	raw, _ := json.Marshal(body)
	return schema.ToolCall{ID: id, Name: "write_file", Arguments: raw}
}
func deliverySpec(mode tools.Mode) TaskSpec {
	kind := TaskChange
	if mode == tools.ModeReadOnly {
		kind = TaskAnswer
	}
	return TaskSpec{Goal: "根据原文汇总，保留实际条件", Mode: mode, Kind: kind, DeliveryReview: &DeliveryReviewSpec{Version: 1}}
}

func TestDeliveryReviewDefersWriteOnceAndUsesNormalEvidenceNotes(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "版本二\n仅非空不证明合法。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read", "a.md"))
		case 2:
			return modelCalls(reviewWriteCall("draft", "不应提前写入", nil))
		case 3:
			if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("候选提前写入: %v", err)
			}
			r := currentRun(ctx)
			if len(r.state.Pending) != 0 {
				t.Fatal("退回创建Pending")
			}
			last := messages[len(messages)-1]
			if !strings.Contains(last.Content, "delivery_review_required") || strings.Contains(last.Content, "系统救援指南") {
				t.Fatalf("退回被当普通失败: %s", last.Content)
			}
			if r.state.Usage.ModelCalls != 3 || r.state.Usage.Turns != 3 {
				t.Fatalf("票据重复扣数: %+v", r.state.Usage)
			}
			var e AnalysisEvidence
			for _, v := range analysisIntegrationEvidence(ctx) {
				if v.Receipt.Path == "a.md" {
					e = v
				}
			}
			if e.ID == "" {
				return nil, fmt.Errorf("普通阶段交付资格丢失")
			}
			notes := map[string]any{"schema_version": 1, "findings": []any{map[string]any{"id": "F1", "kind": "fact", "statement": "原文首行是版本二", "context": "仅限本文件", "evidence": []any{map[string]any{"evidence_id": e.ID, "start_line": 1, "end_line": 1, "quote": "版本二"}}}}, "processed_reads": []string{e.ID}, "summaries": map[string]string{e.ID: "已确认首行版本二，结论限本文件原文"}, "source_reviews": []any{map[string]any{"evidence_ids": []string{e.ID}, "finding_ids": []string{"F1"}, "disposition": "addressed", "reason": "版本与适用范围已形成有来源发现"}}}
			return modelCalls(reviewWriteCall("publish", "版本二，结论仅限原文。", notes))
		case 4:
			return reply(`{"ok":true}`)
		}
		return nil, fmt.Errorf("不应增加模型调用%d", calls)
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 4, MaxToolCalls: 6, MaxTurns: 4}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("一次交付", dir), deliverySpec(tools.ModeWorkspace), nil)
	if err != nil || result.Status != NeedsVerification || calls != 4 || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 3 || result.Usage.Turns != 4 {
		t.Fatalf("结果/预算错误: %+v calls%d err%v", result, calls, err)
	}
	q := result.DeliveryReview
	if q == nil || q.Status != "completed" || q.AdmissionCount != 1 || q.Addressed != 1 || len(q.Unresolved) != 0 {
		t.Fatalf("处置未闭环: %+v", q)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
	if string(raw) != "版本二，结论仅限原文。" || result.FinalText != `{"ok":true}` {
		t.Fatalf("正文或业务JSON被改写: %s / %s", raw, result.FinalText)
	}
	if len(result.Delivery.Effects) != 1 {
		t.Fatalf("退回产生额外效果: %+v", result.Delivery)
	}
}

func TestDeliveryReviewFreeJSONMayRemainUnresolvedWithoutExtraTool(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "具体条件。\n")
	calls := 0
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("read", "a.md"))
		}
		if calls == 2 {
			return reply(`{"draft":true}`)
		}
		if calls == 3 {
			return reply(`{"answer":"部分结果"}`)
		}
		return nil, fmt.Errorf("重复处理")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 3, MaxToolCalls: 4, MaxTurns: 3}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("自由JSON", dir), deliverySpec(tools.ModeReadOnly), nil)
	if err != nil || result.Status != Answered || calls != 3 || result.Usage.ToolCalls != 1 || result.FinalText != `{"answer":"部分结果"}` {
		t.Fatalf("自由终答受损: %+v / %v", result, err)
	}
	if result.DeliveryReview.Status != "completed" || result.DeliveryReview.AdmissionCount != 1 || len(result.DeliveryReview.Unresolved) == 0 {
		t.Fatalf("未记录未解决项: %+v", result.DeliveryReview)
	}
	if c := result.DeliveryReview.ContentComparisons; len(c) != 1 || c[0].Kind != "answer" || c[0].Status != "changed" || c[0].ConfirmedSHA256 != deliveryContentSHA(result.FinalText) {
		t.Fatalf("自由JSON终答未比较或摘要错误: %+v", c)
	}
}

func TestDeliveryReviewBudgetUnavailableKeepsLastAllowedWrite(t *testing.T) {
	for _, toolsLimit := range []int{2, 8} {
		t.Run(fmt.Sprint(toolsLimit), func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "a.md", "来源。\n")
			calls := 0
			p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return modelCalls(readCall("read", "a.md"))
				}
				if calls == 2 {
					return modelCalls(reviewWriteCall("write", "最后一次允许的写入", nil))
				}
				return nil, fmt.Errorf("越过模型额度")
			})
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 2, MaxToolCalls: toolsLimit, MaxTurns: 5}})
			result, _ := eng.Run(context.Background(), ctxpkg.NewSession("最后额度", dir), deliverySpec(tools.ModeWorkspace), nil)
			if result.Status != BudgetExhausted || calls != 2 || result.Usage.ModelCalls != 2 || result.Usage.ToolCalls != 2 {
				t.Fatalf("越预算或终态错误: %+v calls%d", result, calls)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
			if string(raw) != "最后一次允许的写入" || len(result.Delivery.Effects) != 1 {
				t.Fatalf("最后写入被拦截: %s %+v", raw, result.Delivery)
			}
			if result.DeliveryReview.AdmissionCount != 0 || result.DeliveryReview.ErrorClass != "budget_unavailable" {
				t.Fatalf("错误预准入: %+v", result.DeliveryReview)
			}
		})
	}
}

func TestDeliveryReviewCancelledAdmissionDoesNotRepeatOnResume(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "原文事实。\n")
	parent, cancel := context.WithCancel(context.Background())
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("read", "a.md"))
		}
		if calls == 2 {
			r := currentRun(ctx)
			r.mu.Lock()
			prior := r.persist
			r.persist = func(kind string, s taskState) error {
				err := prior(kind, s)
				if err == nil && kind == "delivery_review_evaluated" {
					cancel()
				}
				return err
			}
			r.mu.Unlock()
			return modelCalls(reviewWriteCall("draft", "候选", nil))
		}
		return nil, fmt.Errorf("取消后不应请求模型")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 8, MaxToolCalls: 8, MaxTurns: 8}})
	result, _ := eng.Run(parent, ctxpkg.NewSession("取消准入", dir), deliverySpec(tools.ModeWorkspace), nil)
	if result.Status != Cancelled || calls != 2 || result.DeliveryReview.AdmissionCount != 1 || result.DeliveryReview.Status != "outcome_unknown" {
		t.Fatalf("取消准入结果: %+v calls%d", result, calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("取消后发生写入")
	}
	resumedCalls := 0
	eng.provider = scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		resumedCalls++
		if modelStage(ctx) == "delivery_review" {
			t.Fatal("恢复重复发起处理")
		}
		if resumedCalls == 1 {
			return modelCalls(reviewWriteCall("new", "恢复后正常写入", nil))
		}
		return reply("已交付，历史处理未确认。")
	})
	resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("取消准入", dir), TaskSpec{ID: result.TaskID, Resume: true, Mode: tools.ModeWorkspace}, nil)
	if err != nil || resumed.Status != NeedsVerification || resumedCalls != 2 || resumed.DeliveryReview.AdmissionCount != 1 || resumed.DeliveryReview.Status != "outcome_unknown" {
		t.Fatalf("恢复改变一次性状态: %+v / %v", resumed, err)
	}
	taskRaw, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", result.TaskID, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInspectionSnapshot(taskRaw); err != nil {
		t.Fatalf("状态3无法Inspection: %v", err)
	}
	var snapshot taskstore.Snapshot
	if err := json.Unmarshal(taskRaw, &snapshot); err != nil {
		t.Fatal(err)
	}
	var state taskState
	json.Unmarshal(snapshot.State, &state)
	if state.Version != 3 {
		t.Fatalf("状态版本=%d", state.Version)
	}
}

func TestDeliveryReviewAdaptiveCheckCounterexampleChangesOutput(t *testing.T) {
	for _, value := range []string{"false", "0"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			source := `{"feature":` + value + `}`
			writeFixture(t, dir, "source.json", source)
			calls := 0
			checkID := ""
			p := scriptedProvider(func(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				r := currentRun(ctx)
				switch calls {
				case 1:
					return modelCalls(readCall("source", "source.json"))
				case 2:
					return modelCalls(reviewWriteCall("candidate", "feature不存在", nil))
				case 3:
					var e AnalysisEvidence
					for _, x := range analysisIntegrationEvidence(ctx) {
						if x.Receipt.Path == "source.json" {
							e = x
						}
					}
					args, _ := json.Marshal(map[string]any{"checks": []any{map[string]any{"kind": "json", "subject": map[string]any{"file_ref": r.fileRefForEvidence(ctx, e), "byte_start": 0, "byte_end": len(source)}, "assertions": []any{map[string]any{"op": "absent", "pointer": "/feature"}}}}})
					return modelCalls(schema.ToolCall{ID: "check", Name: "check_evidence", Arguments: args})
				case 4:
					last := msgs[len(msgs)-1].Content
					at := strings.Index(last, "{")
					if at < 0 {
						return nil, fmt.Errorf("没有真实核对回执")
					}
					var observed struct {
						Checks []struct {
							ID     string `json:"id"`
							Result string `json:"result"`
						} `json:"checks"`
					}
					if err := json.Unmarshal([]byte(last[at:]), &observed); err != nil || len(observed.Checks) != 1 || observed.Checks[0].Result != "fail" {
						return nil, fmt.Errorf("未获得缺失断言的实际反例: %s", last)
					}
					checkID = observed.Checks[0].ID
					var e AnalysisEvidence
					for _, x := range analysisIntegrationEvidence(ctx) {
						if x.Receipt.Path == "source.json" {
							e = x
						}
					}
					var obj map[string]json.RawMessage
					if err := json.Unmarshal(e.Source, &obj); err != nil {
						return nil, err
					}
					actual, exists := obj["feature"]
					if !exists {
						return nil, fmt.Errorf("脚本必须根据实际来源修订")
					}
					statement := "feature字段存在，值为" + string(actual) + "；不等于缺失。"
					notes := map[string]any{"schema_version": 1, "findings": []any{map[string]any{"id": "json-presence", "kind": "fact", "statement": statement, "context": "只对已选JSON对象成立", "evidence": []any{map[string]any{"evidence_id": e.ID, "start_line": 1, "end_line": 1, "quote": source}}}}, "source_reviews": []any{map[string]any{"evidence_ids": []string{e.ID}, "finding_ids": []string{"json-presence"}, "check_ids": []string{checkID}, "disposition": "addressed", "reason": "核对缺失断言得反例，已据实际对象修正"}}}
					return modelCalls(reviewWriteCall("corrected", statement, notes))
				case 5:
					return reply("交付完成；只证明所选对象。")
				}
				return nil, fmt.Errorf("不应另开评审或修复模型阶段")
			})
			spec := deliverySpec(tools.ModeWorkspace)
			spec.ReadFeatures = &ReadFeatureSpec{Version: 1, LocalChecks: true}
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 5, MaxToolCalls: 6, MaxTurns: 5}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("根据反例修订", dir), spec, nil)
			if err != nil || result.Status != NeedsVerification || calls != 5 || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 4 {
				t.Fatalf("自适应流程失败: %+v / %v", result, err)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
			if !strings.Contains(string(raw), "值为"+value) || strings.Contains(string(raw), "不存在") {
				t.Fatalf("未按真实反例修订: %s", raw)
			}
			if len(result.LocalChecks) != 1 || result.LocalChecks[0].ID != checkID || result.LocalChecks[0].Result != "fail" {
				t.Fatalf("反例被误改为通过: %+v", result.LocalChecks)
			}
			if len(result.DeliveryReview.Unresolved) != 0 || result.DeliveryReview.AdmissionCount != 1 {
				t.Fatalf("采用反例未闭环: %+v", result.DeliveryReview)
			}
		})
	}
}

func TestDeliveryReviewInspectionRejectsResetTicket(t *testing.T) {
	spec := deliverySpec(tools.ModeReadOnly)
	spec.ReadContextVersion = 2
	admitted := &DeliveryReviewState{Version: 1, Status: "admitted", Evaluated: true, AdmissionCount: 1, OriginRunID: strings.Repeat("a", 32)}
	s := taskState{Version: 3, Spec: spec, DeliveryReview: admitted}
	raw, _ := json.Marshal(s)
	var view Inspection
	if err := view.Apply(taskstore.Event{Version: 1, Sequence: 1, Kind: "started", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	s.DeliveryReview = &DeliveryReviewState{Version: 1, Status: "not_run"}
	raw, _ = json.Marshal(s)
	if err := view.Apply(taskstore.Event{Version: 1, Sequence: 2, Kind: "action_result", Payload: raw}); err == nil {
		t.Fatal("只读Inspection接受票据重置")
	}
}

func TestDeliveryReviewCandidateEvidenceFailureStopsBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "原文。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("read", "a.md"))
		}
		if calls == 2 {
			r := currentRun(ctx)
			r.mu.Lock()
			r.artifact = func(string, []byte) error { return fmt.Errorf("注入材料写盘失败") }
			r.mu.Unlock()
			return modelCalls(reviewWriteCall("draft", "不可落盘的候选", nil))
		}
		return nil, fmt.Errorf("不应增加模型")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, _ := eng.Run(context.Background(), ctxpkg.NewSession("证据故障", dir), deliverySpec(tools.ModeWorkspace), nil)
	if result.Status != Failed || result.StopCode != "storage_error" || calls != 2 || result.FinalText != "" {
		t.Fatalf("证据失败被降级: %+v calls%d", result, calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("关键证据失败后写入")
	}
}

func TestDeliveryReviewSnapshotFailureRecoversCommittedVersionThreeEvents(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root可绕过目录只读权限")
	}
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "实际原文。\n")
	calls := 0
	taskDir := ""
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read", "a.md"))
		case 2:
			return modelCalls(reviewWriteCall("candidate", "候选", nil))
		case 3:
			taskDir = filepath.Join(dir, ".claw", "tasks", currentRun(ctx).state.Spec.ID)
			if err := os.Chmod(taskDir, 0500); err != nil {
				return nil, err
			}
			return modelCalls(reviewWriteCall("actual", "只执行一次的正式文件", nil))
		case 4:
			return reply("已交付，仍有未解决事项。")
		}
		return nil, fmt.Errorf("意外模型")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("快照故障", dir), deliverySpec(tools.ModeWorkspace), nil)
	if taskDir != "" {
		if chmodErr := os.Chmod(taskDir, 0700); chmodErr != nil {
			t.Fatal(chmodErr)
		}
	}
	if err != nil || result.Status != NeedsVerification || result.DeliveryReview.AdmissionCount != 1 {
		t.Fatalf("快照更新失败改坏已提交结果: %+v / %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(taskDir, "snapshot.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未真正注入快照失败: %v", err)
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.OpenTask(result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	state, err := loadTaskState(task)
	if err != nil || state.Version != 3 || state.DeliveryReview.AdmissionCount != 1 || state.DeliveryReview.Status != "completed" {
		t.Fatalf("无法从事件恢复v3: %+v / %v", state.DeliveryReview, err)
	}
}

func TestDeliveryReviewEmptyPlaceholderDoesNotConsumeOpportunity(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "真正需要处理的材料。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(reviewWriteCall("placeholder", "占位", nil))
		case 2:
			return modelCalls(readCall("source", "a.md"))
		case 3, 4:
			r := currentRun(ctx)
			content := "候选"
			if calls == 4 {
				content = "正式提交"
				if modelStage(ctx) != "delivery_review" {
					t.Fatal("空目录过早消耗处理机会")
				}
				raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
				if string(raw) != "占位" {
					t.Fatal("候选被提前执行")
				}
			}
			version := r.state.EffectLedger.Effects[0].File.AfterVersion
			raw, _ := json.Marshal(map[string]any{"path": "result.md", "content": content, "expected_version": version})
			return modelCalls(schema.ToolCall{ID: fmt.Sprint(calls), Name: "write_file", Arguments: raw})
		case 5:
			return reply("已交付，有未核实材料。")
		}
		return nil, fmt.Errorf("重复机会")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 5, MaxToolCalls: 7, MaxTurns: 5}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("先占位后阅读", dir), deliverySpec(tools.ModeWorkspace), nil)
	if err != nil || result.Status != NeedsVerification || result.DeliveryReview.AdmissionCount != 1 || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 4 {
		t.Fatalf("占位流程错误: %+v / %v", result, err)
	}
	if len(result.Delivery.Effects) != 2 {
		t.Fatalf("占位与正式写入回执不完整: %+v", result.Delivery)
	}
}

func TestDeliveryReviewCandidatePersistsOnlyDisplayTextAndToolIntents(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "来源事实。\n")
	calls := 0
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("read", "a.md"))
		}
		if calls == 2 {
			return &schema.Message{Role: schema.RoleAssistant, Content: "用户可见的候选答复", ResponseItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque-only-should-stay-in-session"}`)}}, nil
		}
		return reply("只交付有效部分。")
	})
	session := ctxpkg.NewSession("候选最小留存", dir)
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly})
	result, err := eng.Run(context.Background(), session, deliverySpec(tools.ModeReadOnly), nil)
	if err != nil || result.Status != Answered {
		t.Fatalf("结果:%+v/%v", result, err)
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.OpenTask(result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	state, err := loadTaskState(task)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := task.ReadArtifact(state.DeliveryReview.CandidateArtifactID, 0, 4000)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || string(fields["content"]) != `"用户可见的候选答复"` || strings.Contains(string(raw), "opaque-only") {
		t.Fatalf("候选越界持久化: %s", raw)
	}
	foundOpaque := false
	for _, m := range session.GetWorkingMemory(10000) {
		for _, item := range m.ResponseItems {
			if strings.Contains(string(item), "opaque-only") {
				foundOpaque = true
			}
		}
	}
	if !foundOpaque {
		t.Fatal("原Session opaque项被移除")
	}
}

func TestDeliveryReviewPersistentStateRequiresCanonicalReadPolicy(t *testing.T) {
	for _, v := range []int{0, 1, 3, 5} {
		spec := deliverySpec(tools.ModeReadOnly)
		spec.ReadContextVersion = v
		s := taskState{Version: 3, Spec: spec, DeliveryReview: &DeliveryReviewState{Version: 1, Status: "not_run"}}
		if ValidateTaskStateVersion(s) == nil {
			t.Fatalf("接受损坏持久读取策略%d", v)
		}
	}
}
