package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type TestCase struct {
	ID             string
	Name           string
	SetupScript    string
	TaskPrompt     string
	ValidateScript string
	MaxTurns       int
}

type TestResult struct {
	TestCaseID string
	Passed     bool
	Status     engine.TaskStatus
	DurationMs int64
	ErrorMsg   string
	Usage      ctxpkg.UsageSnapshot
	RunUsage   engine.Usage
}

type BenchmarkRunner struct {
	modelName string
	provider  provider.LLMProvider
}

func NewBenchmarkRunner(model string, llm provider.LLMProvider) *BenchmarkRunner {
	return &BenchmarkRunner{modelName: model, provider: llm}
}

// RunSuite 执行显式启动的真实模型评测，并返回是否全部通过。
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) bool {
	log.Printf("评测模型 %s · 用例数 %d", b.modelName, len(testcases))
	passed := 0
	for _, tc := range testcases {
		result := b.runSingleTest(ctx, tc, b.provider)
		if result.Passed {
			passed++
		}
		cost := "未知"
		if result.Usage.CostKnown {
			cost = fmt.Sprintf("¥%.6f", result.Usage.CostCNY)
		}
		log.Printf("%s · %s · %dms · 模型 %d 次 · 工具 %d 次 · 费用 %s · %s",
			tc.ID, result.Status, result.DurationMs, result.RunUsage.ModelCalls, result.RunUsage.ToolCalls, cost, result.ErrorMsg)
		if ctx.Err() != nil {
			break
		}
	}
	log.Printf("评测通过 %d/%d", passed, len(testcases))
	return len(testcases) > 0 && passed == len(testcases)
}

func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase, p provider.LLMProvider) (result TestResult) {
	result.TestCaseID, result.Status = tc.ID, engine.Failed
	started := time.Now()
	defer func() { result.DurationMs = time.Since(started).Milliseconds() }()
	if tc.ValidateScript == "" {
		result.ErrorMsg = "评测缺少验收脚本"
		return result
	}
	workDir, err := os.MkdirTemp("", "claw-bench-")
	if err != nil {
		result.ErrorMsg = err.Error()
		return result
	}
	defer os.RemoveAll(workDir)
	if tc.SetupScript != "" {
		setup, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := exec.CommandContext(setup, "bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			result.ErrorMsg = fmt.Sprintf("夹具初始化失败: %v\n%s", err, output)
			return result
		}
	}

	session := ctxpkg.NewSession(tc.ID, workDir)
	tracked := observability.NewCostTracker(p, b.modelName, session)
	registry := engine.NewWorkspaceRegistry(workDir)
	eng := engine.NewAgentEngine(tracked, registry, engine.EngineOptions{
		Mode: tools.ModeTrusted, Budget: engine.Budget{MaxTurns: tc.MaxTurns},
	})
	registry.Register(tools.NewSubagentTool(eng, engine.NewExplorerRegistry(workDir), nil))
	run, err := eng.Run(ctx, session, engine.TaskSpec{
		Kind: engine.TaskChange, Goal: tc.TaskPrompt, VerifyCommand: tc.ValidateScript,
	}, nil)
	result.Status, result.Passed = run.Status, run.Status == engine.Verified
	result.Usage, result.RunUsage = session.UsageSnapshot(), run.Usage
	if err != nil {
		result.ErrorMsg = err.Error()
	} else if !result.Passed {
		result.ErrorMsg = run.Reason
	}
	return result
}
