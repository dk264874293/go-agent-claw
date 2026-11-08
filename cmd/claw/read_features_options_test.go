package main

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
)

func TestCLIReadFeaturesExplicitSwitchesAndResume(t *testing.T) {
	for _, tc := range []struct {
		values  map[string]string
		resume  bool
		bad     bool
		version int
	}{
		{nil, false, false, 0}, {map[string]string{"source_views": "off"}, false, false, 2},
		{map[string]string{"local_checks": "on"}, false, false, 4},
		{map[string]string{"claim_focus": "on", "source_views": "off"}, false, false, 4},
		{map[string]string{"local_checks": ""}, false, true, 0},
		{map[string]string{"claim_focus": "auto"}, false, true, 0},
		{nil, true, false, 0}, {map[string]string{"local_checks": "off"}, true, true, 0}, {map[string]string{"source_views": "on"}, true, true, 0},
	} {
		s := engine.TaskSpec{Resume: tc.resume}
		err := applyCLIReadFeatures(&s, tc.values)
		if (err != nil) != tc.bad || !tc.bad && s.ReadContextVersion != tc.version {
			t.Fatalf("CLI开关错误: %+v %+v %v", tc, s, err)
		}
	}
}

func TestCLIReadFeaturesRejectStrictModeAndDoNotMutateOnFailure(t *testing.T) {
	for _, spec := range []engine.TaskSpec{{Analysis: true}, {Report: &engine.ReportSpec{}}, {Quality: &engine.QualitySpec{Version: 1}}} {
		if err := applyCLIReadFeatures(&spec, map[string]string{"claim_focus": "on"}); err == nil || spec.ReadFeatures != nil || spec.ReadContextVersion != 0 {
			t.Fatal("严格入口被实验隐式改变")
		}
	}
}
