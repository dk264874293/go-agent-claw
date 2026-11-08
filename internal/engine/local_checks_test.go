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
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func localCheckFixture(t *testing.T, source string) (*runState, context.Context, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(context.Background(), tools.ExecutionScope{Mode: tools.ModeWorkspace, WorkDir: dir})
	ctx = observe.WithAgent(ctx, "root", "")
	ctx = observe.WithModelCall(ctx, "model-1")
	ctx = observe.WithAction(ctx, "action-1")
	version, err := tools.FileVersion(ctx, "source.md")
	if err != nil {
		t.Fatal(err)
	}
	r := &runState{runID: "run-1", started: time.Now(), state: taskState{Version: 1, Spec: TaskSpec{ReadContextVersion: 3, Mode: tools.ModeWorkspace, Kind: TaskChange}}, analysisRuntime: analysisRuntime{sources: map[string]AnalysisEvidence{}, delivered: map[string]map[string]bool{"root": {"E1": true}}}}
	receipt := schema.ReadReceipt{Path: "source.md", Version: version, ByteStart: 0, ByteEnd: int64(len(source)), FirstLine: 1, LastLine: strings.Count(strings.TrimSuffix(source, "\n"), "\n") + 1, TotalBytes: int64(len(source)), AgentID: "root", ActionID: "read-1", EvidenceID: "E1", ArtifactID: "source-artifact", LineBoundary: &schema.ReadBoundary{StartsLine: true, EndsLine: true}}
	r.analysisRuntime.sources["E1"] = AnalysisEvidence{ID: "E1", ActionID: "read-1", AgentID: "root", RunID: r.runID, Receipt: receipt, ArtifactID: receipt.ArtifactID, Source: []byte(source)}
	artifacts := map[string][]byte{}
	r.artifact = func(id string, body []byte) error { artifacts[id] = append([]byte(nil), body...); return nil }
	ctx = context.WithValue(ctx, runContextKey{}, r)
	return r, ctx, artifacts
}

func executeLocalCheckFixture(t *testing.T, ctx context.Context, raw string) (string, error) {
	t.Helper()
	return (localCheckTool{}).Execute(ctx, json.RawMessage(raw))
}

func TestLocalCheckTimestampExactSubjectAndAnchor(t *testing.T) {
	source := "好样本：\r\n{\r\n  \"collected_at\": \"2026-11-07\",\r\n  \"score\": 5\r\n}\r\n坏样本：\r\n{\"score\":0}\r\n"
	r, ctx, artifacts := localCheckFixture(t, source)
	raw := `{"checks":[{"kind":"json","sample_label":"好样本","subject":{"evidence_ids":["E1"],"start_line":2,"end_line":5},"anchor":{"subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"quote":"好样本"},"assertions":[{"op":"absent","pointer":"/collected_at"}]},{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":7,"end_line":7},"assertions":[{"op":"absent","pointer":"/collected_at"}]}]}`
	output, err := executeLocalCheckFixture(t, ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(output)) || len(r.state.LocalChecks) != 2 || r.state.LocalChecks[0].Result != "fail" || r.state.LocalChecks[1].Result != "pass" {
		t.Fatalf("对象字段检查错误：%s %+v", output, r.state.LocalChecks)
	}
	first, second := r.state.LocalChecks[0], r.state.LocalChecks[1]
	if first.ID == second.ID || first.ModelCallID != "model-1" || first.ActionID != "action-1" || first.ReceiptArtifactID != second.ReceiptArtifactID {
		t.Fatalf("核对身份不正确：%+v", r.state.LocalChecks)
	}
	var saved localCheckArtifact
	if err := json.Unmarshal(artifacts[first.ReceiptArtifactID], &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Checks) != 2 || len(saved.Receipts) != 2 || saved.Receipts[0].Assertions[0].Present != true || saved.Receipts[0].Sources[0].FirstLine != 2 {
		t.Fatalf("未保存完整断言与来源：%+v", saved)
	}
	if strings.Contains(r.localCheckContext(ctx), "collected_at") || !strings.Contains(r.localCheckContext(ctx), first.ID) {
		t.Fatal("上下文应保留简短身份而非回放主体或断言正文")
	}
	// 模型自报名称不改变客观选择主体；错误锚点不能让检查通过。
	badAnchor := strings.Replace(raw, `"quote":"好样本"`, `"quote":"不存在的锚点"`, 1)
	_, err = executeLocalCheckFixture(t, ctx, badAnchor)
	if err != nil || r.state.LocalChecks[2].Result != "not_run" || r.state.LocalChecks[2].PremiseResult != "not_run" {
		t.Fatalf("错误锚点误报结论：%+v %v", r.state.LocalChecks, err)
	}
}

func TestLocalCheckJSONMissingAndDefaultPremise(t *testing.T) {
	r, ctx, _ := localCheckFixture(t, `{"null":null,"false":false,"zero":0,"empty":""}`)
	assertions := []quality.Assertion{{Op: "exists", Pointer: "/null"}, {Op: "default", Pointer: "/null", Default: json.RawMessage(`5`), Expected: json.RawMessage(`null`)}, {Op: "default", Pointer: "/false", Default: json.RawMessage(`5`), Expected: json.RawMessage(`false`)}, {Op: "default", Pointer: "/zero", Default: json.RawMessage(`5`), Expected: json.RawMessage(`0`)}, {Op: "default", Pointer: "/empty", Default: json.RawMessage(`5`), Expected: json.RawMessage(`""`)}, {Op: "default", Pointer: "/missing", Default: json.RawMessage(`5`), Expected: json.RawMessage(`5`)}}
	body, _ := json.Marshal(map[string]any{"checks": []any{map[string]any{"kind": "json", "subject": map[string]any{"evidence_ids": []string{"E1"}, "start_line": 1, "end_line": 1}, "assertions": assertions}}})
	_, err := executeLocalCheckFixture(t, ctx, string(body))
	if err != nil || len(r.state.LocalChecks) != 1 || r.state.LocalChecks[0].Result != "pass" || r.state.LocalChecks[0].PremiseResult != "not_run" {
		t.Fatalf("默认代入冒充代码前提：%+v %v", r.state.LocalChecks, err)
	}
}

func TestLocalCheckNumericCounterexampleAndUnsupportedCode(t *testing.T) {
	r, ctx, artifacts := localCheckFixture(t, "score * 2.5\n")
	raw := `{"checks":[{"kind":"numeric","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"expression":"score * 2.5","premise":"source_arithmetic","bindings":{"score":{"kind":"test_input"}},"cases":[{"name":"缺省样本仅代入","inputs":{"score":"5"},"expected":"10"}]},{"kind":"numeric","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"expression":"score * 2.5","premise":"translated","bindings":{"score":{"kind":"test_input"}},"cases":[{"inputs":{"score":"5"},"expected":"12.5"}]},{"kind":"numeric","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"expression":"min(score, 10)","bindings":{"score":{"kind":"test_input"}},"cases":[{"inputs":{"score":"5"},"expected":"5"}]}]}`
	_, err := executeLocalCheckFixture(t, ctx, raw)
	if err != nil || len(r.state.LocalChecks) != 3 {
		t.Fatalf("数值核对失败：%+v %v", r.state.LocalChecks, err)
	}
	if r.state.LocalChecks[0].Result != "fail" || r.state.LocalChecks[1].Result != "pass" || r.state.LocalChecks[1].PremiseResult != "not_run" || r.state.LocalChecks[2].Result != "error" {
		t.Fatalf("数学结果与翻译前提混淆：%+v", r.state.LocalChecks)
	}
	var saved localCheckArtifact
	_ = json.Unmarshal(artifacts[r.state.LocalChecks[0].ReceiptArtifactID], &saved)
	if saved.Receipts[0].Cases[0].Actual != "25/2" {
		t.Fatalf("没有实际精确计算：%+v", saved.Receipts[0])
	}
}

func TestLocalCheckMalformedSourceAndPolicyIsolation(t *testing.T) {
	r, ctx, artifacts := localCheckFixture(t, "{}\n")
	for _, raw := range []string{
		`{"checks":[]}`, `{"checks":null}`, `{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1},"assertions":[]}]}`,
		`{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1,"byte_start":null},"assertions":[]}]}`,
		`{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1,"byte_start":0,"byte_end":2},"assertions":[]}]}`,
		`{"checks":[{"kind":"json","id":"伪造身份","subject":{"evidence_ids":["E1"],"byte_start":0,"byte_end":2}}]}`,
	} {
		if _, err := executeLocalCheckFixture(t, ctx, raw); err == nil {
			t.Fatalf("非法请求获准：%s", raw)
		}
	}
	if len(artifacts) != 0 || len(r.state.LocalChecks) != 0 {
		t.Fatal("参数错误不应创建检查回执")
	}
	raw := `{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"assertions":[{"op":"absent","pointer":"/x"}]}]}`
	for _, version := range []int{0, 1, 2} {
		r.state.Spec.ReadContextVersion = version
		if _, err := executeLocalCheckFixture(t, ctx, raw); err == nil {
			t.Fatalf("旧策略%d被自动升级", version)
		}
	}
	r.state.Spec.ReadContextVersion = 3
	for _, mutate := range []func(){func() { r.state.Spec.Analysis = true }, func() { r.state.Spec.Analysis = false; r.state.Spec.Report = &ReportSpec{} }, func() { r.state.Spec.Report = nil; r.state.Spec.Quality = &QualitySpec{Version: 1} }} {
		mutate()
		if _, err := executeLocalCheckFixture(t, ctx, raw); err == nil {
			t.Fatal("专用契约不得被普通检查旁路")
		}
	}
}

func TestLocalCheckSourcesMustBeCurrentDeliveredAndAuthorized(t *testing.T) {
	for _, mode := range []string{"undelivered", "foreign", "future", "invalidated", "stale", "permission", "resume"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx, _ := localCheckFixture(t, "{}\n")
			source := r.analysisRuntime.sources["E1"]
			switch mode {
			case "undelivered":
				r.analysisRuntime.delivered["root"]["E1"] = false
			case "foreign":
				source.AgentID = "sibling"
				ctx = observe.WithAgent(ctx, "child", "root")
				ctx = observe.WithAction(ctx, "child-action")
			case "future":
				delete(r.analysisRuntime.sources, "E1")
			case "invalidated":
				r.analysisRuntime.invalidated = map[string]string{"E1": "bash_started"}
			case "stale":
				if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "source.md"), []byte("{\"x\":1}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "permission":
				scope := tools.ScopeFromContext(ctx)
				scope.AllowedPaths = []string{"other.md"}
				ctx = tools.WithScope(ctx, scope)
			case "resume":
				r.runID = "run-2"
			}
			if mode != "future" {
				r.analysisRuntime.sources["E1"] = source
			}
			_, err := executeLocalCheckFixture(t, ctx, `{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"assertions":[{"op":"absent","pointer":"/x"}]}]}`)
			if err != nil || len(r.state.LocalChecks) != 1 || r.state.LocalChecks[0].Result != "not_run" {
				t.Fatalf("失效来源获得结论：%+v %v", r.state.LocalChecks, err)
			}
		})
	}
}

func TestLocalCheckPreparedPersistenceAndStorageFailure(t *testing.T) {
	for _, mode := range []string{"success", "prepared_event", "artifact", "completed_event"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx, artifacts := localCheckFixture(t, "{}\n")
			var events []string
			r.persist = func(kind string, state taskState) error {
				events = append(events, kind)
				if mode == "prepared_event" && kind == "local_checks_prepared" || mode == "completed_event" && kind == "local_checks_completed" {
					return fmt.Errorf("注入事件失败")
				}
				if kind == "local_checks_prepared" && (len(artifacts) != 0 || len(state.LocalChecks) != 1 || state.LocalChecks[0].Result != "not_run" || state.LocalChecks[0].ReceiptArtifactID == "") {
					t.Fatal("必须先登记私有材料身份再保存完整回执")
				}
				return nil
			}
			if mode == "artifact" {
				r.artifact = func(string, []byte) error { return fmt.Errorf("注入写盘失败") }
			}
			_, err := executeLocalCheckFixture(t, ctx, `{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"start_line":1,"end_line":1},"assertions":[{"op":"absent","pointer":"/x"}]}]}`)
			if mode == "success" {
				if err != nil || strings.Join(events, ",") != "local_checks_prepared,local_checks_completed" || len(r.state.LocalChecks) != 1 {
					t.Fatalf("回执未按ID更新：%+v %v", r.state.LocalChecks, err)
				}
			} else if !errors.Is(err, errStorage) || r.storageError() == nil {
				t.Fatalf("关键存储失败没有停止：%v", err)
			}
			if (mode == "artifact" || mode == "completed_event") && (r.state.LocalChecks[0].Result != "not_run" || r.state.LocalChecks[0].Reason != "receipt_not_confirmed") {
				t.Fatal("未保存材料不得宣称完成")
			}
			if mode == "prepared_event" && len(artifacts) != 0 {
				t.Fatal("私有身份未持久化前写入了完整回执")
			}
		})
	}
}

func TestLocalCheckJSONStrictObjectsAndByteSelection(t *testing.T) {
	for _, test := range []struct{ name, source, result string }{
		{"escaped_duplicate", `{"field":0,"\u0066ield":1}`, "error"},
		{"multiple_objects", `{"collected_at":1} {"other":2}`, "error"},
		{"array_not_object", `[{"collected_at":1}]`, "error"},
		{"invalid_pointer", `{"collected_at":1}`, "error"},
		{"nested_same_name", `{"good":{"collected_at":1},"bad":{}}`, "pass"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, ctx, _ := localCheckFixture(t, test.source)
			pointer := "/collected_at"
			if test.name == "invalid_pointer" {
				pointer = "/collected~2at"
			}
			if test.name == "nested_same_name" {
				pointer = "/good/collected_at"
			}
			body, _ := json.Marshal(map[string]any{"checks": []any{map[string]any{"kind": "json", "subject": map[string]any{"evidence_ids": []string{"E1"}, "byte_start": 0, "byte_end": len(test.source)}, "assertions": []quality.Assertion{{Op: "exists", Pointer: pointer}}}}})
			_, err := executeLocalCheckFixture(t, ctx, string(body))
			if err != nil || len(r.state.LocalChecks) != 1 || r.state.LocalChecks[0].Result != test.result {
				t.Fatalf("严格JSON范围检查失败：%+v %v", r.state.LocalChecks, err)
			}
		})
	}
	r, ctx, _ := localCheckFixture(t, "{\"中文\":1}")
	_, err := executeLocalCheckFixture(t, ctx, `{"checks":[{"kind":"json","subject":{"evidence_ids":["E1"],"byte_start":3,"byte_end":8},"assertions":[{"op":"exists","pointer":"/中文"}]}]}`)
	if err != nil || r.state.LocalChecks[0].Result != "not_run" {
		t.Fatalf("字节选择切断UTF-8仍获得结论：%+v %v", r.state.LocalChecks, err)
	}
}

func TestLocalCheckLineSelectionRejectsGapsAndDifferentVersions(t *testing.T) {
	start, end := 1, 3
	selector := localCheckSelector{EvidenceIDs: []string{"E1", "E2"}, StartLine: &start, EndLine: &end}
	for _, mode := range []string{"gap", "different_version", "overlap_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			lookup := func(id string) (quality.Source, error) {
				if id == "E1" {
					return quality.Source{ID: id, Path: "source", Version: "same", ByteStart: 0, ByteEnd: 2, TotalBytes: 11, FirstLine: 1, LastLine: 1, Content: []byte("{\n"), Boundary: &quality.Boundary{StartsLine: true, EndsLine: true}}, nil
				}
				second := quality.Source{ID: id, Path: "source", Version: "same", ByteStart: 8, ByteEnd: 11, TotalBytes: 11, FirstLine: 3, LastLine: 3, Content: []byte(" }\n"), Boundary: &quality.Boundary{StartsLine: true, EndsLine: true}}
				if mode == "different_version" {
					second.Version = "other"
				}
				if mode == "overlap_mismatch" {
					second.ByteStart, second.ByteEnd, second.FirstLine, second.Content = 0, 11, 1, []byte("[\n\"x\":0\n }\n")
				}
				return second, nil
			}
			if _, err := resolveLocalCheckSelector(selector, lookup); err == nil {
				t.Fatal("有缺口、版本不同或冲突片段被拼成有效主体")
			}
		})
	}
}

func TestLocalCheckPaginationAndUTF8LineBoundaries(t *testing.T) {
	content := []byte("说明\r\n{\r\n\"collected_at\":\"今天\"\r\n}\r\n")
	cut := int64(strings.Index(string(content), "今天"))
	base := quality.Source{Path: "source.md", Version: "sha", TotalBytes: int64(len(content))}
	lookup := func(id string) (quality.Source, error) {
		source := base
		source.ID = id
		if id == "E1" {
			source.ByteStart, source.ByteEnd, source.FirstLine, source.Content, source.Boundary = 0, cut, 1, content[:cut], &quality.Boundary{StartsLine: true}
		} else {
			source.ByteStart, source.ByteEnd, source.FirstLine, source.Content, source.Boundary = cut, int64(len(content)), 3, content[cut:], &quality.Boundary{EndsLine: true}
		}
		return source, nil
	}
	start, end := 2, 4
	selector := localCheckSelector{EvidenceIDs: []string{"E1", "E2"}, StartLine: &start, EndLine: &end}
	resolved, err := resolveLocalCheckSelector(selector, lookup)
	if err != nil {
		t.Fatal(err)
	}
	result := quality.Run([]quality.Check{{ID: "K", FindingID: "K", Kind: "json", Subject: resolved, Assertions: []quality.Assertion{{Op: "exists", Pointer: "/collected_at"}}}}, lookup)
	if result[0].Result != "pass" || result[0].Sources[0].FirstLine != 2 || result[0].Sources[0].LastLine != 4 {
		t.Fatalf("跨页行主体不正确：%+v", result[0])
	}
	selector.EvidenceIDs = []string{"E2"}
	if _, err := resolveLocalCheckSelector(selector, lookup); err == nil {
		t.Fatal("半行读取不能证明未读的对象开头")
	}
}

func TestLocalCheckOutputStaysCompleteBoundedJSON(t *testing.T) {
	receipts := make([]quality.Receipt, 8)
	for i := range receipts {
		receipts[i] = quality.Receipt{ID: fmt.Sprint(i), Kind: "json", Result: "fail", PremiseResult: "pass", Scope: strings.Repeat("范围", 200), Reason: strings.Repeat("错误字符", 2000), Sources: []quality.SourceRef{{EvidenceIDs: []string{"E1"}, Path: strings.Repeat("目录/", 1500), Version: "sha"}}}
	}
	output := localCheckOutput("artifact", receipts)
	if !json.Valid([]byte(output)) || len(output) > 8000 || !strings.Contains(output, `"omitted":8`) {
		t.Fatalf("输出截断JSON或未标省略：len=%d %s", len(output), output)
	}
}

func TestLocalCheckAdaptiveNormalLoopNoIndependentModelPhase(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "{\"collected_at\":\"2026-11-07\"}\n")
	calls := 0
	provider := scriptedProvider(func(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if modelStage(ctx) == "quality_review" || modelStage(ctx) == "quality_revision" {
			t.Fatal("普通核对新增了独立模型阶段")
		}
		switch calls {
		case 1:
			return modelCalls(readCall("read", "source.md"))
		case 2:
			entries := analysisIntegrationEvidence(ctx)
			if len(entries) != 1 {
				t.Fatalf("没有实际原文：%+v", entries)
			}
			return modelCalls(schema.ToolCall{ID: "check", Name: "check_evidence", Arguments: json.RawMessage(fmt.Sprintf(`{"checks":[{"kind":"json","subject":{"evidence_ids":[%q],"start_line":1,"end_line":1},"assertions":[{"op":"absent","pointer":"/collected_at"}]}]}`, entries[0].ID))})
		case 3:
			found := false
			for _, msg := range msgs {
				if msg.ToolCallID == "check" && strings.Contains(msg.Content, `"result":"fail"`) {
					found = true
				}
			}
			if !found {
				t.Fatal("作者未收到真实反例")
			}
			return reply("根据已读对象，collected_at 字段存在；先前缺失猜测不成立。")
		default:
			t.Fatalf("出现额外模型调用：%d", calls)
			return nil, nil
		}
	})
	eng := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("普通核对", dir), TaskSpec{Goal: "核对样本时间字段，不运行脚本", Mode: tools.ModeReadOnly, Kind: TaskAnswer, ReadContextVersion: 3}, nil)
	if err != nil || result.Status != Answered || result.Usage.ModelCalls != 3 || result.Usage.ToolCalls != 2 || len(result.LocalChecks) != 1 || result.LocalChecks[0].Result != "fail" {
		t.Fatalf("检查失败错误地阻断普通交付或增加调用：%+v %v", result, err)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	if len(state.LocalChecks) != 1 || state.LocalChecks[0].Result != "fail" || state.LastAction == nil || state.LastAction.Result.Status != schema.ToolSucceeded || state.LastAction.Result.Effect != schema.EffectNone {
		t.Fatalf("持久回执不完整：%+v", state.LocalChecks)
	}
}
