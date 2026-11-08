package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type TestCase struct {
	ID             string
	Name           string
	SetupScript    string
	TaskPrompt     string
	ValidateScript string
	MaxTurns       int
	Analysis       bool
	Quality        bool
	// nil继承命令行默认；false显式关闭，保持旧用例默认不变。
	DeliveryReview *bool
	// 显式普通任务通道；不设置时保留旧Trusted/ValidateScript等入口。
	Mode                  tools.Mode
	Kind                  engine.TaskKind
	Delegation            string
	ReadPageBytes         int
	ReadFeatures          *engine.ReadFeatureSpec
	ReadScope             *engine.ReadScopeSpec
	OutputStyle           string
	ReportPath            string
	RequestDiagnostics    string `json:"request_diagnostics,omitempty"`
	RequestDiagnosticsDir string `json:"request_diagnostics_dir,omitempty"`
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
	diagnostics, err := requestdiag.ResolveConfig(tc.RequestDiagnostics, tc.RequestDiagnosticsDir)
	if err != nil {
		result.ErrorMsg = "请求诊断配置无效：" + err.Error()
		return result
	}
	spec, mode, err := benchmarkTaskSpec(tc)
	if err != nil {
		result.ErrorMsg = err.Error()
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
		Mode: mode, Budget: engine.Budget{MaxTurns: tc.MaxTurns},
	})
	registry.Register(tools.NewSubagentTool(eng, engine.NewExplorerRegistry(workDir), nil))
	options := eng.ObservationOptions(workDir, "bench", spec)
	options.Model = b.modelName
	ctx, attempt := observe.StartFromEnv(ctx, options)
	ctx, diagnosticAttempt := engine.StartRequestDiagnostics(ctx, diagnostics)
	defer engine.CloseRequestDiagnostics(diagnosticAttempt, attempt)
	run, err := eng.Run(ctx, session, spec, nil)
	ctx = observe.WithTask(ctx, run.TaskID)
	attempt.Returned(engine.ObservationResult(run, err))
	result.Status, result.Passed = run.Status, run.Status == engine.Verified
	if spec.Kind == engine.TaskAnswer {
		// 只证明报告符合引擎证据契约，不能冒充结论语义验收。
		result.Passed = run.Status == engine.Answered && (!spec.Analysis || run.Analysis != nil)
	}
	if spec.Report != nil {
		// 这里只验收发布与引用契约，报告结论须由外部 oracle 独立评估。
		result.Passed = run.Status == engine.NeedsVerification && run.Report != nil && run.Report.Status == "published"
	}
	if tc.Mode == tools.ModeWorkspace && spec.Kind == engine.TaskChange && spec.Report == nil {
		// 仅表示普通修改的引擎交付达到预期，不能作为运行或语义验收。
		result.Passed = run.Status == engine.NeedsVerification
	}
	result.Usage, result.RunUsage = session.UsageSnapshot(), run.Usage
	if err != nil {
		result.ErrorMsg = err.Error()
	} else if !result.Passed {
		result.ErrorMsg = run.Reason
	}
	return result
}

func benchmarkTaskSpec(tc TestCase) (engine.TaskSpec, tools.Mode, error) {
	spec, mode, err := benchmarkTaskSpecBase(tc)
	if err != nil {
		return spec, mode, err
	}
	spec.ReadFeatures = tc.ReadFeatures
	if err := engine.NormalizeReadFeatures(&spec); err != nil {
		return engine.TaskSpec{}, "", err
	}
	if tc.DeliveryReview != nil && *tc.DeliveryReview {
		spec.Mode = mode
		spec.DeliveryReview = &engine.DeliveryReviewSpec{Version: 1}
		if err := engine.NormalizeDeliveryReviewSpec(&spec); err != nil {
			return engine.TaskSpec{}, "", err
		}
	}
	return spec, mode, nil
}

func benchmarkTaskSpecBase(tc TestCase) (engine.TaskSpec, tools.Mode, error) {
	if tc.Quality && (!tc.Analysis && tc.ReportPath == "" || tc.Delegation != "" && tc.Delegation != "off" || tc.ValidateScript != "") {
		return engine.TaskSpec{}, "", fmt.Errorf("质量评测必须使用分析或报告，不能组合验收脚本或子任务委派")
	}
	reading := engine.TaskSpec{ReadScope: tc.ReadScope, OutputStyle: tc.OutputStyle}
	if err := engine.NormalizeReadScope(&reading); err != nil {
		return engine.TaskSpec{}, "", err
	}
	if reading.OutputStyle == "" {
		reading.OutputStyle = "concise"
	}
	if reading.OutputStyle != "concise" && reading.OutputStyle != "detailed" {
		return engine.TaskSpec{}, "", fmt.Errorf("输出篇幅必须为 concise 或 detailed")
	}
	if tc.ReadPageBytes != 0 && tc.ReadPageBytes != 8000 && tc.ReadPageBytes != 16000 {
		return engine.TaskSpec{}, "", fmt.Errorf("读取档位必须为 8000 或 16000 字节")
	}
	if tc.Mode != "" {
		if tc.Analysis || tc.Quality || tc.ReportPath != "" || tc.ValidateScript != "" {
			return engine.TaskSpec{}, "", fmt.Errorf("显式普通评测不能组合分析、报告、质量模式或引擎验收脚本")
		}
		kind := tc.Kind
		if kind == "" {
			if tc.Mode == tools.ModeReadOnly {
				kind = engine.TaskAnswer
			} else if tc.Mode == tools.ModeWorkspace {
				kind = engine.TaskChange
			}
		}
		if tc.Mode != tools.ModeReadOnly && tc.Mode != tools.ModeWorkspace || tc.Mode == tools.ModeReadOnly && kind != engine.TaskAnswer || tc.Mode == tools.ModeWorkspace && kind != engine.TaskChange {
			return engine.TaskSpec{}, "", fmt.Errorf("普通评测仅支持readonly + answer或workspace + change")
		}
		if tc.Delegation != "" && (tc.Mode != tools.ModeReadOnly || tc.Delegation != "auto" && tc.Delegation != "off" && tc.Delegation != "on") {
			return engine.TaskSpec{}, "", fmt.Errorf("委派策略仅适用于只读回答，取auto、off或on")
		}
		return engine.TaskSpec{Mode: tc.Mode, Kind: kind, Goal: tc.TaskPrompt, Delegation: tc.Delegation, ReadPageBytes: tc.ReadPageBytes, ReadScope: reading.ReadScope, OutputStyle: reading.OutputStyle}, tc.Mode, nil
	}
	if tc.Kind != "" {
		return engine.TaskSpec{}, "", fmt.Errorf("显式任务类型须同时指定普通评测Mode")
	}
	if tc.ReportPath != "" {
		if tc.Analysis || tc.Delegation != "" && !(tc.Quality && tc.Delegation == "off") || tc.ValidateScript != "" {
			return engine.TaskSpec{}, "", fmt.Errorf("证据报告评测不能与分析、委派策略或引擎验收脚本组合；语义验收须独立执行")
		}
		if !filepath.IsLocal(tc.ReportPath) || filepath.Ext(tc.ReportPath) != ".md" || strings.ContainsRune(tc.ReportPath, 0) {
			return engine.TaskSpec{}, "", fmt.Errorf("报告目标必须是工作区内的相对 Markdown 路径")
		}
		for _, part := range strings.Split(filepath.ToSlash(tc.ReportPath), "/") {
			if part == ".." {
				return engine.TaskSpec{}, "", fmt.Errorf("报告目标不能包含上级目录跳转")
			}
		}
		spec := engine.TaskSpec{Kind: engine.TaskChange, Mode: tools.ModeWorkspace, Goal: tc.TaskPrompt, ReadPageBytes: tc.ReadPageBytes, ReadScope: reading.ReadScope, OutputStyle: reading.OutputStyle, Report: &engine.ReportSpec{
			Version: 1, Format: "evidence_v1", TargetPath: filepath.ToSlash(filepath.Clean(tc.ReportPath)), CreateOnly: true,
		}}
		if tc.Quality {
			spec.Quality = &engine.QualitySpec{Version: 1}
		}
		return spec, tools.ModeWorkspace, engine.NormalizeQualitySpec(&spec)
	}
	if tc.Analysis || tc.Delegation != "" {
		if tc.ValidateScript != "" {
			return engine.TaskSpec{}, "", fmt.Errorf("分析评测不能设置验收脚本；只读回答的结论正确性须独立判定")
		}
		delegation := tc.Delegation
		if tc.Quality {
			delegation = "off"
		}
		if delegation == "" {
			delegation = "auto"
		}
		if delegation != "auto" && delegation != "off" && delegation != "on" {
			return engine.TaskSpec{}, "", fmt.Errorf("委派策略必须是 auto、off 或 on")
		}
		spec := engine.TaskSpec{Kind: engine.TaskAnswer, Mode: tools.ModeReadOnly, Goal: tc.TaskPrompt, Analysis: tc.Analysis, Delegation: delegation, ReadPageBytes: tc.ReadPageBytes, ReadScope: reading.ReadScope, OutputStyle: reading.OutputStyle}
		if tc.Quality {
			spec.Quality = &engine.QualitySpec{Version: 1}
		}
		return spec, tools.ModeReadOnly, engine.NormalizeQualitySpec(&spec)
	}
	if tc.ValidateScript == "" {
		return engine.TaskSpec{}, "", fmt.Errorf("评测缺少验收脚本")
	}
	return engine.TaskSpec{Kind: engine.TaskChange, Goal: tc.TaskPrompt, VerifyCommand: tc.ValidateScript, ReadPageBytes: tc.ReadPageBytes, ReadScope: reading.ReadScope, OutputStyle: reading.OutputStyle}, tools.ModeTrusted, nil
}
