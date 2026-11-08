package engine

import (
	"context"
	"encoding/json"
	"fmt"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/tools"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func v2SourceFixture(t *testing.T) (context.Context, *runState) {
	ctx, r, _ := sourceViewFixture(t)
	r.state.Spec.ReadContextVersion = 4
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, SourceViews: true, LocalChecks: true}
	return ctx, r
}
func TestReadSourceV2TypedIdentityAndViewCursor(t *testing.T) {
	ctx, r := v2SourceFixture(t)
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, []byte(`{"id":"E1","review":{"target_id":"E1","question":"核对"}}`)); err == nil || !strings.Contains(err.Error(), "evidence_id") {
		t.Fatalf("短ID没有可操作纠正: %v", err)
	}
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, []byte(`{"id":"artifact","evidence_id":"E1"}`)); err == nil {
		t.Fatal("歧义身份被猜测执行")
	}
	out, receipt, body, err := (evidenceTool{}).ExecuteReadSource(ctx, []byte(`{"evidence_id":"E1","offset":3,"limit":53}`))
	if err != nil || receipt == nil || receipt.ByteStart != 3 || !strings.Contains(out, `"evidence_id":"E1"`) {
		t.Fatalf("选中E游标错误: %q %+v %v", out, receipt, err)
	}
	result := schema.NewToolResult("view", out, schema.ToolSucceeded, schema.EffectNone, "", nil)
	result.Read, result.ReadContent = receipt, body
	bindRead(ctx, "view-action", &result)
	if err := r.captureAnalysisRead(ctx, "view-action", &result); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.rememberAnalysisReadLocked(result)
	r.mu.Unlock()
	id := result.Read.EvidenceID
	raw, _ := json.Marshal(sourceViewArgsV2{EvidenceID: id, Offset: 0, Limit: 4000})
	_, sub, subBody, err := (evidenceTool{}).ExecuteReadSource(ctx, raw)
	if err != nil || sub.ByteStart != receipt.ByteStart || sub.ByteEnd > receipt.ByteEnd || len(subBody) > len(body) || sub.SourceView.OriginEvidenceID != "E1" {
		t.Fatalf("视图E扩展了原范围: %+v %v", sub, err)
	}
	raw, _ = json.Marshal(sourceViewArgsV2{EvidenceID: id, Offset: len(body), Limit: 4000})
	_, eof, empty, err := (evidenceTool{}).ExecuteReadSource(ctx, raw)
	if err != nil || len(empty) != 0 || eof.ByteStart != receipt.ByteEnd {
		t.Fatalf("子视图EOF跳到原材料: %+v %v", eof, err)
	}
}

func TestReadSourceV2OldIdAndNewEvidenceNormalizeSameRange(t *testing.T) {
	ctx, r := v2SourceFixture(t)
	origin := r.analysisRuntime.sources["E1"]
	a, _ := json.Marshal(sourceViewArgsV2{EvidenceID: origin.ID, Offset: 3, Limit: 90})
	b := viewRaw(origin.ArtifactID, 3, 90, nil)
	aa, ak, ok := r.normalizedReadRequest(ctx, schema.ToolCall{Name: "read_evidence", Arguments: a})
	bb, bk, bok := r.normalizedReadRequest(ctx, schema.ToolCall{Name: "read_evidence", Arguments: b})
	if !ok || !bok || ak != bk || *aa.Offset != *bb.Offset || *aa.Limit != *bb.Limit {
		t.Fatalf("换入口改变实际区间: %+v %s %+v %s", aa, ak, bb, bk)
	}
}

func fileRefCheckFixture(t *testing.T) (*runState, context.Context, string) {
	content := "说明\r\n{\r\n\"collected_at\":\"今天\",\r\n\"score\":5\r\n}\r\n"
	r, ctx, _ := localCheckFixture(t, content)
	r.state.Spec.ReadContextVersion = 4
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, LocalChecks: true}
	original := r.analysisRuntime.sources["E1"]
	cut := int64(strings.Index(content, "今天"))
	first := original
	first.Receipt.ByteEnd = cut
	first.Receipt.LastLine = 3
	first.Receipt.LineBoundary = &schema.ReadBoundary{StartsLine: true}
	first.Source = original.Source[:cut]
	second := original
	second.ID, second.ActionID = "E2", "read-2"
	second.Receipt.EvidenceID, second.Receipt.ActionID = "E2", "read-2"
	second.ArtifactID, second.Receipt.ArtifactID = "source-artifact-2", "source-artifact-2"
	second.Receipt.ByteStart = cut
	second.Receipt.FirstLine = 3
	second.Receipt.LineBoundary = &schema.ReadBoundary{EndsLine: true}
	second.Source = original.Source[cut:]
	for _, e := range []AnalysisEvidence{first, second} {
		r.analysisRuntime.sources[e.ID] = e
		rr := schema.ToolResult{Status: schema.ToolSucceeded, Read: &e.Receipt, ReadContent: e.Source}
		r.mu.Lock()
		r.rememberAnalysisReadLocked(rr)
		r.mu.Unlock()
	}
	return r, ctx, "F1"
}
func TestLocalCheckV2FileRefJoinsOnlyDeliveredSlicesAndReportsGap(t *testing.T) {
	r, ctx, ref := fileRefCheckFixture(t)
	start, end := 2, 5
	input := localCheckInput{Kind: "json", Subject: localCheckSelector{FileRef: ref, StartLine: &start, EndLine: &end}, Assertions: []quality.Assertion{{Op: "exists", Pointer: "/collected_at"}}}
	receipt, res := r.runLocalCheckResolved(ctx, "K1", input)
	if receipt.Result != "not_run" || res.ReasonCode == "" || len(res.NextActions) == 0 {
		t.Fatalf("未交付片段没有可操作缺口: %+v %+v", receipt, res)
	}
	r.markAnalysisDelivered(ctx, []string{"E2"})
	receipt, res = r.runLocalCheckResolved(ctx, "K2", input)
	if receipt.Result != "pass" || len(res.ResolvedEvidenceIDs) != 2 || len(receipt.Sources) != 1 || receipt.Sources[0].FirstLine != 2 || receipt.Sources[0].LastLine != 5 {
		t.Fatalf("完整主体未由真实片段核对: %+v %+v", receipt, res)
	}
	if len(r.state.ReadMemory.FileRefs) != 1 || len(r.state.ReadMemory.Evidence) != 2 {
		t.Fatal("核对制造了读取或新句柄")
	}
	legacy := input
	legacy.Subject.FileRef = ""
	legacy.Subject.EvidenceIDs = []string{"E1"}
	if got, _ := r.runLocalCheckResolved(ctx, "K3", legacy); got.Result != "not_run" {
		t.Fatal("兼容显式ID模式偷偷扩大了来源集合")
	}
	r.mu.Lock()
	r.invalidateMemoryLocked("", "bash_dispatched", true)
	r.mu.Unlock()
	receipt, res = r.runLocalCheckResolved(ctx, "K4", input)
	if receipt.Result != "not_run" || res.ReasonCode != "source_ref_stale" {
		t.Fatalf("Bash同SHA恢复旧资格: %+v %+v", receipt, res)
	}
	fresh := r.analysisRuntime.sources["E1"]
	fresh.ID, fresh.ActionID = "E3", "fresh-3"
	fresh.Receipt.EvidenceID, fresh.Receipt.ActionID = "E3", "fresh-3"
	fresh.ArtifactID, fresh.Receipt.ArtifactID = "fresh-material", "fresh-material"
	r.analysisRuntime.sources["E3"] = fresh
	rr := schema.ToolResult{Status: schema.ToolSucceeded, Read: &fresh.Receipt, ReadContent: fresh.Source}
	r.mu.Lock()
	r.rememberAnalysisReadLocked(rr)
	r.mu.Unlock()
	if rr.Read.FileRef != "F2" {
		t.Fatalf("失效后同SHA重用旧文件句柄: %s", rr.Read.FileRef)
	}
	r.runID = "resumed"
	if _, err := r.resolveReadFileRef(ctx, "F1"); err == nil {
		t.Fatal("恢复复用了旧句柄")
	}
}

func TestReadSourceV2AuthorityAndCompleteBoundedFeedback(t *testing.T) {
	ctx, _ := v2SourceFixture(t)
	sibling := observe.WithAgent(ctx, "sibling", "parent")
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(sibling, []byte(`{"evidence_id":"E1"}`)); err == nil {
		t.Fatal("兄弟Agent使用了他人E")
	}
	receipts := []quality.Receipt{{ID: "K1", Kind: "json", Result: "not_run", PremiseResult: "not_run", Reason: strings.Repeat("原因", 200)}}
	res := &LocalSourceResolution{FileRef: "F1", ReasonCode: "source_incomplete", Requested: localCheckSelector{FileRef: "F1"}, NextActions: []LocalSourceAction{{Tool: "read_lines", Arguments: map[string]any{"path": "source.md", "start_line": 1, "end_line": 5}}}}
	for i := 0; i < 128; i++ {
		res.AvailableRanges = append(res.AvailableRanges, ByteInterval{Start: int64(i * 2), End: int64(i*2 + 1)})
		res.ResolvedEvidenceIDs = append(res.ResolvedEvidenceIDs, strings.Repeat("X", 128))
	}
	out := localCheckOutputV2("artifact", receipts, map[string]*LocalSourceResolution{"K1": res})
	var parsed map[string]any
	if len(out) > 8192 || json.Unmarshal([]byte(out), &parsed) != nil || !strings.Contains(out, "next_actions") || !strings.Contains(out, "reason_code") {
		t.Fatalf("反馈裁掉纠正入口或JSON损坏: %d %s", len(out), out)
	}
}

// 行为取决于真实拒绝/缺口/核对回执，不预制成功文本或增加独立模型阶段。
func TestReadSourceV2AdaptiveCorrectionAndFileRefCheck(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "说明\n{\n\"score\":5,\n\"collected_at\":\"今天\"\n}\n")
	calls := 0
	var evidenceID, fileRef string
	var checkArgs json.RawMessage
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(schema.ToolCall{ID: "initial", Name: "read_file", Arguments: []byte(`{"path":"source.md","limit":20,"line_numbers":true}`)})
		case 2:
			entries := analysisIntegrationEvidence(ctx)
			evidenceID = entries[0].ID
			fileRef = entries[0].Receipt.FileRef
			raw, _ := json.Marshal(map[string]any{"id": evidenceID, "review": map[string]any{"target_id": evidenceID, "question": "核对对象位置"}})
			return modelCalls(schema.ToolCall{ID: "wrong-kind", Name: "read_evidence", Arguments: raw})
		case 3:
			if !strings.Contains(messages[len(messages)-1].Content, "evidence_id_required") {
				return nil, fmt.Errorf("未收到身份纠正观察")
			}
			raw, _ := json.Marshal(map[string]any{"evidence_id": evidenceID, "review": map[string]any{"target_id": evidenceID, "question": "核对对象位置"}})
			return modelCalls(schema.ToolCall{ID: "correct-kind", Name: "read_evidence", Arguments: raw})
		case 4:
			checkArgs, _ = json.Marshal(map[string]any{"checks": []any{map[string]any{"kind": "json", "subject": map[string]any{"file_ref": fileRef, "start_line": 2, "end_line": 5}, "assertions": []any{map[string]any{"op": "exists", "pointer": "/collected_at"}}}}})
			return modelCalls(schema.ToolCall{ID: "incomplete-check", Name: "check_evidence", Arguments: checkArgs})
		case 5:
			text := messages[len(messages)-1].Content
			start := strings.Index(text, "{")
			var response struct {
				Checks []struct {
					Result     string                `json:"result"`
					Resolution LocalSourceResolution `json:"resolution"`
				} `json:"checks"`
			}
			if start < 0 || json.Unmarshal([]byte(text[start:]), &response) != nil || len(response.Checks) != 1 || response.Checks[0].Result != "not_run" || len(response.Checks[0].Resolution.NextActions) == 0 {
				return nil, fmt.Errorf("缺口反馈不可操作: %s", text)
			}
			action := response.Checks[0].Resolution.NextActions[0]
			raw, _ := json.Marshal(action.Arguments)
			return modelCalls(schema.ToolCall{ID: "fill-source", Name: action.Tool, Arguments: raw})
		case 6:
			return modelCalls(schema.ToolCall{ID: "completed-check", Name: "check_evidence", Arguments: checkArgs})
		default:
			if !strings.Contains(messages[len(messages)-1].Content, `"result":"pass"`) {
				return nil, fmt.Errorf("未根据实际检查通过交付")
			}
			return reply("指定对象的collected_at字段存在；该检查仅证明此对象字段，不代表代码已运行验收。")
		}
	})
	spec := TaskSpec{Goal: "静态核对指定对象", Mode: tools.ModeReadOnly, Kind: TaskAnswer, ReadContextVersion: 4, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true, LocalChecks: true}}
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 7, MaxToolCalls: 8}}).Run(context.Background(), ctxpkg.NewSession("v2自适应", dir), spec, nil)
	if err != nil || result.Status != Answered || calls != 7 || result.Usage.ModelCalls != 7 || result.Usage.ToolCalls != 6 || len(result.LocalChecks) != 2 || result.LocalChecks[0].Result != "not_run" || result.LocalChecks[1].Result != "pass" {
		t.Fatalf("自适应纠正/调用数失败: %+v %v calls=%d", result, err, calls)
	}
}

func TestReadSourceV2BOnlyChildOwnReceiptAndNoSourcePromotion(t *testing.T) {
	ctx, r, materials := sourceViewFixture(t)
	r.state.Spec.ReadContextVersion = 4
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, LocalChecks: true}
	id := strings.Repeat("a", 32)
	materials[id] = []byte(`{"receipt":"私有核对详情"}`)
	r.state.LocalChecks = []LocalCheckSummary{{ID: "K1", ReceiptArtifactID: id, AgentID: "root", OriginRunID: r.runID, Result: "pass", Sources: []LocalCheckSource{{Path: "E1.md"}}}}
	child := observe.WithAgent(ctx, "root", "parent")
	raw, _ := json.Marshal(map[string]any{"id": id})
	out, read, _, err := (evidenceTool{}).ExecuteReadSource(child, raw)
	if err != nil || read != nil || !strings.Contains(out, "私有核对详情") {
		t.Fatalf("B-only子任务不能读取本人回执或被提升source: %+v %v %s", read, err, out)
	}
	sibling := observe.WithAgent(ctx, "sibling", "parent")
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(sibling, raw); err == nil {
		t.Fatal("兄弟子任务读取了核对回执")
	}
	r.state.LocalChecks[0].OriginRunID = "old-run"
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(child, raw); err == nil {
		t.Fatal("历史回执取得当前子任务资格")
	}
	materials["generic"] = []byte("普通工具材料")
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(child, []byte(`{"id":"generic"}`)); err == nil {
		t.Fatal("B-only扩展了子任务通用材料权限")
	}
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(child, []byte(`{"evidence_id":"E1"}`)); err == nil {
		t.Fatal("B-only开启了source view")
	}
}

func TestLocalCheckV2OutOfBoundsAndTypedConflictHaveNoReadLoop(t *testing.T) {
	r, ctx, ref := fileRefCheckFixture(t)
	r.markAnalysisDelivered(ctx, []string{"E2"})
	start, end := int64(0), int64(999999)
	// 实际请求仍先遵守64KiB主体限制，使用合法大小但超出源文件EOF。
	end = 1000
	receipt, res := r.runLocalCheckResolved(ctx, "K1", localCheckInput{Kind: "json", Subject: localCheckSelector{FileRef: ref, ByteStart: &start, ByteEnd: &end}})
	if receipt.Result != "not_run" || res.ReasonCode != "source_range_out_of_bounds" || len(res.NextActions) != 0 || len(res.MissingByteRanges) != 0 {
		t.Fatalf("EOF外字节被当补读缺口: %+v %+v", receipt, res)
	}
	first, last := 2, 99
	receipt, res = r.runLocalCheckResolved(ctx, "K2", localCheckInput{Kind: "json", Subject: localCheckSelector{FileRef: ref, StartLine: &first, EndLine: &last}})
	if receipt.Result != "not_run" || res.ReasonCode != "source_range_out_of_bounds" || len(res.NextActions) != 0 {
		t.Fatalf("EOF外行号被当补读: %+v %+v", receipt, res)
	}
	a := quality.Source{ID: "E1", Path: "one", Version: "v1", ByteStart: 0, ByteEnd: 3, TotalBytes: 3, FirstLine: 1, LastLine: 1, Content: []byte("abc"), Boundary: &quality.Boundary{StartsLine: true, EndsLine: true}}
	b := a
	b.ID = "E2"
	b.Path = "two"
	sel := localCheckSelector{ByteStart: &start, ByteEnd: ptrInt64(2)}
	if code, _ := preflightLocalParts(sel, []quality.Source{a, b}); code != "source_version_mismatch" {
		t.Fatal(code)
	}
	b = a
	b.ID = "E2"
	b.Content = []byte("axc")
	if code, _ := preflightLocalParts(sel, []quality.Source{a, b}); code != "source_overlap_conflict" {
		t.Fatal(code)
	}
}
func ptrInt64(v int64) *int64 { return &v }
