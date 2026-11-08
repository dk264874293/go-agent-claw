package observability

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestBenchmarkQualityConfigurationKeepsTaskLane(t *testing.T) {
	for _, tc := range []TestCase{
		{Quality: true, Analysis: true},
		{Quality: true, Analysis: true, Delegation: "off"},
		{Quality: true, ReportPath: "报告.md"},
		{Quality: true, ReportPath: "报告.md", Delegation: "off"},
	} {
		spec, mode, err := benchmarkTaskSpec(tc)
		if err != nil || spec.Quality == nil || spec.Quality.Version != 1 || tc.Analysis && (mode != tools.ModeReadOnly || spec.Delegation != "off") || tc.ReportPath != "" && (mode != tools.ModeWorkspace || spec.Delegation != "") {
			t.Fatalf("质量配置错误: %+v %s %v", spec, mode, err)
		}
	}
	for _, tc := range []TestCase{
		{Quality: true},
		{Quality: true, Analysis: true, Delegation: "auto"},
		{Quality: true, Analysis: true, Delegation: "on"},
		{Quality: true, Analysis: true, ValidateScript: "true"},
		{Quality: true, ReportPath: "报告.md", ValidateScript: "true"},
		{Quality: true, ReportPath: "报告.md", Analysis: true},
	} {
		if _, _, err := benchmarkTaskSpec(tc); err == nil {
			t.Fatalf("接受了不支持的质量组合: %+v", tc)
		}
	}
	for _, tc := range []TestCase{{Analysis: true}, {ReportPath: "报告.md"}, {ValidateScript: "true"}} {
		spec, _, err := benchmarkTaskSpec(tc)
		if err != nil || spec.Quality != nil {
			t.Fatalf("普通评测被自动升级: %+v %v", spec, err)
		}
	}
}
