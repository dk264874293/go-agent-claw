package engine

import (
	"strings"
	"testing"
)

func TestQualityDeliverySeparatesInternalReviewAndVerification(t *testing.T) {
	result := RunResult{Status: Blocked, StopCode: "quality_not_ready", Quality: &QualityDelivery{Version: 1, DraftID: "draft", Status: "blocked", CheckResult: "partial", ReviewStatus: "completed", RevisionStatus: "completed", Unresolved: []string{"默认值的适用前提未确认"}}}
	text := FormatRunResult(result)
	for _, want := range []string{"质量核验 v1", "本地检查：partial", "风险复核：completed", "作者修订：completed", "待核实：默认值的适用前提未确认", "不代表独立语义验收或运行验收", "quality_not_ready"} {
		if !strings.Contains(text, want) {
			t.Fatalf("终报缺少独立状态 %q: %s", want, text)
		}
	}
	if strings.Contains(text, "验收已通过") {
		t.Fatal("内部检查被误报为引擎验收")
	}
	result.Quality = nil
	if strings.Contains(FormatRunResult(result), "质量核验") {
		t.Fatal("普通任务被展示为已核验")
	}
}
