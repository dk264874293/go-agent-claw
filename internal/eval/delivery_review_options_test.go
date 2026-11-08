package observability

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestBenchmarkDeliveryReviewOrdinaryModesAndLegacyDefaults(t *testing.T) {
	on, off := true, false
	for _, tc := range []TestCase{
		{Mode: tools.ModeReadOnly, DeliveryReview: &on},
		{Mode: tools.ModeWorkspace, DeliveryReview: &on},
		{Mode: tools.ModeWorkspace, Kind: engine.TaskChange, DeliveryReview: &on},
		{Delegation: "off", DeliveryReview: &on},
	} {
		spec, mode, err := benchmarkTaskSpec(tc)
		if err != nil || spec.DeliveryReview == nil || spec.DeliveryReview.Version != 1 || mode != tools.ModeReadOnly && mode != tools.ModeWorkspace || spec.VerifyCommand != "" || spec.ReadFeatures != nil {
			t.Fatalf("普通评测交付前处理配置错误：%+v %s %v", spec, mode, err)
		}
	}
	for _, tc := range []TestCase{{ValidateScript: "true"}, {ValidateScript: "true", DeliveryReview: &off}} {
		spec, mode, err := benchmarkTaskSpec(tc)
		if err != nil || mode != tools.ModeTrusted || spec.VerifyCommand != "true" || spec.DeliveryReview != nil {
			t.Fatalf("旧评测入口发生改变：%+v %s %v", spec, mode, err)
		}
	}
	for _, tc := range []TestCase{
		{ValidateScript: "true", DeliveryReview: &on},
		{Analysis: true, DeliveryReview: &on},
		{ReportPath: "报告.md", DeliveryReview: &on},
		{Analysis: true, Quality: true, DeliveryReview: &on},
		{Mode: tools.ModeTrusted, DeliveryReview: &on},
		{Mode: tools.ModeWorkspace, ValidateScript: "true", DeliveryReview: &on},
		{Mode: tools.ModeReadOnly, Kind: engine.TaskChange, DeliveryReview: &on},
		{Mode: tools.ModeWorkspace, Kind: engine.TaskAnswer, DeliveryReview: &on},
		{Kind: engine.TaskAnswer, DeliveryReview: &on},
	} {
		if _, _, err := benchmarkTaskSpec(tc); err == nil {
			t.Fatalf("接受了不支持的普通评测组合：%+v", tc)
		}
	}
}
