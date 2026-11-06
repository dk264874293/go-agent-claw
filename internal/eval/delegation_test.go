package observability

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestBenchmarkReadonlyDelegationConfiguration(t *testing.T) {
	for _, value := range []string{"auto", "off", "on"} {
		spec, mode, err := benchmarkTaskSpec(TestCase{Delegation: value, TaskPrompt: "分析材料"})
		if err != nil || spec.Kind != engine.TaskAnswer || mode != tools.ModeReadOnly || spec.Analysis || spec.Delegation != value {
			t.Fatalf("只读评测配置错误: %+v %s %v", spec, mode, err)
		}
	}
	if _, _, err := benchmarkTaskSpec(TestCase{Delegation: "wrong"}); err == nil {
		t.Fatal("非法委派值未拒绝")
	}
	if _, _, err := benchmarkTaskSpec(TestCase{Delegation: "on", ValidateScript: "true"}); err == nil {
		t.Fatal("只读委派允许验收脚本")
	}
	spec, _, err := benchmarkTaskSpec(TestCase{Analysis: true})
	if err != nil || spec.Delegation != "auto" {
		t.Fatal("分析任务未采用自动委派默认值")
	}
}
