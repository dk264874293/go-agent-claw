package observability

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
)

func TestBenchmarkReadFeaturesPropagateIndependently(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		features := &engine.ReadFeatureSpec{Version: 1, SourceViews: mask&1 != 0, LocalChecks: mask&2 != 0, ClaimFocus: mask&4 != 0}
		spec, _, err := benchmarkTaskSpec(TestCase{ValidateScript: "true", ReadFeatures: features})
		if err != nil {
			t.Fatal(err)
		}
		want := 4
		if mask == 0 {
			want = 2
		}
		if spec.ReadContextVersion != want || (spec.ReadFeatures == nil) != (mask == 0) {
			t.Fatalf("Bench组合%d错误: %+v", mask, spec)
		}
	}
	for _, tc := range []TestCase{{Analysis: true}, {ReportPath: "报告.md"}, {Analysis: true, Quality: true}} {
		tc.ReadFeatures = &engine.ReadFeatureSpec{Version: 1, LocalChecks: true}
		if _, _, err := benchmarkTaskSpec(tc); err == nil {
			t.Fatal("Bench严格模式接受实验")
		}
	}
}
