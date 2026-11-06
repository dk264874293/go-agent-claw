package main

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIAnalysisIsExplicitAndCannotOverrideResume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		mode    tools.Mode
		spec    engine.TaskSpec
		wantErr bool
		wantOn  bool
	}{
		{"普通任务不按目标推断", false, tools.ModeReadOnly, engine.TaskSpec{Goal: "分析代码并引用证据"}, false, false},
		{"显式分析", true, tools.ModeReadOnly, engine.TaskSpec{Goal: "分析文档"}, false, true},
		{"显式回答", true, tools.ModeReadOnly, engine.TaskSpec{Kind: engine.TaskAnswer}, false, true},
		{"禁止写入权限", true, tools.ModeWorkspace, engine.TaskSpec{}, true, false},
		{"禁止可信权限", true, tools.ModeTrusted, engine.TaskSpec{}, true, false},
		{"禁止修改任务", true, tools.ModeReadOnly, engine.TaskSpec{Kind: engine.TaskChange}, true, false},
		{"禁止验收命令", true, tools.ModeReadOnly, engine.TaskSpec{VerifyCommand: "true"}, true, false},
		{"恢复不覆盖原设置", true, tools.ModeTrusted, engine.TaskSpec{ID: "历史任务", Resume: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			err := applyCLIAnalysis(&spec, tc.enabled, tc.mode)
			if (err != nil) != tc.wantErr || spec.Analysis != tc.wantOn {
				t.Fatalf("分析选择错误: spec=%+v err=%v", spec, err)
			}
			if spec.Analysis && spec.Kind != engine.TaskAnswer {
				t.Fatal("分析任务未使用 answer")
			}
			if tc.spec.Resume && spec != tc.spec {
				t.Fatal("恢复参数被启动分析选项覆盖")
			}
		})
	}
}
