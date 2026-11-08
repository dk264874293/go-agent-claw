package observability

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestBenchmarkWriteOptionsKeepReportSeparateFromVerification(t *testing.T) {
	for _, tc := range []TestCase{
		{ReportPath: "report.md", Analysis: true},
		{ReportPath: "report.md", Delegation: "off"},
		{ReportPath: "report.md", ValidateScript: "true"},
		{ReportPath: "../report.md"},
		{ReportPath: "report.txt"},
		{ReadPageBytes: 8192, ValidateScript: "true"},
	} {
		if _, _, err := benchmarkTaskSpec(tc); err == nil {
			t.Fatalf("接受了无效配置: %+v", tc)
		}
	}
	spec, mode, err := benchmarkTaskSpec(TestCase{ReportPath: "report.md", TaskPrompt: "生成汇总", ReadPageBytes: 16000})
	if err != nil || mode != tools.ModeWorkspace || spec.Mode != mode || spec.Kind != engine.TaskChange || spec.ReadPageBytes != 16000 || spec.Report == nil || spec.VerifyCommand != "" {
		t.Fatalf("报告评测错误地启用验收或改变配置: %+v %s %v", spec, mode, err)
	}
	for _, tc := range []TestCase{{Analysis: true, ReadPageBytes: 16000}, {ValidateScript: "true", ReadPageBytes: 16000}} {
		spec, _, err := benchmarkTaskSpec(tc)
		if err != nil || spec.ReadPageBytes != 16000 {
			t.Fatalf("既有入口没有传递档位: %+v %v", spec, err)
		}
	}
}

func TestBenchmarkReadScopeAndStylePreserveTaskLane(t *testing.T) {
	for _, tc := range []TestCase{{Analysis: true}, {ReportPath: "result.md"}, {ValidateScript: "true"}} {
		tc.ReadScope = &engine.ReadScopeSpec{Paths: []string{"docs"}, NameGlob: "*.md", RequireComplete: true}
		tc.OutputStyle = "detailed"
		spec, mode, err := benchmarkTaskSpec(tc)
		if err != nil || spec.ReadScope == nil || spec.ReadScope.Version != 1 || !spec.ReadScope.RequireComplete || spec.OutputStyle != "detailed" {
			t.Fatalf("评测读取契约未传播: %+v %s %v", spec, mode, err)
		}
		if tc.Analysis && mode != tools.ModeReadOnly || tc.ReportPath != "" && mode != tools.ModeWorkspace || tc.ValidateScript != "" && mode != tools.ModeTrusted {
			t.Fatal("资料范围改变了执行模式")
		}
	}
	for _, tc := range []TestCase{
		{Analysis: true, ReadScope: &engine.ReadScopeSpec{Paths: []string{"../secret"}}},
		{Analysis: true, ReadScope: &engine.ReadScopeSpec{Paths: []string{"docs"}, NameGlob: "**"}},
		{Analysis: true, OutputStyle: "short"},
	} {
		if _, _, err := benchmarkTaskSpec(tc); err == nil {
			t.Fatalf("接受了非法评测读取配置: %+v", tc)
		}
	}
	spec, _, err := benchmarkTaskSpec(TestCase{Analysis: true})
	if err != nil || spec.ReadScope != nil || spec.OutputStyle != "concise" {
		t.Fatalf("默认行为错误: %+v %v", spec, err)
	}
}
