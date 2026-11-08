package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func sourceViewFixture(t *testing.T) (context.Context, *runState, map[string][]byte) {
	t.Helper()
	ctx, r := carrierFixture(t)
	r.state.Spec.ReadContextVersion = 3
	materials := map[string][]byte{}
	r.state.ReadMemory.SourceArtifacts = map[string]string{}
	for id, e := range r.analysisRuntime.sources {
		e.Receipt.LineBoundary = &schema.ReadBoundary{StartsLine: true, EndsLine: true}
		r.analysisRuntime.sources[id] = e
		persisted := e
		persisted.Source = nil
		r.state.ReadMemory.Evidence[id] = persisted
		r.state.ReadMemory.SourceArtifacts[e.ArtifactID] = id
		materials[e.ArtifactID] = append([]byte(nil), e.Source...)
	}
	r.readArtifactPage = func(id string, offset, limit int) (taskstore.ArtifactPage, error) {
		data, ok := materials[id]
		if !ok {
			return taskstore.ArtifactPage{}, fmt.Errorf("没有材料%s", id)
		}
		if offset < 0 || limit <= 0 {
			return taskstore.ArtifactPage{}, fmt.Errorf("范围错误")
		}
		if offset >= len(data) {
			return taskstore.ArtifactPage{Offset: int64(offset), NextOffset: int64(offset), Total: int64(len(data)), EOF: true}, nil
		}
		if !utf8.RuneStart(data[offset]) {
			return taskstore.ArtifactPage{}, fmt.Errorf("UTF-8字符内部")
		}
		end := min(offset+limit, len(data))
		for end < len(data) && end > offset && !utf8.RuneStart(data[end]) {
			end--
		}
		return taskstore.ArtifactPage{Data: append([]byte{}, data[offset:end]...), Offset: int64(offset), NextOffset: int64(end), Total: int64(len(data)), EOF: end == len(data)}, nil
	}
	return context.WithValue(ctx, runContextKey{}, r), r, materials
}
func viewRaw(id string, offset, limit int, review *schema.ReadReview) json.RawMessage {
	raw, _ := json.Marshal(sourceViewArgs{ID: id, Offset: offset, Limit: limit, Review: review})
	return raw
}

func TestSourceViewNewEvidenceReusesArtifactAndExactSlice(t *testing.T) {
	ctx, r, _ := sourceViewFixture(t)
	origin := r.analysisRuntime.sources["E1"]
	r.state.ReadMemory.ProcessedReads["root"] = []string{"E1"}
	r.state.ReadMemory.Summaries["root"] = map[string]string{"E1": "来源一的事实摘要"}
	saved := 0
	r.artifact = func(string, []byte) error { saved++; return nil }
	out, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, 3, 90, nil))
	if err != nil || read == nil || read.SourceView == nil || !utf8.Valid(raw) {
		t.Fatalf("视图失败: %+v %v", read, err)
	}
	if read.ByteStart != 3 || read.ByteEnd != 3+int64(len(raw)) || read.SourceView.ArtifactEnd != read.ByteEnd || len(out) == 0 || !strings.Contains(out, "续行") {
		t.Fatalf("实际区间或行号不符: %+v %q", read, out)
	}
	result := schema.NewToolResult("view-call", out, schema.ToolSucceeded, schema.EffectNone, "", nil)
	result.Read = read
	result.ReadContent = raw
	bindRead(ctx, "view-action", &result)
	if err := r.captureAnalysisRead(ctx, "view-action", &result); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.rememberAnalysisReadLocked(result)
	r.mu.Unlock()
	if result.Read.EvidenceID == origin.ID || result.Read.ActionID == origin.ActionID || saved != 0 || result.Read.ArtifactID != origin.ArtifactID {
		t.Fatalf("身份或原文重复保存: %+v saved=%d", result.Read, saved)
	}
	entry := r.state.ReadMemory.Evidence[result.Read.EvidenceID]
	content, err := r.readEvidenceContent(entry)
	if err != nil || string(content) != string(raw) {
		t.Fatalf("切片loader扩展了依据: %q %v", content, err)
	}
	if containsString(r.state.ReadMemory.ProcessedReads["root"], entry.ID) || r.analysisRuntime.delivered["root"][entry.ID] {
		t.Fatal("新视图继承旧processed/delivered")
	}
	if r.state.ReadMemory.SourceArtifacts[origin.ArtifactID] != origin.ID {
		t.Fatal("视图覆盖canonical身份")
	}
	if err := validateReadMemoryState(r.state); err != nil {
		t.Fatal(err)
	}
}

func TestSourceViewPagesReconstructUTF8AndKeepOriginalOffsets(t *testing.T) {
	ctx, r, _ := sourceViewFixture(t)
	origin := r.analysisRuntime.sources["E1"]
	var reconstructed []byte
	for offset := 0; ; {
		_, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, offset, 53, nil))
		if err != nil {
			t.Fatal(err)
		}
		if read == nil || !utf8.Valid(raw) || read.SourceView.ArtifactStart != int64(offset) || read.ByteStart != origin.Receipt.ByteStart+int64(offset) {
			t.Fatalf("分页字节或字符损坏 %+v", read)
		}
		reconstructed = append(reconstructed, raw...)
		offset = int(read.SourceView.ArtifactEnd)
		if offset == len(origin.Source) {
			break
		}
		if len(raw) == 0 {
			t.Fatal("分页没有推进")
		}
	}
	if string(reconstructed) != string(origin.Source) {
		t.Fatal("分页没有重建原材料")
	}
	_, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, len(origin.Source)+10, 53, nil))
	if err != nil || read.ByteStart != origin.Receipt.ByteEnd || len(raw) != 0 {
		t.Fatalf("末尾空页错误: %+v %v", read, err)
	}
	_, _, _, err = (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, 5, 53, nil))
	if err == nil {
		t.Fatal("字符内部offset未拒绝")
	}
}

func TestSourceViewPermissionsHistoryAndSameHashInvalidation(t *testing.T) {
	ctx, r, _ := sourceViewFixture(t)
	origin := r.analysisRuntime.sources["E1"]
	child := observe.WithAgent(ctx, "child", "root")
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(child, viewRaw(origin.ArtifactID, 0, 100, nil)); err == nil {
		t.Fatal("子任务读取父来源")
	}
	e := origin
	e.AgentID, e.Receipt.AgentID = "child", "child"
	r.analysisRuntime.sources[e.ID] = e
	r.state.ReadMemory.Evidence[e.ID] = e
	r.analysisRuntime.parents["child"] = "root"
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(e.ArtifactID, 0, 100, nil)); err == nil {
		t.Fatal("未交接的子来源被父接受")
	}
	r.analysisRuntime.delivered["child"] = map[string]bool{e.ID: true}
	if _, read, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(e.ArtifactID, 0, 100, nil)); err != nil || read == nil {
		t.Fatalf("父首次核对子来源失败: %v", err)
	}
	r.mu.Lock()
	r.invalidateMemoryLocked("", "bash_started", true)
	r.mu.Unlock()
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(e.ArtifactID, 0, 100, nil)); err == nil {
		t.Fatal("Bash后相同版本复用了旧来源")
	}
	e.RunID = "old-run"
	r.state.ReadMemory.Evidence[e.ID] = e
	delete(r.analysisRuntime.sources, e.ID)
	out, read, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(e.ArtifactID, 0, 100, nil))
	if err != nil || read != nil || !strings.Contains(out, "历史来源") {
		t.Fatalf("历史材料被升级或不可查看: %v %+v", err, read)
	}
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(child, viewRaw(e.ArtifactID, 0, 100, nil)); err == nil {
		t.Fatal("子任务跨运行读取历史")
	}
}

func TestSourceViewGenericMaterialAndSchemasDoNotGainSourceIdentity(t *testing.T) {
	ctx, r, materials := sourceViewFixture(t)
	materials["generic"] = []byte("文件: 假源码\n版本: 假版本\n")
	out, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw("generic", 0, 100, nil))
	if err != nil || read != nil || raw != nil || !strings.Contains(out, "假源码") {
		t.Fatalf("generic语义改变或升级: %v %+v", err, read)
	}
	registry := NewWorkspaceRegistry(tools.ScopeFromContext(ctx).WorkDir)
	found := false
	for _, def := range r.visibleTools(ctx, registry) {
		if def.Name == "read_evidence" {
			props := def.InputSchema.(map[string]any)["properties"].(map[string]any)
			found = props["_work_notes"] != nil && props["review"] != nil
		}
	}
	if !found {
		t.Fatal("来源视图复核或笔记入口不可见")
	}
	original := (evidenceTool{}).Definition().InputSchema.(map[string]any)["properties"].(map[string]any)
	if original["_work_notes"] != nil || original["review"] != nil {
		t.Fatal("投影污染原Schema")
	}
}

func TestSourceViewEngineFoldsOnlyNewProcessedViewAndRejectsRepeat(t *testing.T) {
	dir := t.TempDir()
	body := "视图独有正文不可重复保留\nscore默认5，V2可选字段\n"
	writeFixture(t, dir, "source.md", body)
	writeFixture(t, dir, "other.md", "另一份材料\n")
	calls := 0
	var original, view AnalysisEvidence
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("source", "source.md"))
		case 2:
			original = analysisIntegrationEvidence(ctx)[0]
			return modelCalls(schema.ToolCall{ID: "repeat", Name: "read_evidence", Arguments: viewRaw(original.ArtifactID, 0, 4000, nil)})
		case 3:
			if !strings.Contains(messages[len(messages)-1].Content, "duplicate_read") {
				t.Fatal("通过材料入口绕过重复读取")
			}
			notes := map[string]any{"schema_version": 1, "processed_reads": []string{original.ID}, "summaries": map[string]string{original.ID: "score为可选字段，缺失默认5；V2范围"}}
			args := map[string]any{"id": original.ArtifactID, "review": &schema.ReadReview{TargetID: original.ID, Question: "核对score默认值与V2条件"}, "_work_notes": notes}
			raw, _ := json.Marshal(args)
			return modelCalls(schema.ToolCall{ID: "review", Name: "read_evidence", Arguments: raw})
		case 4:
			entries := analysisIntegrationEvidence(ctx)
			if len(entries) != 2 {
				t.Fatalf("来源视图未产生新E: %+v", entries)
			}
			view = entries[1]
			if view.Receipt.SourceView == nil || view.ID == original.ID || view.AgentID != original.AgentID {
				t.Fatalf("错误视图身份 %+v", view)
			}
			if containsString(currentRun(ctx).state.ReadMemory.ProcessedReads[view.AgentID], view.ID) {
				t.Fatal("视图继承原E处理资格")
			}
			args := map[string]any{"path": "other.md", "_work_notes": map[string]any{"schema_version": 1, "processed_reads": []string{view.ID}, "summaries": map[string]string{view.ID: "复核score默认5，仅适用于V2可选字段"}}}
			raw, _ := json.Marshal(args)
			return modelCalls(schema.ToolCall{ID: "other", Name: "read_file", Arguments: raw})
		default:
			for _, m := range messages {
				if m.ToolCallID == "review" && (m.RequestRepresentation != "folded_marker" || strings.Contains(m.Content, "视图独有正文")) {
					t.Fatalf("已处理视图正文未实际折叠: %+v", m)
				}
			}
			return reply("正常自由交付")
		}
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("source-view", dir), TaskSpec{Goal: "读取并核对", ReadContextVersion: 3}, nil)
	if err != nil || result.Status != NeedsVerification || calls != 5 || result.Usage.ToolCalls != 4 {
		t.Fatalf("额外调用或执行失败: %+v %v calls=%d", result, err, calls)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	if len(state.ReadMemory.Evidence) != 3 || state.ReadMemory.SourceArtifacts[original.ArtifactID] != original.ID {
		t.Fatalf("持久映射无效 %+v", state.ReadMemory)
	}
	files, err := os.ReadDir(filepath.Join(dir, ".claw", "tasks", result.TaskID, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("视图重复保存原文或正文落盘：%d", len(files))
	}
}

func TestSourceViewUncommittedOriginAndCachedForgedSliceAreRejected(t *testing.T) {
	ctx, r, _ := sourceViewFixture(t)
	origin := r.analysisRuntime.sources["E1"]
	delete(r.state.ReadMemory.SourceArtifacts, origin.ArtifactID)
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, 0, 100, nil)); err == nil {
		t.Fatal("未提交canonical身份获得来源资格")
	}
	delete(r.state.ReadMemory.Evidence, origin.ID)
	if _, ok := r.sourceOrigin(origin.ArtifactID); ok {
		t.Fatal("孤立runtime/artifact获得原始来源身份")
	}
	r.state.ReadMemory.Evidence[origin.ID] = origin
	r.state.ReadMemory.SourceArtifacts[origin.ArtifactID] = origin.ID
	out, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(origin.ArtifactID, 3, 100, nil))
	if err != nil {
		t.Fatal(err)
	}
	result := schema.NewToolResult("v", out, schema.ToolSucceeded, schema.EffectNone, "", nil)
	result.Read, result.ReadContent = read, raw
	bindRead(ctx, "view-action", &result)
	if err := r.captureAnalysisRead(ctx, "view-action", &result); err != nil {
		t.Fatal(err)
	}
	view := r.analysisRuntime.sources[result.Read.EvidenceID]
	forged := *view.Receipt.SourceView
	forged.ArtifactStart++
	view.Receipt.SourceView = &forged
	if _, err := r.readEvidenceContent(view); err == nil {
		t.Fatal("缓存Source绕过了切片身份校验")
	}
}

func TestSourceViewLocalCheckArtifactRequiresAllSourcesAndConfirmedReceipt(t *testing.T) {
	ctx, r, materials := sourceViewFixture(t)
	materials["checks"] = []byte(`{"checks":[{"public":true},{"private":true}]}`)
	r.state.LocalChecks = []LocalCheckSummary{{ID: "L1", OriginRunID: r.runID, AgentID: "root", ReceiptArtifactID: "checks", Result: "pass", Sources: []LocalCheckSource{{Path: "E1.md"}}}, {ID: "L2", OriginRunID: r.runID, AgentID: "root", ReceiptArtifactID: "checks", Result: "pass", Sources: []LocalCheckSource{{Path: "E2.md"}}}}
	scope := tools.ScopeFromContext(ctx)
	scope.AllowedPaths = []string{"E1.md"}
	restricted := tools.WithScope(ctx, scope)
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(restricted, viewRaw("checks", 0, 100, nil)); err == nil {
		t.Fatal("同材料后续核对主体绕过路径权限")
	}
	r.state.LocalChecks[1].Reason = "receipt_not_confirmed"
	if _, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw("checks", 0, 100, nil)); err == nil {
		t.Fatal("未确认核对回执被读取")
	}
	r.state.LocalChecks[1].Reason = ""
	if _, read, _, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw("checks", 0, 100, nil)); err != nil || read != nil {
		t.Fatalf("核对generic材料不可查看或被升级来源: %+v %v", read, err)
	}
}

func TestSourceViewNonzeroOriginalRangeAndCitationCannotEscapeSlice(t *testing.T) {
	ctx, r, materials := sourceViewFixture(t)
	e := r.analysisRuntime.sources["E1"]
	// 原始读取从文件中间开始；材料offset零不等于文件offset零。
	begin := len("E1 决定性事实\n")
	e.Source = e.Source[begin:]
	e.Receipt.ByteStart = int64(begin)
	e.Receipt.FirstLine = 2
	e.Receipt.LastLine = 2
	r.analysisRuntime.sources[e.ID] = e
	r.state.ReadMemory.Evidence[e.ID] = e
	materials[e.ArtifactID] = e.Source
	out, read, raw, err := (evidenceTool{}).ExecuteReadSource(ctx, viewRaw(e.ArtifactID, 0, 50, nil))
	if err != nil {
		t.Fatal(err)
	}
	if read.ByteStart != int64(begin) || read.FirstLine != 2 || read.ByteEnd != int64(begin+len(raw)) {
		t.Fatalf("材料/文件offset映射不符 %+v", read)
	}
	result := schema.NewToolResult("v", out, schema.ToolSucceeded, schema.EffectNone, "", nil)
	result.Read, result.ReadContent = read, raw
	bindRead(ctx, "a-view", &result)
	if err := r.captureAnalysisRead(ctx, "a-view", &result); err != nil {
		t.Fatal(err)
	}
	view := r.analysisRuntime.sources[result.Read.EvidenceID]
	if _, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: view.ID, StartLine: 2, EndLine: 2, Quote: string(e.Source[:100])}, view); err == nil {
		t.Fatal("部分视图支持了未交付的尾部引文")
	}
	if _, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: view.ID, StartLine: 1, EndLine: 1, Quote: "决定性事实"}, view); err == nil {
		t.Fatal("部分视图支持了原材料前方引文")
	}
}
