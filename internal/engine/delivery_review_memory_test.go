package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func deliveryMemoryFixture(t *testing.T) (context.Context, *runState) {
	ctx, r := carrierFixture(t)
	r.state.Spec.DeliveryReview = &DeliveryReviewSpec{Version: 1}
	r.state.Spec.Mode = "workspace"
	r.state.Spec.Kind = TaskChange
	r.state.Version = 3
	r.state.DeliveryReview = &DeliveryReviewState{Version: 1, Status: "not_run"}
	return ctx, r
}
func deliveryNote(t *testing.T, r *runState, ctx context.Context, notes any) workNotesCarrierBatch {
	t.Helper()
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("delivery-note", notes)})
	if err != nil {
		t.Fatal(err)
	}
	return batch
}
func deliveryReviewUpdate(ids []string, kind string) findingsReviewUpdate {
	return findingsReviewUpdate{SchemaVersion: 1, SourceReviews: []SourceReviewInput{{EvidenceIDs: ids, Disposition: kind, Reason: "已检查与当前任务的适用范围"}}}
}

type findingsReviewUpdate struct {
	SchemaVersion  int                 `json:"schema_version"`
	Findings       []AnalysisFinding   `json:"findings,omitempty"`
	ProcessedReads []string            `json:"processed_reads,omitempty"`
	DeferredReads  []DeferredRead      `json:"deferred_reads,omitempty"`
	SourceReviews  []SourceReviewInput `json:"source_reviews,omitempty"`
}

func TestDeliveryMemoryCatalogDoesNotConfuseReadsAndDocuments(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	alias := r.analysisRuntime.sources["E1"]
	alias.ID = "E3"
	alias.Receipt.EvidenceID = "E3"
	r.analysisRuntime.sources["E3"] = alias
	r.state.ReadMemory.Evidence["E3"] = alias
	r.markAnalysisDelivered(ctx, []string{"E3"})
	note := deliveryReviewUpdate([]string{"E1", "E3"}, "not_relevant")
	b := deliveryNote(t, r, ctx, note)
	if !b.valid || !strings.Contains(b.receipts[0], `"source_reviews_accepted":1`) {
		t.Fatalf("处置未接受: %+v", b)
	}
	s := r.deliveryReviewSummary(ctx)
	if s.Documents != 2 || s.NotRelevant != 1 || s.Undeclared != 1 || s.Addressed != 0 {
		t.Fatalf("读取数冒充文档处置: %+v", s)
	}
	if len(r.state.ReadMemory.ProcessedReads["root"]) != 0 {
		t.Fatal("不相关声明授予处理资格")
	}
	if err := validateDeliveryReviewMemory(r.state); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryMemoryAddressedNeedsCurrentCitedFindingAndDoesNotProcess(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	bad := deliveryReviewUpdate([]string{"E1"}, "addressed")
	bad.SourceReviews[0].FindingIDs = []string{"F1"}
	if b := deliveryNote(t, r, ctx, bad); b.valid {
		t.Fatal("不存在的发现取得addressed")
	}
	good := bad
	good.Findings = []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "材料记录决定性事实", Context: "当前资料版本", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}}
	if b := deliveryNote(t, r, ctx, good); !b.valid {
		t.Fatalf("有效来源发现未被接受: %+v", b)
	}
	s := r.deliveryReviewSummary(ctx)
	if s.Addressed != 1 || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 {
		t.Fatalf("addressed错误授予处理: %+v", s)
	}
	// 同ID改变不能继承此前文档处置票据。
	report := r.state.ReadMemory.Notes["root"]
	report.Findings[0].Statement = "另一个尚未复查的判断"
	r.state.ReadMemory.Notes["root"] = report
	if s = r.deliveryReviewSummary(ctx); s.Addressed != 0 || s.Undeclared != 2 {
		t.Fatalf("改变的发现借用旧处置: %+v", s)
	}
}

func TestDeliveryMemoryRejectsMixedForeignFutureAndHistoricSources(t *testing.T) {
	for _, mode := range []string{"mixed_path", "future", "foreign", "historic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, r := deliveryMemoryFixture(t)
			ids := []string{"E1"}
			switch mode {
			case "mixed_path":
				ids = append(ids, "E2")
			case "future":
				delete(r.analysisRuntime.delivered["root"], "E1")
			case "foreign":
				e := r.analysisRuntime.sources["E1"]
				e.AgentID = "child"
				r.analysisRuntime.sources["E1"] = e
			case "historic":
				e := r.analysisRuntime.sources["E1"]
				e.RunID = "old"
				r.analysisRuntime.sources["E1"] = e
			}
			if b := deliveryNote(t, r, ctx, deliveryReviewUpdate(ids, "not_relevant")); b.valid || len(r.state.ReadMemory.SourceReviews) != 0 {
				t.Fatalf("越界来源被处置: %+v", b)
			}
		})
	}
}

func TestDeliveryMemoryWaitForBecomesReadyOnlyAfterActualDelivery(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	whole := r.analysisRuntime.sources["E1"]
	split := int64(strings.IndexByte(string(whole.Source), '\n') + 1)
	first := whole
	first.Source = append([]byte(nil), whole.Source[:split]...)
	first.Receipt.ByteEnd = split
	first.Receipt.LastLine = 1
	r.analysisRuntime.sources["E1"] = first
	r.state.ReadMemory.Evidence["E1"] = first
	tail := whole
	tail.ID = "E3"
	tail.Receipt.EvidenceID = "E3"
	tail.Receipt.ByteStart = split
	tail.Receipt.FirstLine = 2
	tail.Source = append([]byte(nil), whole.Source[split:]...)
	r.analysisRuntime.sources["E3"] = tail
	r.state.ReadMemory.Evidence["E3"] = tail
	n := findingsReviewUpdate{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "仍需后半材料", WaitFor: &DeferredReadRange{ByteStart: split, ByteEnd: whole.Receipt.TotalBytes}}}}
	if b := deliveryNote(t, r, ctx, n); !b.valid {
		t.Fatalf("有效等待范围被拒: %+v", b)
	}
	if s := r.deliveryReviewSummary(ctx); s.ReadyToResolve != 0 {
		t.Fatal("未成功交付提前ready")
	}
	r.markAnalysisDelivered(ctx, []string{"E3"})
	s := r.deliveryReviewSummary(ctx)
	if s.ReadyToResolve != 1 || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 || len(r.state.ReadMemory.Notes["root"].DeferredReads) != 1 {
		t.Fatalf("ready错误改变处理资格: %+v", s)
	}
	if b := deliveryNote(t, r, ctx, deliveryReviewUpdate([]string{"E1"}, "not_relevant")); !b.valid {
		t.Fatal("不相关声明不能保存")
	}
	if r.deliveryReviewSummary(ctx).ReadyToResolve != 1 {
		t.Fatal("不相关声明误清独立暂缓")
	}
	r.mu.Lock()
	r.invalidateMemoryLocked("", "bash_executed", true)
	r.mu.Unlock()
	if s = r.deliveryReviewSummary(ctx); s.ReadyToResolve != 0 || s.Documents != 0 || len(s.Unresolved) == 0 {
		t.Fatalf("失效资格仍被使用: %+v", s)
	}
}

func TestDeliveryMemoryWaitForRangeAndModeBoundaries(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	total := r.analysisRuntime.sources["E1"].Receipt.TotalBytes
	for _, span := range []DeferredReadRange{{ByteStart: -1, ByteEnd: 4}, {ByteStart: 0, ByteEnd: total + 1}, {ByteStart: 4, ByteEnd: 4}} {
		b := deliveryNote(t, r, ctx, findingsReviewUpdate{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "等待材料", WaitFor: &span}}})
		if b.valid {
			t.Fatalf("非法等待范围被接受: %+v", span)
		}
	}
	for _, raw := range []string{`{"byte_end":3}`, `{"byte_start":null,"byte_end":3}`, `{"byte_start":0,"byte_end":3,"path":"other"}`} {
		var span DeferredReadRange
		if json.Unmarshal([]byte(raw), &span) == nil {
			t.Fatal("wait_for缺字段或未知字段被接受")
		}
	}
	r.state.Spec.DeliveryReview = nil
	b := deliveryNote(t, r, ctx, map[string]any{"schema_version": 1, "summaries": map[string]string{"E1": "旧任务合法摘要"}, "deferred_reads": []any{map[string]any{"evidence_id": "E1", "reason": "仍待处理", "wait_for": "旧任务原本未知字段"}}, "source_reviews": []any{map[string]any{"not": "a new feature"}}})
	if !b.valid || len(r.state.ReadMemory.SourceReviews) != 0 || r.state.ReadMemory.Notes["root"].DeferredReads[0].WaitFor != nil || r.deliveryReviewPrompt(ctx) != "" || r.deliveryReviewSummary(ctx) != nil {
		t.Fatal("默认关闭改变旧载体语义")
	}
}

func TestDeliveryMemoryPromptIsBoundedWholeJSONAndHistoryDoesNotQualify(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	def := r.withWorkNotesCarrier(schema.ToolDefinition{Name: "read_file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}})
	if !strings.Contains(fmtSchema(def), "source_reviews") || !strings.Contains(fmtSchema(def), "wait_for") {
		t.Fatal("扩展Schema未开启")
	}
	r.state.DeliveryReview = &DeliveryReviewState{Version: 1, Status: "completed", AdmissionCount: 1, OriginRunID: "old", Summary: &DeliveryReviewSummary{Documents: 15, Addressed: 15, Unresolved: []DeliveryReviewIssue{{ID: "old_issue", Kind: "history_source_unverified", Reason: "历史未解决项"}}}}
	s := r.deliveryReviewSummary(ctx)
	if s.Documents != 2 || s.Addressed != 0 {
		t.Fatalf("历史处置借用当前资格: %+v", s)
	}
	found := false
	for _, issue := range s.Unresolved {
		if issue.Kind == "history_evidence_required" {
			found = true
		}
		if strings.Contains(issue.Kind, "history_history") {
			t.Fatal("重复历史前缀")
		}
	}
	if !found {
		t.Fatal("历史票据无重新取证提示")
	}
	for i := 0; i < 80; i++ {
		r.state.DeliveryReview.Issues = append(r.state.DeliveryReview.Issues, DeliveryReviewIssue{ID: strings.Repeat("id", 50) + string(rune('A'+i)), Kind: "unverified", Reason: strings.Repeat("未解决事项", 100)})
	}
	prompt := r.deliveryReviewPrompt(ctx)
	if len(prompt) > 4096 || !strings.Contains(prompt, `"omitted":`) || !strings.Contains(prompt, "唯一交付前处理机会") {
		t.Fatalf("交付目录未有界: %d", len(prompt))
	}
	start := strings.Index(prompt, "：") + len("：")
	end := strings.Index(prompt, "\n在既有适用工具")
	if !json.Valid([]byte(prompt[start:end])) {
		t.Fatal("目录JSON被截断")
	}
	r.runID = "resumed-run"
	s = r.deliveryReviewSummary(ctx)
	if s.Documents != 0 || s.Addressed != 0 || len(s.Unresolved) == 0 {
		t.Fatalf("恢复空目录被误报已处理，或继承旧资格: %+v", s)
	}
}
func fmtSchema(def schema.ToolDefinition) string {
	raw, _ := json.Marshal(def.InputSchema)
	return string(raw)
}

func TestDeliveryMemorySourceReviewPersistenceFailureIsStorageError(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	r.persist = func(string, taskState) error { return errors.New("disk failure") }
	_, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("source", deliveryReviewUpdate([]string{"E1"}, "not_relevant"))})
	if err == nil || r.storageError() == nil {
		t.Fatal("目录存储失败被软吞掉")
	}
}

func TestDeliveryMemoryResolvedDeclarationsRemoveCurrentTriggerSnapshot(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	r.state.DeliveryReview = &DeliveryReviewState{Version: 1, Status: "admitted", OriginRunID: r.runID, AdmissionCount: 1, Issues: []DeliveryReviewIssue{{ID: "old_trigger", Kind: "source_undeclared", Reason: "准入当时尚未处置的快照"}}}
	b := deliveryNote(t, r, ctx, findingsReviewUpdate{SchemaVersion: 1, SourceReviews: []SourceReviewInput{{EvidenceIDs: []string{"E1"}, Disposition: "not_relevant", Reason: "与目标无关"}, {EvidenceIDs: []string{"E2"}, Disposition: "not_relevant", Reason: "与目标无关"}}})
	if !b.valid {
		t.Fatal("正常声明失败")
	}
	if s := r.deliveryReviewSummary(ctx); len(s.Unresolved) != 1 || s.Unresolved[0].Kind != "no_valid_findings" || s.NotRelevant != 2 {
		t.Fatalf("当前已解决项被旧准入快照复活: %+v", s)
	}
	deferred := AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "材料虽齐仍需处理", WaitFor: &DeferredReadRange{ByteStart: 0, ByteEnd: r.analysisRuntime.sources["E1"].Receipt.TotalBytes}}}}
	if !deliveryNote(t, r, ctx, deferred).valid || r.deliveryReviewSummary(ctx).ReadyToResolve != 1 {
		t.Fatal("独立deferred未保留")
	}
	update := carrierSummary("E1", "已依据原文保留限定范围，当前资料与交付主题无关")
	if !deliveryNote(t, r, ctx, update).valid {
		t.Fatal("既有有效处理声明失败")
	}
	if s := r.deliveryReviewSummary(ctx); len(s.Unresolved) != 1 || s.Unresolved[0].Kind != "no_valid_findings" || s.ReadyToResolve != 0 {
		t.Fatalf("处理声明未清除当前暂缓，或快照复活: %+v", s)
	}
}

func TestDeliveryMemoryAdoptedCounterexampleDoesNotChangeCheckResult(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	e := r.analysisRuntime.sources["E1"]
	r.state.LocalChecks = []LocalCheckSummary{{ID: "C1", OriginRunID: r.runID, AgentID: "root", Result: "fail", PremiseResult: "not_run", Sources: []LocalCheckSource{{Path: e.Receipt.Path, Version: e.Receipt.Version, EvidenceIDs: []string{"E1"}}}}}
	before := r.deliveryReviewSummary(ctx)
	hasCheck := false
	for _, i := range before.Unresolved {
		hasCheck = hasCheck || i.Kind == "local_check_unresolved"
	}
	if !hasCheck {
		t.Fatal("未采用反例未呈现")
	}
	update := deliveryReviewUpdate([]string{"E1"}, "addressed")
	update.SourceReviews[0].FindingIDs = []string{"F1"}
	update.SourceReviews[0].CheckIDs = []string{"C1"}
	update.Findings = []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "检查得到反例，保留前提未核实边界", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}}
	if !deliveryNote(t, r, ctx, update).valid {
		t.Fatal("关联反例的发现未接受")
	}
	for _, issue := range r.deliveryReviewSummary(ctx).Unresolved {
		if issue.Kind == "local_check_unresolved" {
			t.Fatal("已声明采用观察仍被称未采用")
		}
	}
	if r.state.LocalChecks[0].Result != "fail" || r.state.LocalChecks[0].PremiseResult != "not_run" {
		t.Fatal("采用观察错误升级检查或前提通过")
	}
	// 孤立的核对身份不能让没有发现的addressed成功。
	bad := deliveryReviewUpdate([]string{"E2"}, "addressed")
	bad.SourceReviews[0].CheckIDs = []string{"C1"}
	if deliveryNote(t, r, ctx, bad).valid {
		t.Fatal("孤立check_id清除了文档未解决项")
	}
}
