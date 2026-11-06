package observability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type analysisEntryProvider struct{ calls int }

func (p *analysisEntryProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	return &schema.Message{Role: schema.RoleAssistant, Content: "无证据普通答复"}, nil
}

func TestBenchmarkAnalysisRejectsOracleBeforeSetupAndProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unexpected")
	provider := &analysisEntryProvider{}
	runner := NewBenchmarkRunner("脚本模型", provider)
	result := runner.runSingleTest(context.Background(), TestCase{ID: "只读分析", Analysis: true, SetupScript: "touch '" + path + "'", ValidateScript: "true"}, provider)
	if result.Passed || provider.calls != 0 || !strings.Contains(result.ErrorMsg, "分析评测不能设置验收脚本") {
		t.Fatalf("错误的分析评测执行了模型或验收: %+v calls=%d", result, provider.calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("无效配置已执行夹具命令: %v", err)
	}
}

func TestBenchmarkAnalysisUsesReadonlyAnswerAndPreservesLegacy(t *testing.T) {
	analysis, mode, err := benchmarkTaskSpec(TestCase{Analysis: true, TaskPrompt: "核对字段"})
	if err != nil || !analysis.Analysis || analysis.Kind != engine.TaskAnswer || analysis.Mode != tools.ModeReadOnly || mode != tools.ModeReadOnly || analysis.VerifyCommand != "" {
		t.Fatalf("分析任务配置错误: %+v %s %v", analysis, mode, err)
	}
	legacy, mode, err := benchmarkTaskSpec(TestCase{TaskPrompt: "修复代码", ValidateScript: "go test ./parser"})
	if err != nil || legacy.Analysis || legacy.Kind != engine.TaskChange || mode != tools.ModeTrusted || legacy.VerifyCommand != "go test ./parser" {
		t.Fatalf("既有评测语义被改变: %+v %s %v", legacy, mode, err)
	}
	if _, _, err := benchmarkTaskSpec(TestCase{}); err == nil {
		t.Fatal("既有修改评测仍须提供验收脚本")
	}
}
