package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDeliveryContentTracksConfirmedBytesAndKeepsHistoryBoundary(t *testing.T) {
	for _, content := range []string{"原草稿", "修订后正文"} {
		t.Run(content, func(t *testing.T) {
			s := taskState{DeliveryReview: &DeliveryReviewState{Version: 1, AdmissionCount: 1, Status: "completed", OriginRunID: "run", ContentComparisons: deliveryReviewCandidates(schema.Message{ToolCalls: []schema.ToolCall{reviewWriteCall("candidate", "原草稿", nil)}})}}
			pending := pendingAction{ID: "confirmed-action", Call: reviewWriteCall("publish", content, nil), Before: &fileVersion{Path: "result.md", Version: "missing"}, Expected: &fileVersion{Path: "result.md", Version: deliveryContentSHA(content)}}
			confirmed := schema.NewToolResult("publish", "", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)
			recordDeliveryReviewContent(&s, "other-run", pending, confirmed)
			if s.DeliveryReview.ContentComparisons[0].Status != "not_compared" {
				t.Fatal("恢复运行覆盖历史比较")
			}
			recordDeliveryReviewContent(&s, "run", pending, confirmed)
			c := s.DeliveryReview.ContentComparisons[0]
			want := "changed"
			if content == "原草稿" {
				want = "unchanged"
			}
			if c.Status != want || c.ActionID != pending.ID || c.ConfirmedSHA256 != deliveryContentSHA(content) {
				t.Fatalf("确认字节比较错误: %+v", c)
			}
			if err := validateDeliveryReviewComparisons(s.DeliveryReview); err != nil {
				t.Fatal(err)
			}
			recordDeliveryReviewContent(&s, "run", pending, schema.NewToolResult("publish", "", schema.ToolFailed, schema.EffectUnknown, "", nil))
			if c = s.DeliveryReview.ContentComparisons[0]; c.Status != "unknown" || c.ConfirmedSHA256 != "" {
				t.Fatalf("未知效果被当作已确认修改: %+v", c)
			}
			if err := validateDeliveryReviewComparisons(s.DeliveryReview); err != nil {
				t.Fatal(err)
			}
		})
	}
	duplicates := schema.Message{ToolCalls: []schema.ToolCall{reviewWriteCall("a", "甲", nil), reviewWriteCall("b", "乙", nil)}}
	if c := deliveryReviewCandidates(duplicates); len(c) != 0 {
		t.Fatal("同批同路径多候选被猜成唯一草稿")
	}
}

func TestDeliveryContentComparesFreeJSONWithoutChangingIt(t *testing.T) {
	const draft = `{"ok":false,"value":0}`
	const final = `{"ok":true,"value":0}`
	s := taskState{DeliveryReview: &DeliveryReviewState{AdmissionCount: 1, Status: "completed", OriginRunID: "run", ContentComparisons: deliveryReviewCandidates(schema.Message{Content: draft})}}
	r := RunResult{RunID: "run", FinalText: final, Status: Answered}
	recordDeliveryReviewAnswer(&s, r)
	if c := s.DeliveryReview.ContentComparisons[0]; c.Status != "changed" || c.ConfirmedSHA256 != deliveryContentSHA(final) || r.FinalText != final {
		t.Fatalf("业务JSON被改写或未比较: %+v", c)
	}
	s.DeliveryReview.ContentComparisons[0].Status = "unchanged"
	if err := validateDeliveryReviewComparisons(s.DeliveryReview); err == nil {
		t.Fatal("比较状态与摘要矛盾仍通过恢复校验")
	}
}

// 脚本先提交有问题的草稿，再读取真实核对回执；不根据调用序号预置正确正文。
func TestDeliveryFocusAdaptiveArithmeticPublishesObservedResult(t *testing.T) {
	for _, sample := range []struct{ base, factor, unit, shown int }{{11, 4, 7, 19}, {3, 2, 5, 13}} {
		t.Run(fmt.Sprint(sample), func(t *testing.T) {
			dir := t.TempDir()
			source := fmt.Sprintf(`{"base":%d,"factor":%d,"unit":%d,"shown":%d}`, sample.base, sample.factor, sample.unit, sample.shown)
			writeFixture(t, dir, "source.json", source)
			calls := 0
			published, observedResult := "", ""
			p := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				r := currentRun(ctx)
				switch calls {
				case 1:
					return modelCalls(readCall("read", "source.json"))
				case 2:
					e := analysisIntegrationEvidence(ctx)[0]
					notes := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "shown", Kind: "fact", Statement: fmt.Sprintf("原文标注shown=%d", sample.shown), Context: "仅这个JSON对象，计算关系base+factor*unit另行核对", Evidence: []AnalysisCitation{{EvidenceID: e.ID, StartLine: 1, EndLine: 1, Quote: source}}}}}
					return modelCalls(reviewWriteCall("draft", "直接采用原文标注，没有核对公式。", notes))
				case 3:
					if modelStage(ctx) != "delivery_review" || !strings.Contains(messages[0].Content, `"claims"`) || !strings.Contains(messages[0].Content, "check_evidence") {
						return nil, fmt.Errorf("处理机会没有命题及可用核对入口")
					}
					e := analysisIntegrationEvidence(ctx)[0]
					selector := map[string]any{"file_ref": r.fileRefForEvidence(ctx, e), "byte_start": 0, "byte_end": len(source)}
					bindings := map[string]any{}
					for _, key := range []string{"base", "factor", "unit"} {
						bindings[key] = map[string]any{"kind": "source_value", "source": selector, "pointer": "/" + key}
					}
					args, _ := json.Marshal(map[string]any{"checks": []any{map[string]any{"kind": "arithmetic", "subject": selector, "expression": "base+factor*unit", "bindings": bindings, "premise": "translated", "cases": []any{map[string]any{"name": "原文标注核对", "expected": fmt.Sprint(sample.shown)}}}}})
					return modelCalls(schema.ToolCall{ID: "calculate", Name: "check_evidence", Arguments: args})
				case 4:
					last := messages[len(messages)-1].Content
					at := strings.Index(last, "{")
					var response struct {
						Checks []struct{ ID, Result, Detail string }
					}
					if at < 0 || json.Unmarshal([]byte(last[at:]), &response) != nil || len(response.Checks) != 1 {
						return nil, fmt.Errorf("没有真实核对回执: %s", last)
					}
					check := response.Checks[0]
					actual := regexp.MustCompile(`实际 ([0-9]+)，断言`).FindStringSubmatch(check.Detail)
					if len(actual) != 2 || check.Result != "fail" && check.Result != "pass" {
						return nil, fmt.Errorf("检查器未提供实际计算值: %+v", check)
					}
					observedResult = check.Result
					published = fmt.Sprintf("按给定公式静态计算为%s；原文标注%d。核对%s。仅证明这个对象和给定数学关系，代码前提及运行效果未验证。", actual[1], sample.shown, check.Result)
					e := analysisIntegrationEvidence(ctx)[0]
					notes := findingsReviewUpdate{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "calculated", Kind: "inference", Statement: published, DependsOn: []string{"shown"}, Context: "仅给定JSON及数学关系", Limitations: "翻译前提未由程序确认，没有执行资料脚本", Evidence: []AnalysisCitation{{EvidenceID: e.ID, StartLine: 1, EndLine: 1, Quote: source}}}}, SourceReviews: []SourceReviewInput{{EvidenceIDs: []string{e.ID}, FindingIDs: []string{"calculated"}, CheckIDs: []string{check.ID}, Disposition: "addressed", Reason: "根据真实检查回执形成限定结论并用于正文"}}}
					return modelCalls(reviewWriteCall("publish", published, notes))
				case 5:
					return reply("已按核对回执交付限定结论。")
				}
				return nil, fmt.Errorf("不应增加模型阶段")
			})
			spec := deliverySpec(tools.ModeWorkspace)
			spec.ReadFeatures = &ReadFeatureSpec{Version: 1, LocalChecks: true}
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 5, MaxToolCalls: 6, MaxTurns: 5}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("定向数值核对", dir), spec, nil)
			if err != nil || result.Status != NeedsVerification || calls != 5 || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 4 || result.DeliveryReview.AdmissionCount != 1 {
				t.Fatalf("结果或预算错误: %+v err=%v calls=%d", result, err, calls)
			}
			wantResult := "pass"
			if sample.base+sample.factor*sample.unit != sample.shown {
				wantResult = "fail"
			}
			if observedResult != wantResult || !strings.Contains(published, fmt.Sprint(sample.base+sample.factor*sample.unit)) {
				t.Fatalf("脚本没有按真实反例/通过结果分支: %s %s", observedResult, published)
			}
			body, _ := os.ReadFile(filepath.Join(dir, "result.md"))
			if string(body) != published || len(result.LocalChecks) != 1 || len(result.DeliveryReview.ContentComparisons) != 1 || result.DeliveryReview.ContentComparisons[0].Status != "changed" || len(result.DeliveryReview.Unresolved) != 0 {
				t.Fatalf("核对未进入真实产物或处置不完整: %s %+v", body, result.DeliveryReview)
			}
			text := FormatRunResult(result)
			if !strings.Contains(text, "正文与处理前草稿不同") || !strings.Contains(text, "不代表语义改善") {
				t.Fatal("展示把字节变化冒充语义提升，或未提供比较")
			}
		})
	}
}
