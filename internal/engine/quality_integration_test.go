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
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func qualitySpec(report bool) TaskSpec {
	if report {
		s := reportTestSpec()
		s.Quality = &QualitySpec{Version: 1}
		return s
	}
	return TaskSpec{Goal: "分析明确公式与来源边界", Mode: tools.ModeReadOnly, Kind: TaskAnswer, Analysis: true, Quality: &QualitySpec{Version: 1}}
}
func qualityCall(name, id string, value any) (*schema.Message, error) {
	raw, _ := json.Marshal(value)
	return modelCalls(schema.ToolCall{ID: id, Name: name, Arguments: raw})
}
func qualityTestFinding(e AnalysisEvidence) QualityFinding {
	return QualityFinding{AnalysisFinding: AnalysisFinding{ID: "F1", Kind: "fact", Statement: strings.TrimSpace(string(e.Source)), Context: "仅本次源片段", Evidence: []AnalysisCitation{{EvidenceID: e.ID, StartLine: e.Receipt.FirstLine, EndLine: e.Receipt.LastLine, Quote: strings.TrimSpace(string(e.Source))}}}}
}
func qualityTestDraft(e AnalysisEvidence) QualityDraft {
	return QualityDraft{SchemaVersion: 1, Findings: []QualityFinding{qualityTestFinding(e)}, OverviewIDs: []string{"F1"}}
}

func TestQualityPlainFactPublicationResumeAndStale(t *testing.T) {
	for _, report := range []bool{false, true} {
		t.Run(fmt.Sprint(report), func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "V2 使用 review 状态。\n")
			calls := 0
			model := reportModel(func(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				switch modelStage(ctx) {
				case "quality_review":
					t.Fatal("逐字材料事实不应复核")
				case "quality_revision":
					if len(msgs) != 2 || strings.Contains(msgs[0].Content, "证据目录") || strings.Contains(msgs[0].Content, "读取处理进度") {
						t.Fatal("专用修订重新注入全量历史")
					}
					r := currentRun(ctx)
					sub := QualitySubmission{DraftID: r.state.Quality.DraftID, Corrections: []QualityCorrection{}}
					if report {
						return qualityCall("write_report", "publish", sub)
					}
					raw, _ := json.Marshal(sub)
					return reply(string(raw))
				}
				if calls == 1 {
					return modelCalls(readCall("read", "source.md"))
				}
				if calls == 2 {
					return qualityCall("verify_findings", "verify", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
				}
				t.Fatal("出现额外模型调用")
				return nil, nil
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("quality", dir), qualitySpec(report), nil)
			want := Answered
			if report {
				want = NeedsVerification
			}
			if err != nil || result.Status != want || result.Quality == nil || result.Quality.Status != "published" || result.Quality.ReviewStatus != "skipped" || result.Quality.RevisionStatus != "completed" || calls != 3 {
				t.Fatalf("结果错误：%+v calls=%d err=%v", result, calls, err)
			}
			if result.Usage.ModelCalls != 3 || result.Usage.Turns != 3 || result.Usage.ToolCalls != 2+boolInt(report) {
				t.Fatalf("准入计数错误：%+v", result.Usage)
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			if state.Version != 2 || ValidateTaskStateVersion(state) != nil {
				t.Fatalf("质量版本丢失：%+v", state.Quality)
			}
			resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("quality", dir), TaskSpec{ID: result.TaskID, Resume: true}, nil)
			if err != nil || resumed.Status != want || calls != 3 {
				t.Fatalf("恢复重复调用：%+v %v", resumed, err)
			}
			writeFixture(t, dir, "source.md", "V3 新版本\n")
			stale, err := eng.Run(context.Background(), ctxpkg.NewSession("quality", dir), TaskSpec{ID: result.TaskID, Resume: true}, nil)
			if err != nil || calls != 3 || stale.Status == Answered || (report && stale.StopCode != "report_stale") {
				t.Fatalf("未提示陈旧或重复模型：%+v err=%v", stale, err)
			}
		})
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestQualityAdaptiveCorrectionUsesActualCheckerCounterexample(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "(score-1)/9\n")
	calls := 0
	reviewCalls := 0
	var draft QualityDraft
	var replacement QualityFinding
	model := reportModel(func(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch modelStage(ctx) {
		case "quality_review":
			reviewCalls++
			var packet struct {
				Checks []quality.Receipt `json:"checks"`
			}
			if err := json.Unmarshal([]byte(msgs[1].Content), &packet); err != nil {
				t.Fatal(err)
			}
			if len(packet.Checks) != 1 || packet.Checks[0].Result != "fail" || packet.Checks[0].Cases[1].Actual != "1" {
				t.Fatalf("未收到真实反例：%+v", packet.Checks)
			}
			replacement = draft.Findings[0]
			replacement.Statement = "建议将区间缩放到0至25，须同步替换原式"
			replacement.Kind = "inference"
			correctedCheck := draft.Checks[0]
			correctedCheck.Expression = "(score-1)/9*25"
			correctedCheck.Premise = "proposed"
			review := QualityReview{SchemaVersion: 1, Findings: []QualityReviewFinding{{FindingID: "F1", Verdict: "revise", Reason: "上界只有1，需要乘25", Replacement: &replacement, Checks: []quality.Check{correctedCheck}}}}
			raw, _ := json.Marshal(review)
			return reply(string(raw))
		case "quality_revision":
			var packet struct {
				Checks []quality.Receipt `json:"checks"`
				Review QualityReview     `json:"review"`
			}
			_ = json.Unmarshal([]byte(msgs[1].Content), &packet)
			if packet.Checks[0].Result != "fail" || packet.Review.Findings[0].Replacement == nil {
				t.Fatal("修订没有实际失败反馈")
			}
			check := draft.Checks[0]
			check.Expression = "(score-1)/9*25"
			check.Premise = "proposed"
			sub := QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{{Operation: "replace", FindingID: "F1", Finding: packet.Review.Findings[0].Replacement, Checks: []quality.Check{check}}}}
			return qualityCall("write_report", "publish", sub)
		}
		if calls == 1 {
			return modelCalls(readCall("read", "source.md"))
		}
		e := analysisIntegrationEvidence(ctx)[0]
		draft = qualityTestDraft(e)
		draft.Findings[0].Statement = "此公式把1到10映射到0到25"
		draft.Checks = []quality.Check{{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: quality.Selector{EvidenceIDs: []string{e.ID}, ByteStart: 0, ByteEnd: int64(len(strings.TrimSpace(string(e.Source))))}, Expression: "(score-1)/9", Premise: "source_arithmetic", Bindings: map[string]quality.Binding{"score": {Kind: "test_input"}}, Cases: []quality.Case{{Name: "下界", Inputs: map[string]string{"score": "1"}, Expected: "0"}, {Name: "上界", Inputs: map[string]string{"score": "10"}, Expected: "25"}}}}
		return qualityCall("verify_findings", "verify", draft)
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("quality", dir), qualitySpec(true), nil)
	if err != nil || result.Status != NeedsVerification || calls != 4 || reviewCalls != 1 {
		t.Fatalf("修订失败：%+v calls=%d err=%v", result, calls, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "汇总.md"))
	if !strings.Contains(string(body), "25") || strings.Contains(string(body), "此公式把1到10映射") {
		t.Fatalf("发布未经核验文字：%s", body)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	raw, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", result.TaskID, "artifacts", state.Quality.FinalCheckArtifactID))
	_ = raw
	_ = err
	if result.Report == nil || result.Report.SemanticResult != "not_run" {
		t.Fatal("内部复核冒充语义验收")
	}
}

func TestQualityInvalidReviewDoesNotRepairOrPublishUnsupportedClaims(t *testing.T) {
	for _, variant := range []string{"bad_json", "tools", "empty", "provider"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "原文只检查非空。\n")
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				switch modelStage(ctx) {
				case "quality_review":
					switch variant {
					case "bad_json":
						return reply("{")
					case "empty":
						return reply("")
					case "tools":
						return modelCalls(readCall("evil", "source.md"))
					default:
						return nil, errors.New("脚本请求错误")
					}
				case "quality_revision":
					return qualityCall("write_report", "publish", QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{}})
				}
				if calls == 1 {
					return modelCalls(readCall("read", "source.md"))
				}
				d := qualityTestDraft(analysisIntegrationEvidence(ctx)[0])
				d.Findings[0].Statement = "已经验证全部URL合法"
				return qualityCall("verify_findings", "verify", d)
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("q", dir), qualitySpec(true), nil)
			if variant == "provider" {
				if result.Status != Failed || err == nil || calls != 3 {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err != nil || result.Status != Blocked || result.StopCode != "quality_not_ready" || calls != 4 {
				t.Fatalf("%+v calls=%d err=%v", result, calls, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
				t.Fatal("错误报告被发布")
			}
			resumed, _ := eng.Run(context.Background(), ctxpkg.NewSession("q", dir), TaskSpec{ID: result.TaskID, Resume: true}, nil)
			if calls > 4 || resumed.Status == NeedsVerification {
				t.Fatal("恢复重试或误成功")
			}
		})
	}
}

func TestQualitySharedBudgetsStopExactlyWithoutClosing(t *testing.T) {
	for _, maxModels := range []int{2, 3} {
		t.Run(fmt.Sprint(maxModels), func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "原文事实\n")
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return modelCalls(readCall("r", "source.md"))
				}
				if modelStage(ctx) == "quality_revision" {
					return qualityCall("write_report", "p", QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{}})
				}
				return qualityCall("verify_findings", "v", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: maxModels, MaxToolCalls: 3, MaxTurns: 3, Timeout: time.Minute}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("q", dir), qualitySpec(true), nil)
			if err != nil || calls != maxModels || result.Usage.ModelCalls != maxModels {
				t.Fatalf("%+v calls=%d err=%v", result, calls, err)
			}
			if maxModels == 2 {
				if result.Status != BudgetExhausted || result.StopCode != "model_limit" {
					t.Fatalf("%+v", result)
				}
			} else if result.Status != NeedsVerification || len(result.Delivery.Effects) != 1 {
				t.Fatalf("最后一次准入未交付：%+v", result)
			}
		})
	}
}

func TestQualityReadyTicketResumesPublicationWithFrozenScopeWithoutModel(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "范围内的逐字事实\n")
	calls := 0
	injected := false
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if modelStage(ctx) == "quality_revision" {
			r := currentRun(ctx)
			persist := r.persist
			r.persist = func(kind string, state taskState) error {
				if kind == "report_draft" && !injected {
					injected = true
					return errors.New("故障注入：已复验，尚未准备Pending")
				}
				return persist(kind, state)
			}
			return qualityCall("write_report", "publish", QualitySubmission{DraftID: r.state.Quality.DraftID, Corrections: []QualityCorrection{}})
		}
		if calls == 1 {
			return modelCalls(readCall("read", "source.md"))
		}
		return qualityCall("verify_findings", "verify", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
	})
	spec := qualitySpec(true)
	spec.ReadScope = &ReadScopeSpec{Paths: []string{"source.md"}, RequireComplete: true}
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 3}})
	session := ctxpkg.NewSession("scope-ticket", dir)
	failed, err := eng.Run(context.Background(), session, spec, nil)
	if err == nil || failed.Status != Failed || failed.StopCode != "storage_error" || calls != 3 {
		t.Fatalf("故障未触发：%+v err=%v", failed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
		t.Fatal("未到Pending却写入文件")
	}
	resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("scope-ticket", dir), TaskSpec{ID: failed.TaskID, Resume: true}, nil)
	if err != nil || resumed.Status != NeedsVerification || calls != 3 || resumed.Usage.ModelCalls != 3 || resumed.Usage.ToolCalls != 4 || len(resumed.Artifacts) != 1 {
		t.Fatalf("票据未续交付：%+v calls=%d err=%v", resumed, calls, err)
	}
	state := loadAnalysisIntegrationState(t, dir, resumed.TaskID)
	if len(state.Quality.ScopeDependencies) == 0 || state.Quality.Status != "published" {
		t.Fatal("恢复丢失完整范围证明")
	}
}

func TestQualityMissingDraftIdentityRemainsBlockedWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "有来源的逐字事实\n")
	calls := 0
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if modelStage(ctx) == "quality_revision" {
			return modelCalls(schema.ToolCall{ID: "publish", Name: "write_report", Arguments: json.RawMessage(`{"corrections":[]}`)})
		}
		if calls == 1 {
			return modelCalls(readCall("read", "source.md"))
		}
		return qualityCall("verify_findings", "verify", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("missing-ticket", dir), qualitySpec(true), nil)
	if err != nil || result.Status != Blocked || result.StopCode != "quality_contract_invalid" || !strings.Contains(result.Reason, "缺少必填draft_id") || calls != 3 || len(result.Artifacts) != 0 {
		t.Fatalf("缺身份被补填或修复：%+v calls=%d err=%v", result, calls, err)
	}
	if result.Quality == nil || len(result.Quality.EvidenceIDs) != 1 {
		t.Fatalf("停止未交付已登记材料身份：%+v", result.Quality)
	}
}
