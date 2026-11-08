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

func TestNotesResponseCountsOneOpportunityForToolBatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		writeFixture(t, dir, name, name+" 的适用条件和来源事实\n")
	}
	calls := 0
	model := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("a", "a.md"), readCall("b", "b.md"))
		case 2:
			report := AnalysisReport{SchemaVersion: 1, Summaries: map[string]string{}}
			for _, entry := range analysisIntegrationEvidence(ctx) {
				report.ProcessedReads = append(report.ProcessedReads, entry.ID)
				report.Summaries[entry.ID] = string(entry.Source)
			}
			raw, _ := json.Marshal(map[string]any{"path": "c.md", "_work_notes": report})
			return modelCalls(schema.ToolCall{ID: "c", Name: "read_file", Arguments: raw}, readCall("d", "d.md"))
		case 3:
			return reply("已完成阅读。")
		default:
			return nil, fmt.Errorf("不应增加补笔记调用")
		}
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 3, MaxToolCalls: 5}})
	result, events := observedRun(t, eng, ctxpkg.NewSession("笔记响应指标", dir), TaskSpec{Goal: "读取四份资料并回答", Kind: TaskAnswer, ReadContextVersion: 2})
	if result.Status != Answered || result.Usage.ModelCalls != 3 || result.Usage.ToolCalls != 4 {
		t.Fatalf("元数据改变预算或任务结果: %+v", result)
	}
	responses, opportunities, accepted := 0, 0, 0
	ids := map[string]bool{}
	for _, event := range events {
		if event.EventType != "work_notes_response" {
			continue
		}
		responses++
		if event.ModelCallID == "" || ids[event.ModelCallID] {
			t.Fatalf("响应身份缺失或重复: %+v", event)
		}
		ids[event.ModelCallID] = true
		if event.Payload.NotesOpportunity {
			opportunities++
			if event.Payload.NotesCandidates != 2 || event.Payload.NotesSubmitted != 1 || event.Payload.NotesProcessedSources != 2 || event.Payload.ErrorClass != "accepted" {
				t.Fatalf("单批多工具的采纳口径错误: %+v", event)
			}
		}
		if event.Payload.NotesResponseAccepted {
			accepted++
		}
	}
	if responses != 3 || opportunities != 1 || accepted != 1 {
		t.Fatalf("响应计数错误: responses=%d opportunities=%d accepted=%d", responses, opportunities, accepted)
	}
	if events[eventIndex(events, "model_started")].Payload.NotesThisRequest != 0 {
		t.Fatal("首次读取前凭空产生来源候选")
	}
}
