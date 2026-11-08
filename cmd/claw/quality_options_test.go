package main

import (
	"reflect"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIQualityOnlyExplicitSupportedLanes(t *testing.T) {
	report := &engine.ReportSpec{Version: 1, Format: "evidence_v1", TargetPath: "报告.md", CreateOnly: true}
	for _, tc := range []struct {
		name       string
		spec       engine.TaskSpec
		mode       tools.Mode
		delegation string
		plan       bool
		thinking   bool
		invalid    bool
	}{
		{name: "分析默认关闭委派", spec: engine.TaskSpec{Analysis: true, Delegation: "auto"}, mode: tools.ModeReadOnly},
		{name: "报告", spec: engine.TaskSpec{Report: report}, mode: tools.ModeWorkspace},
		{name: "报告显式关闭委派", spec: engine.TaskSpec{Report: report}, mode: tools.ModeWorkspace, delegation: "off"},
		{name: "分析显式关闭委派", spec: engine.TaskSpec{Analysis: true, Delegation: "off"}, mode: tools.ModeReadOnly, delegation: "off"},
		{name: "普通任务", mode: tools.ModeWorkspace, invalid: true},
		{name: "错误权限", spec: engine.TaskSpec{Analysis: true}, mode: tools.ModeWorkspace, invalid: true},
		{name: "回答写报告", spec: engine.TaskSpec{Report: report, Kind: engine.TaskAnswer}, mode: tools.ModeWorkspace, invalid: true},
		{name: "双入口", spec: engine.TaskSpec{Analysis: true, Report: report}, mode: tools.ModeWorkspace, invalid: true},
		{name: "规划", spec: engine.TaskSpec{Analysis: true}, mode: tools.ModeReadOnly, plan: true, invalid: true},
		{name: "额外思考", spec: engine.TaskSpec{Analysis: true}, mode: tools.ModeReadOnly, thinking: true, invalid: true},
		{name: "验收命令", spec: engine.TaskSpec{Analysis: true, VerifyCommand: "true"}, mode: tools.ModeReadOnly, invalid: true},
		{name: "委派on", spec: engine.TaskSpec{Analysis: true}, mode: tools.ModeReadOnly, delegation: "on", invalid: true},
		{name: "显式委派auto", spec: engine.TaskSpec{Analysis: true}, mode: tools.ModeReadOnly, delegation: "auto", invalid: true},
		{name: "恢复不更改策略", spec: engine.TaskSpec{Resume: true}, mode: tools.ModeReadOnly, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			err := applyCLIQuality(&spec, true, tc.mode, tc.plan, tc.thinking, tc.delegation)
			if (err != nil) != tc.invalid {
				t.Fatalf("质量入口判断错误: %+v %v", spec, err)
			}
			if tc.invalid {
				if !reflect.DeepEqual(spec, tc.spec) {
					t.Fatal("非法配置改变了原任务")
				}
				return
			}
			if spec.Quality == nil || spec.Quality.Version != 1 || spec.Mode != tc.spec.Mode || spec.Analysis && spec.Delegation != "off" {
				t.Fatalf("质量配置丢失或改变执行权限: %+v", spec)
			}
		})
	}
}

func TestCLIQualityOffDoesNotUpgradeTasks(t *testing.T) {
	for _, resume := range []bool{false, true} {
		spec := engine.TaskSpec{Resume: resume, Delegation: "auto"}
		before := spec
		if err := applyCLIQuality(&spec, false, tools.ModeWorkspace, false, false, "auto"); err != nil || !reflect.DeepEqual(spec, before) {
			t.Fatalf("未显式开启仍改变任务: %+v %v", spec, err)
		}
	}
}
