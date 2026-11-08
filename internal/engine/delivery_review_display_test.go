package engine

import (
	"strings"
	"testing"
)

func TestDeliveryReviewTerminalSummaryKeepsFinalTextAndUnresolved(t *testing.T) {
	text := `{"result":"业务JSON原文","amount":0}`
	result := RunResult{Status: Answered, RunID: "current-run", FinalText: text, DeliveryReview: &DeliveryReviewSummary{Version: 1, Status: "completed", AdmissionCount: 1, OriginRunID: "old-run", Documents: 3, Addressed: 1, NotRelevant: 1, Unverified: 1, Undeclared: 0, ReadyToResolve: 1, Unresolved: []DeliveryReviewIssue{{ID: "U1", Kind: "source_unverified", Path: "资料.md", Version: "版本", EvidenceIDs: []string{"E1"}, FindingIDs: []string{"F1"}, CheckIDs: []string{"C1"}, Reason: "尚未核实版本边界"}}}}
	got := FormatRunResult(result)
	for _, want := range []string{text, "一次处理响应已记录", "准入次数：1", "共3，有来源发现1，不相关1，待核实1，未声明0，待重新处理1", "历史记录", "不证明重要问题无遗漏", "资料.md", "E1", "F1", "C1", "尚未核实版本边界"} {
		if !strings.Contains(got, want) {
			t.Fatalf("终报缺少%q：%s", want, got)
		}
	}
	if result.FinalText != text || result.DeliveryReview.Unresolved[0].Reason != "尚未核实版本边界" || strings.Contains(got, "交付前处理通过") {
		t.Fatal("展示改写原文或误报质量通过")
	}
	plain := FormatRunResult(RunResult{Status: Answered, FinalText: "普通答复"})
	if strings.Contains(plain, "交付前处理") {
		t.Fatal("默认关闭仍产生展示副作用")
	}
}
