package main

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIDeliveryReviewSwitchesAndResume(t *testing.T) {
	for _, tc := range []struct {
		value                          string
		explicit, resume, bad, enabled bool
	}{
		{"off", false, false, false, false}, {"off", true, false, false, false}, {"on", true, false, false, true},
		{"true", true, false, true, false}, {"", true, false, true, false},
		{"off", false, true, false, false}, {"off", true, true, true, false}, {"on", true, true, true, false},
	} {
		s := engine.TaskSpec{Resume: tc.resume}
		err := applyCLIDeliveryReview(&s, tc.value, tc.explicit, tools.ModeWorkspace, false, false)
		if (err != nil) != tc.bad || (s.DeliveryReview != nil) != tc.enabled {
			t.Fatalf("交付前处理开关错误：%+v %+v %v", tc, s, err)
		}
	}
}

func TestCLIDeliveryReviewKeepsOrdinaryLaneAndRejectsStrictCombinations(t *testing.T) {
	for _, mode := range []tools.Mode{tools.ModeReadOnly, tools.ModeWorkspace} {
		s := engine.TaskSpec{}
		if err := applyCLIDeliveryReview(&s, "on", true, mode, false, false); err != nil || s.DeliveryReview.Version != 1 || s.Mode != mode || mode == tools.ModeReadOnly && s.Kind != engine.TaskAnswer || mode == tools.ModeWorkspace && s.Kind != engine.TaskChange {
			t.Fatalf("普通任务通道错误：%+v %v", s, err)
		}
		if s.ReadFeatures != nil || s.ReadContextVersion != 0 {
			t.Fatal("交付前处理隐式开启了读取实验")
		}
	}
	for _, tc := range []struct {
		spec           engine.TaskSpec
		mode           tools.Mode
		plan, thinking bool
	}{
		{engine.TaskSpec{}, tools.ModeTrusted, false, false},
		{engine.TaskSpec{Kind: engine.TaskChange}, tools.ModeReadOnly, false, false},
		{engine.TaskSpec{Kind: engine.TaskAnswer}, tools.ModeWorkspace, false, false},
		{engine.TaskSpec{Analysis: true}, tools.ModeReadOnly, false, false},
		{engine.TaskSpec{Report: &engine.ReportSpec{}}, tools.ModeWorkspace, false, false},
		{engine.TaskSpec{Quality: &engine.QualitySpec{Version: 1}}, tools.ModeReadOnly, false, false},
		{engine.TaskSpec{}, tools.ModeWorkspace, true, false},
		{engine.TaskSpec{}, tools.ModeWorkspace, false, true},
	} {
		if err := applyCLIDeliveryReview(&tc.spec, "on", true, tc.mode, tc.plan, tc.thinking); err == nil || tc.spec.DeliveryReview != nil {
			t.Fatalf("接受了不支持组合或失败时修改原配置：%+v", tc)
		}
	}
}
