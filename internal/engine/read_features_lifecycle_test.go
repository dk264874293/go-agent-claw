package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadFeaturesV2SourceLifecycleUsesSuccessfulRequestIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "标题\n字段仅在指定条件下存在。\n")
	writeFixture(t, dir, "b.md", "新的独立读取批次。\n")
	calls := 0
	viewID := ""
	model := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read-a", "a.md"))
		case 2:
			var original AnalysisEvidence
			for _, e := range analysisIntegrationEvidence(ctx) {
				if e.Receipt.Path == "a.md" && e.Receipt.SourceView == nil {
					original = e
				}
			}
			if original.ID == "" {
				return nil, fmt.Errorf("缺少实际原始读取")
			}
			raw, _ := json.Marshal(map[string]any{"evidence_id": original.ID, "offset": 0, "limit": 4000, "review": schema.ReadReview{TargetID: original.ID, Question: "核对字段的实际条件"}})
			return modelCalls(schema.ToolCall{ID: "view-a", Name: "read_evidence", Arguments: raw})
		case 3:
			var view AnalysisEvidence
			for _, e := range analysisIntegrationEvidence(ctx) {
				if e.Receipt.SourceView != nil {
					view = e
				}
			}
			if view.ID == "" {
				return nil, fmt.Errorf("typed来源视图没有实际执行")
			}
			viewID = view.ID
			raw, _ := json.Marshal(map[string]any{"path": "b.md", "_work_notes": carrierSummary(view.ID, "已核对这一片段："+string(view.Source))})
			return modelCalls(schema.ToolCall{ID: "read-b", Name: "read_file", Arguments: raw})
		case 4:
			return reply("阅读完成，结论仅限静态来源。")
		}
		return nil, fmt.Errorf("不应增加模型阶段")
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 4, MaxToolCalls: 4}})
	result, events := observedRun(t, eng, ctxpkg.NewSession("视图生命周期", dir), TaskSpec{Goal: "阅读两份材料", Kind: TaskAnswer, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true}})
	if result.Status != Answered || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 3 {
		t.Fatalf("观察改变了任务结果/调用数: %+v", result)
	}
	seen := map[string]int{}
	for _, ev := range events {
		switch ev.EventType {
		case "source_view_created", "source_view_delivered", "source_view_processed", "source_view_folded":
			if ev.Payload.ViewEvidenceID != viewID || ev.ModelCallID == "" || ev.Payload.ContentBytes <= 0 {
				t.Fatalf("生命周期身份或范围丢失: %+v", ev)
			}
			seen[ev.EventType]++
		}
	}
	for _, kind := range []string{"source_view_created", "source_view_delivered", "source_view_processed", "source_view_folded"} {
		if seen[kind] == 0 {
			t.Fatalf("缺少%s，实收%v", kind, seen)
		}
	}
}
