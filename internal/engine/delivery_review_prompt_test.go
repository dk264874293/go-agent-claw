package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestDeliveryReviewProcessedReadsStillAllowDispositionNotes(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	r.state.ReadMemory.ProcessedReads["root"] = []string{"E1", "E2"}
	r.state.ReadMemory.Summaries["root"] = map[string]string{"E1": "原文一的事实和适用范围", "E2": "原文二的事实和适用范围"}
	before, _ := json.Marshal(r.state)
	if summary := r.deliveryReviewSummary(ctx); summary.Undeclared != 2 {
		t.Fatalf("场景应仍有两份未处置来源：%+v", summary)
	}
	request := []schema.Message{{Role: schema.RoleSystem, Content: r.deliveryReviewPrompt(ctx)}, {Role: schema.RoleUser, Content: "汇总已有材料"}}
	prepared, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.unprocessed != 0 {
		t.Fatalf("已处理读取不应重新成为读取候选：%+v", stats)
	}
	prompt := prepared[0].Content
	for _, forbidden := range []string{"没有候选时省略载体", "本轮没有可记的读取候选，请省略_work_notes"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("仍有文档处置待办却禁止唯一笔记载体：%s", forbidden)
		}
	}
	for _, want := range []string{"source_reviews", "没有新的读取处理候选", "若交付前目录仍有待办", "没有有效增量时省略载体"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("提示缺少读取与处置的独立边界：%s", want)
		}
	}
	after, _ := json.Marshal(r.state)
	if string(before) != string(after) {
		t.Fatal("提示投影改变了持久状态或预算")
	}
	// 旧读取已处理后，仍可在原有业务调用上增量登记有来源的发现和处置。
	update := deliveryReviewUpdate([]string{"E1"}, "addressed")
	update.SourceReviews[0].FindingIDs = []string{"F1"}
	update.Findings = []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "材料一注明决定性事实", Context: "仅当前源版本", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}}
	if batch := deliveryNote(t, r, ctx, update); !batch.valid {
		t.Fatalf("已处理证据的有效处置增量被拒绝：%+v", batch)
	}
	if summary := r.deliveryReviewSummary(ctx); summary.Addressed != 1 || summary.Undeclared != 1 {
		t.Fatalf("处置未按文档更新：%+v", summary)
	}
}

func TestDeliveryReviewDisabledKeepsNoCandidatePrompt(t *testing.T) {
	ctx, r := carrierFixture(t)
	r.state.ReadMemory.ProcessedReads["root"] = []string{"E1", "E2"}
	request := []schema.Message{{Role: schema.RoleSystem, Content: "任务约束"}, {Role: schema.RoleUser, Content: "普通任务"}}
	prepared, _, _, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := prepared[0].Content
	if !strings.Contains(prompt, workNotesCarrierPrompt) || !strings.Contains(prompt, "本轮没有可记的读取候选，请省略_work_notes") || strings.Contains(prompt, "source_reviews") {
		t.Fatal("默认关闭任务的提示发生变化或泄露新处置契约")
	}
}
