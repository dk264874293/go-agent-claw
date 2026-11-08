package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func TestDeliveryFocusProjectsCurrentClaimsWithConditionsAndDependencies(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	update := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{
		{ID: "basis", Kind: "fact", Statement: "来源一给出决定性事实", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}},
		{ID: "interpretation", Kind: "inference", Statement: "应先核对两个来源的适用条件", DependsOn: []string{"basis"}, Context: "仅限这两个版本", Limitations: "未执行资料脚本", Evidence: []AnalysisCitation{{EvidenceID: "E2", StartLine: 1, EndLine: 1, Quote: "E2 决定性事实"}}},
	}}
	if b := deliveryNote(t, r, ctx, update); !b.valid {
		t.Fatalf("有效笔记未接受: %+v", b)
	}
	before, _ := json.Marshal(r.state)
	prompt := r.deliveryReviewPrompt(ctx)
	for _, want := range []string{`"claims"`, "interpretation", "basis", "仅限这两个版本", "未执行资料脚本", "E1 决定性事实", "E2 决定性事实", "存在、非空、类型和合法性", "正文"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("定向清单丢失命题或依据 %q: %s", want, prompt)
		}
	}
	if len(prompt) > 4096 {
		t.Fatalf("清单超过既有4KiB上限: %d", len(prompt))
	}
	after, _ := json.Marshal(r.state)
	if string(before) != string(after) {
		t.Fatal("定向投影改变持久状态或预算")
	}
	// 同摘要Bash失效也不得借笔记重新取得引用资格。
	r.mu.Lock()
	r.invalidateMemoryLocked("", "bash_started", true)
	r.mu.Unlock()
	if next := r.deliveryReviewPrompt(ctx); strings.Contains(next, `"id":"interpretation"`) {
		t.Fatal("失效命题继续进入定向目录")
	}
}

func TestDeliveryFocusPreservesFailedCheckWithoutResolvedSources(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	r.state.LocalChecks = []LocalCheckSummary{{ID: "bad-range", AgentID: "root", OriginRunID: r.runID, Kind: "json", Result: "not_run", ReasonCode: "source_incomplete", Reason: "主体跨页缺少尾部，不能判断字段不存在", FileRef: "F1", ReceiptArtifactID: "receipt"}}
	var found bool
	for _, issue := range r.deliveryReviewSummary(ctx).Unresolved {
		if issue.ID == "bad-range" && issue.Kind == "local_check_unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatal("没有成功解析来源的核对失败从待办中消失")
	}
	prompt := r.deliveryReviewPrompt(ctx)
	for _, want := range []string{`"checks"`, "bad-range", "source_incomplete", "主体跨页缺少尾部", "not_run"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("核对失败未提供可处理的原因 %q: %s", want, prompt)
		}
	}
	// 不能借其他Agent或历史核对制造当前任务待办。
	r.state.LocalChecks[0].AgentID = "child"
	for _, issue := range r.deliveryReviewSummary(ctx).Unresolved {
		if issue.ID == "bad-range" {
			t.Fatal("跨Agent核对进入当前待办")
		}
	}
}

func TestDeliveryFocusNoFindingsCannotBeHiddenByDocumentDeclarations(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	for _, id := range []string{"E1", "E2"} {
		if b := deliveryNote(t, r, ctx, deliveryReviewUpdate([]string{id}, "not_relevant")); !b.valid {
			t.Fatal("有理由的不相关声明应保持合法")
		}
	}
	var found bool
	for _, issue := range r.deliveryReviewSummary(ctx).Unresolved {
		if issue.Kind == "no_valid_findings" {
			found = true
		}
	}
	if !found {
		t.Fatal("全部文档声明不相关掩盖了没有有效发现的处理触发条件")
	}
	// 非显式任务不新增目录、工具或处理门槛。
	r.state.Spec.DeliveryReview = nil
	if prompt := r.deliveryReviewPrompt(observe.WithAgent(ctx, "root", "")); prompt != "" {
		t.Fatal("关闭模式泄露定向提示")
	}
}

func TestDeliveryFocusSkipsOversizedClaimWithoutLosingLaterClaim(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	ref := []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}
	r.state.ReadMemory.Notes["root"] = AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{
		{ID: "oversized", Kind: "inference", Statement: strings.Repeat("较长但合法的发现", 120), Context: strings.Repeat("适用条件", 100), Evidence: ref},
		{ID: "compact", Kind: "inference", Statement: "这个完整短发现仍应可见", Context: "仅当前版本", Evidence: ref},
	}}
	prompt := r.deliveryReviewPrompt(ctx)
	start := strings.Index(prompt, "：") + len("：")
	end := strings.Index(prompt, "\n在既有适用工具")
	var view struct {
		Claims        []deliveryReviewClaim `json:"claims"`
		OmittedClaims int                   `json:"omitted_claims"`
	}
	if len(prompt) > 4096 || end < start || json.Unmarshal([]byte(prompt[start:end]), &view) != nil || len(view.Claims) != 1 || view.Claims[0].Finding.ID != "compact" || view.OmittedClaims != 1 {
		t.Fatalf("超大条目阻塞其他命题或产生残缺JSON: %s", prompt)
	}
	if !strings.Contains(prompt, "check_evidence不可用") {
		t.Fatal("未启用局部核对却引导调用隐藏工具")
	}
}
