package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDeclaredReadingGatePreservesWritesAndBudgetPriority(t *testing.T) {
	for _, variant := range []string{"fragment", "required_gap", "required_full", "budget"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "0123456789abcdef\n")
			calls := 0
			p := reportModel(func(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				switch calls {
				case 1:
					limit := 8
					if variant == "required_full" {
						limit = 8000
					}
					a, _ := json.Marshal(map[string]any{"path": "source.md", "limit": limit})
					return modelCalls(schema.ToolCall{ID: "read", Name: "read_file", Arguments: a})
				case 2:
					return modelCalls(schema.ToolCall{ID: "write", Name: "write_file", Arguments: json.RawMessage(`{"path":"out.md","content":"部分产物","expected_version":"missing"}`)})
				default:
					return reply("已交付报告。")
				}
			})
			spec := TaskSpec{Goal: "整理资料", Mode: tools.ModeWorkspace, Kind: TaskChange}
			if variant != "fragment" {
				spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"source.md"}, NameGlob: "*", RequireComplete: true}
			}
			budget := Budget{MaxModelCalls: 3, MaxToolCalls: 4}
			if variant == "budget" {
				budget.MaxModelCalls = 2
			}
			result, err := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: budget}).Run(context.Background(), ctxpkg.NewSession("gate", dir), spec, nil)
			if err != nil || len(result.Artifacts) != 1 || result.Usage.ToolCalls != 2 {
				t.Fatalf("效果丢失或额外执行: %+v err=%v", result, err)
			}
			switch variant {
			case "required_gap":
				if result.Status != Blocked || result.StopCode != "reading_incomplete" || result.FinalText != "" {
					t.Fatalf("缺页未阻断: %+v", result)
				}
			case "budget":
				if result.Status != BudgetExhausted || result.StopCode != "model_limit" || calls != 2 {
					t.Fatalf("预算终态被覆盖: %+v", result)
				}
			default:
				if result.Status != NeedsVerification || result.FinalText == "" {
					t.Fatalf("合法阅读误阻断: %+v", result)
				}
			}
		})
	}
}

func TestReportFullScopeGapKeepsDraftWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V2 使用 review 状态。\n尚未读取尾部。\n")
	calls := 0
	p := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(schema.ToolCall{ID: "read", Name: "read_lines", Arguments: json.RawMessage(`{"path":"source.md","start_line":1,"end_line":1}`)})
		}
		raw := json.RawMessage(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
		return modelCalls(schema.ToolCall{ID: "submit", Name: "write_report", Arguments: raw})
	})
	spec := reportTestSpec()
	spec.ReadScope = &ReadScopeSpec{Paths: []string{"source.md"}, RequireComplete: true}
	res, err := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("gapreport", dir), spec, nil)
	if err != nil || res.Status != Blocked || res.StopCode != "reading_incomplete" || calls != 2 || len(res.Artifacts) != 0 || res.Report == nil || res.Report.DraftArtifactID == "" {
		t.Fatalf("报告缺页交付错误: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
		t.Fatalf("正式报告被写入: %v", err)
	}
}

func TestReportUnquotedScopeSourceChangeMakesRecoveryStale(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V2 使用 review 状态。\n")
	writeFixture(t, dir, "other.md", "未摘录的必读来源。\n")
	calls := 0
	p := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("one", "source.md"), readCall("two", "other.md"))
		}
		if calls > 2 {
			t.Fatal("恢复发布不应调用模型")
		}
		var evidence string
		for id, x := range currentRun(ctx).analysisRuntime.sources {
			if x.Receipt.Path == "source.md" {
				evidence = id
			}
		}
		raw, _ := json.Marshal(AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "V2 使用 review 状态", Context: "本次版本", Evidence: []AnalysisCitation{{EvidenceID: evidence, StartLine: 1, EndLine: 1, Quote: "V2 使用 review 状态。"}}}}})
		return modelCalls(schema.ToolCall{ID: "submit", Name: "write_report", Arguments: raw})
	})
	spec := reportTestSpec()
	spec.ReadScope = &ReadScopeSpec{Paths: []string{"source.md", "other.md"}, RequireComplete: true}
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	sess := ctxpkg.NewSession("scope-stale", dir)
	res, err := eng.Run(context.Background(), sess, spec, nil)
	if err != nil || res.Status != NeedsVerification {
		t.Fatalf("初次发布失败: %+v %v", res, err)
	}
	state := loadAnalysisIntegrationState(t, dir, res.TaskID)
	if len(state.Report.SourceVersions) != 1 || len(state.Report.ScopeDependencies) != 1 || len(state.Report.ScopeDependencies[0].SourceVersions) != 2 {
		t.Fatalf("遗漏完整性来源: %+v", state.Report)
	}
	copyState := state
	copyReport := *state.Report
	copyReport.ScopeDependencies = nil
	copyState.Report = &copyReport
	if validateReportState(copyState) == nil {
		t.Fatal("完整阅读报告删除范围依赖后仍可恢复")
	}
	writeFixture(t, dir, "other.md", "来源版本已经变化。\n")
	res, err = eng.Run(context.Background(), sess, TaskSpec{ID: res.TaskID, Resume: true}, nil)
	if err != nil || res.Status != NeedsVerification || res.StopCode != "report_stale" || calls != 2 || len(res.Artifacts) != 1 {
		t.Fatalf("陈旧发布恢复错误: %+v %v", res, err)
	}
	if !strings.Contains(strings.Join(res.Report.Issues, " "), "来源版本已变化") {
		t.Fatalf("没有范围依赖诊断: %+v", res.Report)
	}
}

func TestAnsweredReadingCacheIsHistoricalAndDoesNotCallModel(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V1\n")
	calls := 0
	p := reportModel(func(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("one", "source.md"))
		}
		return reply("材料为V1。")
	})
	eng := NewAgentEngine(p, NewExplorerRegistry(dir), EngineOptions{})
	sess := ctxpkg.NewSession("cache-scope", dir)
	result, err := eng.Run(context.Background(), sess, TaskSpec{Goal: "查看资料", ReadScope: &ReadScopeSpec{Paths: []string{"source.md"}, RequireComplete: true}}, nil)
	if err != nil || result.Status != Answered {
		t.Fatalf("初次回答失败: %+v %v", result, err)
	}
	writeFixture(t, dir, "source.md", "V2\n")
	result, err = eng.Run(context.Background(), sess, TaskSpec{ID: result.TaskID, Resume: true}, nil)
	if err != nil || calls != 2 || result.Reading == nil || !result.Reading.RecordedOnly || !strings.Contains(result.Reason, "历史") {
		t.Fatalf("历史结果冒充当前覆盖: %+v calls=%d err=%v", result, calls, err)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	if state.LastResult.Reading.RecordedOnly {
		t.Fatal("缓存展示修改了旧持久结果")
	}
}

func TestCancelledReportRefreshPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &runState{}
	if err := r.refreshPublishedReport(ctx); err != context.Canceled {
		t.Fatalf("取消被转为陈旧或正常返回: %v", err)
	}
}
