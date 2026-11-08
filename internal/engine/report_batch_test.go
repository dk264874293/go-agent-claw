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

func TestReportIllegalBatchKeepsDraftAndRejectedPairs(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "完整拒绝", true: "拒绝也遵守工具预算"}[limited], func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "原始资料\n")
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return modelCalls(schema.ToolCall{ID: "report", Name: "write_report", Arguments: json.RawMessage(`{"schema_version":1,"findings":[]}`)}, readCall("read", "source.md"))
			})
			budget := Budget{MaxModelCalls: 1, MaxTurns: 1, MaxToolCalls: 2}
			if limited {
				budget.MaxToolCalls = 1
			}
			session := ctxpkg.NewSession("illegal-batch", dir)
			result, err := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: budget}).Run(context.Background(), session, TaskSpec{Goal: "生成报告", Report: &ReportSpec{TargetPath: "result.md"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !limited && (result.Status != Blocked || result.StopCode != "report_invalid" || result.Usage.ToolCalls != 2) {
				t.Fatalf("非法批次计数或终态错误: %+v", result)
			}
			if limited && (result.Status != BudgetExhausted || result.StopCode != "tool_limit" || result.Usage.ToolCalls != 1) {
				t.Fatalf("非法批次突破预算: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(dir, "result.md")); !os.IsNotExist(err) {
				t.Fatal("非法批次执行了写入")
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			if state.Report == nil || state.Report.DraftArtifactID == "" || state.Report.Status != "invalid" {
				t.Fatal("非法提交丢失可定位草稿")
			}
			pairs := map[string]int{}
			for _, m := range session.GetWorkingMemory(0) {
				if m.ToolCallID != "" {
					pairs[m.ToolCallID]++
					if m.ToolStatus != schema.ToolRejected {
						t.Fatal("非法批次误报成功")
					}
				}
			}
			if pairs["read"] != 1 || pairs["report"] != 1 {
				t.Fatalf("工具配对缺失: %+v", pairs)
			}
		})
	}
}
