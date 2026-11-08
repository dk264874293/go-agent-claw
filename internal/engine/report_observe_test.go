package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReportRejectedObservationMatchesCommittedAction(t *testing.T) {
	for _, variant := range []string{"bad_quote", "bad_json"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte("V2 真实材料\n"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}}}, nil
				}
				raw := json.RawMessage(`{"findings":`)
				if variant == "bad_quote" {
					var report AnalysisReport
					if err := json.Unmarshal([]byte(analysisIntegrationJSON(analysisIntegrationEvidence(ctx))), &report); err != nil {
						t.Fatal(err)
					}
					report.Findings[0].Evidence[0].Quote = "并不存在的材料"
					raw, _ = json.Marshal(report)
				}
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "submit", Name: "write_report", Arguments: raw}}}, nil
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
			result, events := observedRun(t, eng, ctxpkg.NewSession("报告拒绝观测", dir), reportTestSpec())
			if result.Status != Blocked || result.StopCode != "report_invalid" || calls != 2 {
				t.Fatalf("拒绝终态错误：%+v calls=%d", result, calls)
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			if state.LastAction == nil || state.LastAction.Tool != "write_report" {
				t.Fatal("报告拒绝未记录动作")
			}
			action := state.LastAction
			if action.Result.Status != schema.ToolRejected || action.Result.Effect != schema.EffectNone || action.Result.ErrorCode != "report_invalid" {
				t.Fatalf("动作拒绝结果错误：%+v", action)
			}
			matched := 0
			for _, event := range events {
				if event.EventType != "tool_finished" || event.Payload.Tool != "write_report" {
					continue
				}
				matched++
				if event.ActionID != action.ID || event.Payload.Status != string(action.Result.Status) || event.Payload.Effect != string(action.Result.Effect) || event.Payload.ErrorClass != "报告校验拒绝" {
					t.Fatalf("观测与账本不一致：%+v action=%+v", event, action)
				}
			}
			if matched != 1 || len(result.Delivery.Effects) != 0 {
				t.Fatalf("拒绝被记为文件未知效果：matched=%d delivery=%+v", matched, result.Delivery)
			}
		})
	}
}
