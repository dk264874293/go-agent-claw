package main

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIDelegationDefaultsAndResume(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		mode        tools.Mode
		spec        engine.TaskSpec
		want        string
		err         bool
	}{
		{"默认自动", "", tools.ModeReadOnly, engine.TaskSpec{}, "auto", false},
		{"显式关闭", "off", tools.ModeReadOnly, engine.TaskSpec{}, "off", false},
		{"显式开启", "on", tools.ModeReadOnly, engine.TaskSpec{Kind: engine.TaskAnswer}, "on", false},
		{"非目标保持默认", "", tools.ModeWorkspace, engine.TaskSpec{Kind: engine.TaskChange}, "", false},
		{"禁止修改任务配置", "auto", tools.ModeReadOnly, engine.TaskSpec{Kind: engine.TaskChange}, "", true},
		{"禁止写入模式配置", "on", tools.ModeTrusted, engine.TaskSpec{}, "", true},
		{"拒绝非法值", "AUTO", tools.ModeReadOnly, engine.TaskSpec{}, "", true},
		{"恢复不覆盖", "on", tools.ModeReadOnly, engine.TaskSpec{Resume: true, Delegation: "off"}, "off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			err := applyCLIDelegation(&spec, tc.value, tc.mode)
			if (err != nil) != tc.err || spec.Delegation != tc.want {
				t.Fatalf("spec=%+v err=%v", spec, err)
			}
			if tc.spec.Resume && spec != tc.spec {
				t.Fatal("恢复策略被启动值覆盖")
			}
		})
	}
}
