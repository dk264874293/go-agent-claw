/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-14 17:31:01
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-14 21:13:16
 * @FilePath: /go-agent-claw/internal/eval/benchmark.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/eval/benchmark.go
package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/config"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// TestCase 定义了一个需要 Agent 去完成并验证的独立任务
type TestCase struct {
	ID             string // 用例唯一标识
	Name           string // 用例名称
	SetupScript    string // 【可选】在 Agent 运行前执行的 bash 脚本 (用于初始化靶机代码)
	TaskPrompt     string // 发送给 Agent 的任务指令
	ValidateScript string // 【核心】在 Agent 运行结束后执行的 bash 校验脚本。exit 0 视为成功，其他视为失败
	MaxTurns       int    // 允许 Agent 尝试的最大轮数 (超时算失败)
}

// TestResult 存放单次跑分结果
type TestResult struct {
	TestCaseID string
	Passed     bool
	CostLabel  string
	DurationMs int64
	ErrorMsg   string
}

type BenchmarkRunner struct {
	modelName string
	config    config.Config
	options   runpkg.Options
}

func NewBenchmarkRunner(model string) *BenchmarkRunner {
	cfg := config.Defaults()
	cfg.Provider, cfg.Model = "zhipu-openai", model
	cfg.BaseURL = provider.DefaultURL(cfg.Provider)
	opts, _ := cfg.RunOptions()
	return &BenchmarkRunner{modelName: model, config: cfg, options: opts}
}

func NewBenchmarkRunnerWithConfig(cfg config.Config) (*BenchmarkRunner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	opts, err := cfg.RunOptions()
	if err != nil {
		return nil, err
	}
	return &BenchmarkRunner{modelName: cfg.Model, config: cfg, options: opts}, nil
}

// RunSuite 执行一组评测集，并返回跑分报告
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) {
	log.Println("==================================================")
	log.Printf("🚀 启动自动化 Harness Benchmark 评估... | 模型: %s\n", b.modelName)
	log.Println("==================================================")

	var results []TestResult
	passedCount := 0

	for _, tc := range testcases {
		log.Printf("\n>>> ⏳ 正在执行用例 [%s]: %s\n", tc.ID, tc.Name)

		res := b.runSingleTest(ctx, tc)
		results = append(results, res)

		if res.Passed {
			passedCount++
			log.Printf(">>> ✅ 用例 [%s] 测试通过! | 耗时: %dms | 费用: %s\n", tc.ID, res.DurationMs, res.CostLabel)
		} else {
			log.Printf(">>> ❌ 用例 [%s] 测试失败! | 费用: %s | 错误: %s\n", tc.ID, res.CostLabel, res.ErrorMsg)
		}
	}

	// 打印终极报表
	log.Println("\n================ 🏆 跑分终极报告 ================")
	log.Printf("总用例数: %d | 成功数: %d | 成功率: %.2f%%\n", len(testcases), passedCount, float64(passedCount)/float64(len(testcases))*100)
	log.Println("费用按用例展示；未知或不同币种不合并")
	log.Println("==================================================")
}

func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	startTime := time.Now()

	// 1. 为每个用例创建独立工作目录；这不构成进程级沙箱。
	workDir, _ := os.Getwd()
	workDir += fmt.Sprintf("/workspace/%s_%d", tc.ID, time.Now().Unix())
	_ = os.MkdirAll(workDir, 0755)

	// 2. (可选) 执行 Setup 脚本准备靶机代码
	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if err := cmd.Run(); err != nil {
			return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: "靶机 Setup 失败"}
		}
	}

	// 3. 组装具备打点能力 (Tracker) 的引擎
	realProvider, providerErr := provider.NewConfigured(b.config.Provider, b.config.Model, b.config.BaseURL)
	if providerErr != nil {
		return TestResult{TestCaseID: tc.ID, ErrorMsg: providerErr.Error(), CostLabel: "未知"}
	}
	session := ctxpkg.NewSession(tc.ID, workDir) // 为本次跑分单独建一个 Session 记账
	trackedProvider := observability.NewCostTracker(realProvider, b.modelName, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, b.config.Thinking, b.config.PlanMode)

	// 4. 让 Agent 开始干活

	// 我们传入一个空的 reporter 屏蔽普通日志，防止刷屏
	opts := b.options
	if tc.MaxTurns > 0 && tc.MaxTurns < opts.MaxTurns {
		opts.MaxTurns = tc.MaxTurns
	}
	result, err := eng.Submit(ctx, session, tc.TaskPrompt, nil, opts)

	if err != nil {
		return TestResult{TestCaseID: tc.ID, Passed: false, CostLabel: session.UsageSnapshot().CostLabel(), ErrorMsg: fmt.Sprintf("Agent 运行失败 | RunID: %s | 原因: %s | 错误: %v | Trace: %s", result.RunID, result.Reason, err, result.TraceError)}
	}

	// 5. 【核心断言】Agent 跑完了，我们来验收成果！
	cmd := exec.Command("bash", "-c", tc.ValidateScript)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()

	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return TestResult{
			TestCaseID: tc.ID,
			Passed:     false,
			CostLabel:  session.UsageSnapshot().CostLabel(),
			DurationMs: duration,
			ErrorMsg:   fmt.Sprintf("验证脚本执行失败: %s", string(out)),
		}
	}

	return TestResult{
		TestCaseID: tc.ID,
		Passed:     true,
		CostLabel:  session.UsageSnapshot().CostLabel(),
		DurationMs: duration,
	}
}
