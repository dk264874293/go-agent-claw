package main

import (
	"reflect"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIReadScopeIsExplicitAndDoesNotChangePermissions(t *testing.T) {
	for _, tc := range []struct {
		name                                              string
		paths                                             []string
		glob                                              string
		complete, globExplicit, completeExplicit, invalid bool
	}{
		{name: "未声明范围", glob: "*"},
		{name: "明确全文资料", paths: []string{"02资料包", "01资料包", "01资料包"}, glob: "*.md", complete: true},
		{name: "包含逗号的文件名", paths: []string{"资料,正文.md"}, glob: "*"},
		{name: "无范围完整要求", glob: "*", complete: true, invalid: true},
		{name: "无范围显式过滤", glob: "*.md", globExplicit: true, invalid: true},
		{name: "无范围显式要求参数", glob: "*", completeExplicit: true, invalid: true},
		{name: "越界", paths: []string{"../secret"}, glob: "*", invalid: true},
		{name: "绝对路径", paths: []string{"/tmp/secret"}, glob: "*", invalid: true},
		{name: "非法过滤", paths: []string{"docs"}, glob: "**/*.md", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := engine.TaskSpec{Mode: tools.ModeReadOnly, Resume: true}
			err := applyCLIReadScope(&spec, tc.paths, tc.glob, tc.complete, tc.globExplicit, tc.completeExplicit)
			if (err != nil) != tc.invalid {
				t.Fatalf("读取配置判断错误: %+v %v", spec, err)
			}
			if spec.Mode != tools.ModeReadOnly || !spec.Resume {
				t.Fatal("读取配置扩大权限或丢失恢复身份")
			}
			if !tc.invalid && len(tc.paths) == 0 && spec.ReadScope != nil {
				t.Fatal("恢复时填造了未指定范围")
			}
			if !tc.invalid && len(tc.paths) > 0 && (spec.ReadScope == nil || spec.ReadScope.RequireComplete != tc.complete || spec.ReadScope.Version != 1) {
				t.Fatalf("范围没有传给引擎: %+v", spec)
			}
		})
	}
	var flags readScopeFlags
	_ = flags.Set("资料,正文.md")
	_ = flags.Set("第二份.md")
	if !reflect.DeepEqual([]string(flags), []string{"资料,正文.md", "第二份.md"}) {
		t.Fatal("把文件名内逗号误作分隔符")
	}
}

func TestCLIOutputStylePreservesResumeDefaults(t *testing.T) {
	for _, resume := range []bool{false, true} {
		spec := engine.TaskSpec{Resume: resume}
		if err := applyCLIOutputStyle(&spec, "concise", false); err != nil {
			t.Fatal(err)
		}
		if (spec.OutputStyle == "") != resume {
			t.Fatalf("默认值覆盖恢复或未设置新建: %+v", spec)
		}
		if err := applyCLIOutputStyle(&spec, "detailed", true); err != nil || spec.OutputStyle != "detailed" {
			t.Fatalf("显式选择没有交给引擎核对: %+v %v", spec, err)
		}
	}
	if err := applyCLIOutputStyle(&engine.TaskSpec{}, "short", true); err == nil {
		t.Fatal("接受了未知输出偏好")
	}
}

func TestCLIReadPageLeavesRestoreDefaultUnspecified(t *testing.T) {
	for _, resume := range []bool{false, true} {
		spec := engine.TaskSpec{Resume: resume}
		if err := applyCLIReadPageBytes(&spec, 8000, false); err != nil || spec.ReadPageBytes != 0 {
			t.Fatalf("隐式默认覆盖了任务配置: %+v %v", spec, err)
		}
		if err := applyCLIReadPageBytes(&spec, 16000, true); err != nil || spec.ReadPageBytes != 16000 {
			t.Fatalf("显式档位没有传给引擎核对: %+v %v", spec, err)
		}
	}
	if err := applyCLIReadPageBytes(&engine.TaskSpec{}, 8192, true); err == nil {
		t.Fatal("接受了未约定的读取档位")
	}
}

func TestCLIReportCannotIncreasePermissionsOrOverrideResume(t *testing.T) {
	for _, tc := range []struct {
		name, target            string
		mode                    tools.Mode
		spec                    engine.TaskSpec
		analysis, plan, invalid bool
	}{
		{name: "默认workspace报告", target: "优化汇总.md", mode: tools.ModeWorkspace},
		{name: "子目录", target: "docs/report.md", mode: tools.ModeWorkspace},
		{name: "不能提高只读权限", target: "report.md", mode: tools.ModeReadOnly, invalid: true},
		{name: "不能可信执行", target: "report.md", mode: tools.ModeTrusted, invalid: true},
		{name: "不能回答任务", target: "report.md", mode: tools.ModeWorkspace, spec: engine.TaskSpec{Kind: engine.TaskAnswer}, invalid: true},
		{name: "不能分析模式", target: "report.md", mode: tools.ModeWorkspace, analysis: true, invalid: true},
		{name: "不能规划", target: "report.md", mode: tools.ModeWorkspace, plan: true, invalid: true},
		{name: "不能验收命令", target: "report.md", mode: tools.ModeWorkspace, spec: engine.TaskSpec{VerifyCommand: "true"}, invalid: true},
		{name: "不能修改恢复目标", target: "report.md", mode: tools.ModeWorkspace, spec: engine.TaskSpec{Resume: true}, invalid: true},
		{name: "拒绝绝对路径", target: "/tmp/report.md", mode: tools.ModeWorkspace, invalid: true},
		{name: "拒绝向上跳转", target: "a/../report.md", mode: tools.ModeWorkspace, invalid: true},
		{name: "拒绝代码路径", target: "main.go", mode: tools.ModeWorkspace, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			err := applyCLIReport(&spec, tc.target, tc.mode, tc.analysis, tc.plan)
			if (err != nil) != tc.invalid {
				t.Fatalf("报告选择错误: %+v %v", spec, err)
			}
			if !tc.invalid && (spec.Report == nil || !spec.Report.CreateOnly || spec.Report.Format != "evidence_v1" || spec.Kind != engine.TaskChange) {
				t.Fatalf("报告契约没有准确生成: %+v", spec)
			}
		})
	}
}
