package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDeliverySourceHintsAreBoundedAndDoNotInventAliases(t *testing.T) {
	facts := []deliveryPathFact{{path: ".project/policies/agent/RULES.md", location: schema.ContentSourceLocation{Path: "输入/指南.md", Version: "v1", EvidenceID: "E1", FirstLine: 7, LastLine: 7}}}
	fence := strings.Repeat("\x60", 3)
	bad := "结论见P9:2。\n" + fence + "text\nproject/\n├── policies/\n│   └── agent/\n│       └── RULES.md\n" + fence + "\n"
	issues := reviewDeliveryMarkdown(bad, facts)
	if len(issues) != 2 || issues[0].Code != "citation_alias_unresolved" || issues[1].Code != "source_path_difference" || len(issues[1].Locations) != 1 || issues[1].Locations[0].Path != "输入/指南.md" {
		t.Fatalf("提示未保留真实来源或顺序: %+v", issues)
	}
	for _, good := range []string{
		"见输入/指南.md:7。\n" + fence + "text\n.project/\n└── policies/agent/RULES.md\n" + fence,
		"P9 = 输入/指南.md\n见P9:7。",
		fence + "text\nP9:7\n" + fence,
		"{\"业务字段\":\"P9:7\"}",
		"https://example.test/P9:7",
	} {
		if got := reviewDeliveryMarkdown(good, facts); len(got) != 0 {
			t.Fatalf("可靠位置、代码或业务JSON被误报: %q %+v", good, got)
		}
	}
	ambiguous := append(facts, deliveryPathFact{path: ".other/policies/agent/RULES.md"})
	for _, issue := range reviewDeliveryMarkdown("└── policies/agent/RULES.md", ambiguous) {
		if issue.Code == "source_path_difference" {
			t.Fatal("歧义路径被猜测映射")
		}
	}
	ctx, r := deliveryMemoryFixture(t)
	review := r.reviewDeliveryContent(ctx, "result.md", strings.Repeat("P9:2\n", 4000))
	raw, _ := json.Marshal(review)
	if len(raw) > 3000 || len(review.Issues) > 8 || review.Omitted == 0 || !json.Valid(raw) {
		t.Fatalf("有界完整JSON失败: %d %+v", len(raw), review)
	}
	r.state.Spec.DeliveryReview = nil
	if r.reviewDeliveryContent(ctx, "result.md", bad) != nil {
		t.Fatal("默认关闭仍新增检查")
	}
}

func TestDeliveryPathFactsRequireCurrentDeliveredWholeLines(t *testing.T) {
	for _, reason := range []string{"current", "undelivered", "foreign", "historical", "invalidated", "changed", "partial"} {
		t.Run(reason, func(t *testing.T) {
			r, ctx, _ := localCheckFixture(t, ".project/policies/agent/RULES.md\n")
			r.state.Spec.DeliveryReview = &DeliveryReviewSpec{Version: 1}
			entry := r.analysisRuntime.sources["E1"]
			switch reason {
			case "undelivered":
				r.analysisRuntime.delivered["root"]["E1"] = false
			case "foreign":
				entry.AgentID = "sibling"
			case "historical":
				entry.RunID = "old"
			case "invalidated":
				r.analysisRuntime.invalidated = map[string]string{"E1": "bash_started"}
			case "changed":
				if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "source.md"), []byte("新版本"), 0600); err != nil {
					t.Fatal(err)
				}
			case "partial":
				entry.Receipt.ByteStart = 1
				entry.Source = entry.Source[1:]
				entry.Receipt.LineBoundary = &schema.ReadBoundary{StartsLine: false, EndsLine: true}
			}
			r.analysisRuntime.sources["E1"] = entry
			found := false
			for _, fact := range r.deliveryPathFacts(ctx) {
				found = found || fact.path == ".project/policies/agent/RULES.md"
			}
			if found != (reason == "current") {
				t.Fatalf("历史/未交付/半行被升级成路径事实: %s %v", reason, found)
			}
		})
	}
}

func TestDeliveryContentHintsFollowConfirmedActionOrderAndRemainInformational(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	ctx = context.WithValue(ctx, actionIDKey{}, "first")
	call := reviewWriteCall("first", "参考P9:2。", nil)
	first := schema.NewToolResult("first", "写入完成", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)
	r.attachDeliveryContentReview(ctx, call, &first)
	if first.IsError || first.ErrorCode != "" || first.ContentReview == nil || first.ContentReview.Status != "issues_found" {
		t.Fatalf("机械提示影响业务结果: %+v", first)
	}
	r.state.Completed = map[string]schema.ToolResult{"z": first}
	r.state.EffectLedger = &Delivery{Effects: []EffectReceipt{{ActionID: "first", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "result.md"}}}}
	if len(r.currentDeliveryContentIssues(ctx)) != 1 {
		t.Fatal("确认正文的问题没有保留")
	}
	nextCtx := context.WithValue(ctx, actionIDKey{}, "second")
	next := schema.NewToolResult("second", "写入完成", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)
	r.attachDeliveryContentReview(nextCtx, reviewWriteCall("second", "见E1.md:1。", nil), &next)
	r.state.Completed["a"] = next
	r.state.EffectLedger.Effects = append(r.state.EffectLedger.Effects, EffectReceipt{ActionID: "second", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "result.md"}})
	if len(r.currentDeliveryContentIssues(ctx)) != 0 {
		t.Fatal("已修改正文仍被旧提示污染")
	}
	edit := schema.NewToolResult("edit", "编辑完成", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)
	r.attachDeliveryContentReview(context.WithValue(ctx, actionIDKey{}, "edit"), schema.ToolCall{Name: "edit_file", Arguments: []byte("{\"path\":\"result.md\"}")}, &edit)
	r.state.Completed["m"] = edit
	r.state.EffectLedger.Effects = append(r.state.EffectLedger.Effects, EffectReceipt{ActionID: "edit", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "result.md"}})
	if got := r.currentDeliveryContentIssues(ctx); len(got) != 1 || got[0].Kind != "content_review_not_run" {
		t.Fatalf("局部编辑被当作全文通过: %+v", got)
	}
	if len(r.currentDeliveryContentIssues(observe.WithAgent(ctx, "other", ""))) != 0 {
		t.Fatal("他人检查进入当前视图")
	}
	r.runID = "resumed"
	if len(r.currentDeliveryContentIssues(ctx)) != 0 {
		t.Fatal("恢复把旧动作升级为当前检查")
	}
}

func TestDeliveryContentFinalHintsDoNotRewriteOrAddSecondOpportunity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			calls := 0
			p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				return reply("参考P9:2；尚未展开来源。")
			})
			spec := TaskSpec{Goal: "回答", Mode: tools.ModeReadOnly, Kind: TaskAnswer}
			if enabled {
				spec.DeliveryReview = &DeliveryReviewSpec{Version: 1}
			}
			dir := t.TempDir()
			result, err := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 3, MaxTurns: 3}}).Run(context.Background(), ctxpkg.NewSession("自由终答", dir), spec, nil)
			wantCalls := 1
			if enabled {
				wantCalls = 2
			}
			if err != nil || result.Status != Answered || calls != wantCalls || result.FinalText != "参考P9:2；尚未展开来源。" {
				t.Fatalf("自由答复改写、被阻断或重复退回: %+v %v calls=%d", result, err, calls)
			}
			if enabled {
				found := false
				for _, issue := range result.DeliveryReview.Unresolved {
					found = found || issue.Kind == "citation_alias_unresolved"
				}
				if !found {
					t.Fatal("忽略提示后未解决项消失")
				}
			}
		})
	}
}

func TestLocalArithmeticOutputSeparatesMeaningFromTotalAndNamesSources(t *testing.T) {
	receipt := quality.Receipt{ID: "K1", Kind: "numeric", Result: "pass", PremiseResult: "pass", Formula: "4+4+4", Cases: []quality.CaseResult{{Expected: "12", Actual: "12", Result: "pass"}}, Sources: []quality.SourceRef{{Path: "资料/规则.md", Version: "v1", EvidenceIDs: []string{"E1"}, FirstLine: 10, LastLine: 14, ByteStart: 25, ByteEnd: 100}}}
	out := localCheckOutputV2("receipt", []quality.Receipt{receipt}, nil)
	for _, want := range []string{"\"interpretation_result\":\"not_run\"", "资料/规则.md", "\"first_line\":10", "\"formula\":\"4+4+4\"", "\"actual\":\"12\"", "分项"} {
		if !strings.Contains(out, want) {
			t.Fatalf("算术通过被当成分项解释通过或缺少位置 %s: %s", want, out)
		}
	}
}

// 脚本从实际读取和机械回执生成修订，不增加模型阶段，也不在生产提示内写入答案。
func TestDeliverySourceHintsDriveOneAdaptiveRevision(t *testing.T) {
	dir := t.TempDir()
	source := "加载位置为 .project/policies/agent/RULES.md。\n"
	writeFixture(t, dir, "source.md", source)
	fence := strings.Repeat("\x60", 3)
	calls := 0
	p := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read", "source.md"))
		case 2:
			e := analysisIntegrationEvidence(ctx)[0]
			notes := map[string]any{"schema_version": 1, "findings": []any{map[string]any{"id": "location", "kind": "fact", "statement": "加载位置以原文为准", "evidence": []any{map[string]any{"evidence_id": e.ID, "start_line": 1, "end_line": 1, "quote": strings.TrimSpace(source)}}}}, "source_reviews": []any{map[string]any{"evidence_ids": []string{e.ID}, "finding_ids": []string{"location"}, "disposition": "addressed", "reason": "加载位置已有原文依据"}}}
			return modelCalls(reviewWriteCall("draft", "依据P9:1。\n"+fence+"text\n└── policies/agent/RULES.md\n"+fence, notes))
		case 3:
			if modelStage(ctx) != "delivery_review" {
				return nil, fmt.Errorf("机械提示未触发已有的一次处理")
			}
			if _, err := os.Stat(filepath.Join(dir, "result.md")); !os.IsNotExist(err) {
				return nil, fmt.Errorf("候选提前写入")
			}
			all := ""
			for _, m := range messages {
				all += m.Content
			}
			for _, want := range []string{"candidate_issues", "citation_alias_unresolved", "source_path_difference", "source.md:1-1"} {
				if !strings.Contains(all, want) {
					return nil, fmt.Errorf("未提供可复核位置 %s", want)
				}
			}
			e := analysisIntegrationEvidence(ctx)[0]
			path := deliveryPathPattern.FindString(string(e.Source))
			if path == "" {
				return nil, fmt.Errorf("没有实际来源路径")
			}
			return modelCalls(reviewWriteCall("publish", fmt.Sprintf("加载位置：%s（%s:%d）。\n"+fence+"text\n└── %s\n"+fence, path, e.Receipt.Path, e.Receipt.FirstLine, path), nil))
		case 4:
			if strings.Contains(messages[len(messages)-1].Content, "\"status\":\"issues_found\"") {
				return nil, fmt.Errorf("正确路径仍被误报")
			}
			return reply("已根据来源修订并交付。")
		}
		return nil, fmt.Errorf("多余模型请求")
	})
	result, err := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 4, MaxToolCalls: 6, MaxTurns: 4}}).Run(context.Background(), ctxpkg.NewSession("路径与引用自适应", dir), deliverySpec(tools.ModeWorkspace), nil)
	if err != nil || result.Status != NeedsVerification || calls != 4 || result.Usage.ToolCalls != 3 || result.DeliveryReview.AdmissionCount != 1 || len(result.DeliveryReview.Unresolved) != 0 {
		t.Fatalf("交付/计数/未解决项错误: %+v %v", result, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
	if strings.Contains(string(raw), "P9:") || !strings.Contains(string(raw), ".project/policies/agent/RULES.md") {
		t.Fatalf("没有按真实来源修订: %s", raw)
	}
}
