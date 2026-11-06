package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/runtimeenv"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func main() { os.Exit(runCLI()) }

func runCLI() int {
	prompt := flag.String("prompt", "", "单次任务目标；留空进入交互模式")
	channelName := flag.String("provider", "auto", "模型渠道：auto、openai、zhipu、deepseek，auto 按模型选择")
	modelName := flag.String("model", provider.DefaultModel, "模型名称")
	dir := flag.String("dir", ".", "工作区目录")
	sessionID := flag.String("session", "cli_default_session", "会话 ID")
	mode := flag.String("mode", "workspace", "执行模式：readonly、workspace、trusted")
	kind := flag.String("task-kind", "", "任务类型：answer 或 change；默认按执行模式推断")
	verify := flag.String("verify", "", "用户指定的验收命令，仅 trusted 模式可执行")
	resume := flag.String("resume", "", "恢复指定任务 ID")
	plan := flag.Bool("plan", false, "使用有依赖的结构化计划")
	analysis := flag.Bool("analysis", false, "显式启用带证据引用的只读分析报告；恢复沿用原任务设置")
	delegation := flag.String("delegation", "", "只读回答的委派策略：auto、off、on；默认auto，恢复沿用原任务设置")
	thinking := flag.Bool("thinking", false, "每轮先执行一次无工具推理")
	maxTurns := flag.Int("max-turns", 0, "主任务最多轮数，默认 30")
	maxModels := flag.Int("max-model-calls", 0, "最多模型调用次数，默认 60")
	maxTools := flag.Int("max-tool-calls", 0, "最多工具尝试次数，默认 120")
	timeout := flag.Duration("timeout", 0, "累计运行时限，默认 10m")
	contextBytes := flag.Int("context-bytes", 300000, "请求材料字节上限")
	flag.Parse()
	modelExplicit := false
	flag.Visit(func(f *flag.Flag) { modelExplicit = modelExplicit || f.Name == "model" })
	requestedModel := *modelName
	if !modelExplicit {
		requestedModel = ""
	}
	channel, model, modelErr := provider.ResolveModelConfig(*channelName, requestedModel)
	if modelErr != nil {
		fmt.Fprintln(os.Stderr, modelErr)
		return 2
	}
	if *mode != "readonly" && *mode != "workspace" && *mode != "trusted" ||
		*kind != "" && *kind != "answer" && *kind != "change" ||
		*verify != "" && *mode != "trusted" ||
		*resume != "" && (*prompt != "" || *verify != "" || *plan || *kind != "") ||
		*maxTurns < 0 || *maxModels < 0 || *maxTools < 0 || *timeout < 0 || *contextBytes < 1 || strings.TrimSpace(*modelName) == "" {
		fmt.Fprintln(os.Stderr, "运行参数无效，请查看 -help。恢复任务时使用原目标与验收配置。")
		return 2
	}
	spec := engine.TaskSpec{ID: *resume, Resume: *resume != "", Goal: *prompt, Kind: engine.TaskKind(*kind), VerifyCommand: *verify}
	if err := applyCLIAnalysis(&spec, *analysis, tools.Mode(*mode)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := applyCLIDelegation(&spec, *delegation, tools.Mode(*mode)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	workDir, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法定位工作区: %v\n", err)
		return 1
	}
	if err := runtimeenv.LoadDotenv(true); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runtimeenv.ApplyDefaults(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	llm, err := provider.NewConfiguredProvider(channel, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *mode == "trusted" {
		fmt.Println("可信本机模式已启用，命令以当前用户权限执行。")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sess := ctxpkg.NewSession(*sessionID, workDir)
	registry := engine.NewWorkspaceRegistry(workDir)
	options := engine.EngineOptions{
		Mode: tools.Mode(*mode), Plan: *plan, EnableThinking: *thinking, MaxContextBytes: *contextBytes,
		Budget: engine.Budget{MaxTurns: *maxTurns, MaxModelCalls: *maxModels, MaxToolCalls: *maxTools, Timeout: *timeout},
	}
	eng := engine.NewAgentEngine(observability.NewCostTracker(llm, model, sess), registry, options)
	reporter := engine.NewTerminalReporter()
	registry.Register(tools.NewSubagentTool(eng, engine.NewExplorerRegistry(workDir), reporter))
	fmt.Printf("工作区 %s · 执行模式 %s\n", workDir, *mode)
	if spec.Analysis {
		fmt.Println("分析模式已启用：重要结论须有有效引用，引用校验不代表结论已经验收。")
	}
	if *resume == "" && tools.Mode(*mode) == tools.ModeReadOnly && (spec.Kind == "" || spec.Kind == engine.TaskAnswer) {
		fmt.Printf("只读委派策略 %s · 小范围任务由根 Agent 执行\n", spec.Delegation)
	}
	if *prompt == "" && *resume == "" {
		runREPL(ctx, eng, sess, reporter, model, options, spec)
		if ctx.Err() != nil {
			return 130
		}
		return 0
	}
	result := submit(ctx, eng, sess, reporter, spec)
	printUsage(sess.UsageSnapshot())
	return result.ExitCode()
}

// applyCLIAnalysis 只影响新建任务；恢复不得覆盖持久任务中的分析选择。
func applyCLIAnalysis(spec *engine.TaskSpec, enabled bool, mode tools.Mode) error {
	if spec.Resume || !enabled {
		return nil
	}
	if mode != tools.ModeReadOnly || spec.Kind != "" && spec.Kind != engine.TaskAnswer || spec.VerifyCommand != "" {
		return fmt.Errorf("分析模式仅支持 readonly + answer，不能设置验收命令。")
	}
	spec.Analysis, spec.Kind = true, engine.TaskAnswer
	return nil
}

// 恢复必须复用账本中的策略和累计准入，不能用启动参数重置。
func applyCLIDelegation(spec *engine.TaskSpec, value string, mode tools.Mode) error {
	if value != "" && value != "auto" && value != "off" && value != "on" {
		return fmt.Errorf("委派策略必须为 auto、off 或 on。")
	}
	if spec.Resume {
		return nil
	}
	applicable := mode == tools.ModeReadOnly && (spec.Kind == "" || spec.Kind == engine.TaskAnswer)
	if value != "" && !applicable {
		return fmt.Errorf("委派策略仅适用于 readonly + answer 任务。")
	}
	if applicable {
		if value == "" {
			value = "auto"
		}
		spec.Delegation = value
	}
	return nil
}

func submit(ctx context.Context, eng *engine.AgentEngine, sess *ctxpkg.Session, reporter engine.Reporter, spec engine.TaskSpec) engine.RunResult {
	ctx, attempt := observe.StartFromEnv(ctx, eng.ObservationOptions(sess.WorkDir, "cli", spec))
	defer attempt.Close()
	result, err := eng.Run(ctx, sess, spec, reporter)
	ctx = observe.WithTask(ctx, result.TaskID)
	attempt.Returned(engine.ObservationResult(result, err))
	_, printErr := fmt.Println(engine.FormatRunResult(result))
	status := "printed"
	if printErr != nil {
		status = "failed"
	}
	observe.Emit(ctx, "delivery_attempted", observe.Payload{Entry: "cli", Status: status})
	if err != nil && err.Error() != result.Reason {
		fmt.Fprintf(os.Stderr, "运行错误: %v\n", err)
	}
	return result
}

func printUsage(usage ctxpkg.UsageSnapshot) {
	cost := "未知"
	if usage.CostKnown {
		cost = fmt.Sprintf("¥%.6f", usage.CostCNY)
	}
	suffix := ""
	if !usage.TokensKnown {
		suffix = "（部分调用未返回用量）"
	}
	fmt.Printf("会话费用 %s · Token 输入 %d / 输出 %d%s\n", cost, usage.PromptTokens, usage.CompletionTokens, suffix)
}

func runREPL(ctx context.Context, eng *engine.AgentEngine, sess *ctxpkg.Session, reporter engine.Reporter, model string, options engine.EngineOptions, spec engine.TaskSpec) {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	nextVerify := spec.VerifyCommand
	var lastUsage *ctxpkg.UsageSnapshot
	commands := map[string]func(string) bool{
		"/help": func(string) bool {
			fmt.Println("命令 /help /exit /cost /status /clear /verify <命令> /resume <任务 ID>")
			fmt.Println(`输入独占一行的 """ 开始或提交多行输入`)
			return true
		},
		"/exit": func(string) bool { return false },
		"/cost": func(string) bool {
			printUsage(sess.UsageSnapshot())
			if lastUsage != nil {
				fmt.Print("上一轮 ")
				printUsage(*lastUsage)
			}
			return true
		},
		"/status": func(string) bool {
			fmt.Printf("会话 %s · 模型 %s · 模式 %s · 历史 %d 条\n", sess.ID, model, options.Mode, sess.MessageCount())
			printUsage(sess.UsageSnapshot())
			return true
		},
		"/clear": func(string) bool {
			sess.ResetHistory()
			fmt.Println("已清空对话上下文，账单与任务记录保留。")
			return true
		},
		"/verify": func(cmd string) bool {
			if options.Mode != tools.ModeTrusted || cmd == "" {
				fmt.Println("请在可信本机模式中使用 /verify <命令>。")
				return true
			}
			nextVerify = cmd
			fmt.Println("已设置下一次提交的验收命令。")
			return true
		},
		"/resume": func(id string) bool {
			if id == "" {
				fmt.Println("请输入 /resume <任务 ID>。")
				return true
			}
			submit(ctx, eng, sess, reporter, engine.TaskSpec{ID: id, Resume: true})
			return ctx.Err() == nil
		},
	}
	submitTurn := func(content, display string) bool {
		fmt.Printf("收到任务 %s\n", display)
		task := spec
		task.Goal, task.VerifyCommand = content, nextVerify
		nextVerify = ""
		started := time.Now()
		before := sess.UsageSnapshot()
		submit(ctx, eng, sess, reporter, task)
		after := sess.UsageSnapshot()
		lastUsage = &ctxpkg.UsageSnapshot{
			PromptTokens:     after.PromptTokens - before.PromptTokens,
			CompletionTokens: after.CompletionTokens - before.CompletionTokens,
			CostCNY:          after.CostCNY - before.CostCNY,
			CostKnown:        before.UnknownCostCalls == after.UnknownCostCalls,
			TokensKnown:      before.UnknownUsageCalls == after.UnknownUsageCalls,
		}
		fmt.Printf("本次运行耗时 %v\n", time.Since(started).Round(time.Millisecond))
		return ctx.Err() != nil
	}
	fmt.Println("输入任务描述开始对话，输入 /help 查看命令。")

	// 多行输入采集状态：以独占一行的 """ 开始与结束
	var (
		collecting bool
		multiBuf   []string
		multiLen   int
	)
	// 多行总长度上限：单行已有 1MB Scanner 缓冲，这里防的是无限行拼接
	const maxMultilineBytes = 512 * 1024

	for {
		if collecting {
			fmt.Print("...> ")
		} else {
			fmt.Print("claw> ")
		}

		var line string
		select {
		case <-ctx.Done():
			fmt.Println("\n⚠️ 收到退出信号，正在收尾...")
			return
		case l, ok := <-lines:
			if !ok {
				if collecting {
					fmt.Println("\n⚠️ 多行输入已取消（EOF），未提交任何内容")
				}
				return
			}
			line = l
		}

		// ---- 多行采集状态机：不解析命令与退出词，逐行原样累积 ----
		if collecting {
			if isMultilineDelimiter(line) {
				content := strings.Join(multiBuf, "\n")
				n := len(multiBuf)
				collecting = false
				multiBuf, multiLen = nil, 0
				if n == 0 || strings.TrimSpace(content) == "" {
					fmt.Println("⚠️ 多行内容为空，已忽略")
					fmt.Println()
					continue
				}
				if submitTurn(content, fmt.Sprintf("[多行输入，共 %d 行，%d 字符]", n, utf8.RuneCountInString(content))) {
					return
				}
			} else {
				multiBuf = append(multiBuf, line)
				multiLen += len(line) + 1
				if multiLen > maxMultilineBytes {
					collecting = false
					multiBuf, multiLen = nil, 0
					fmt.Println("⚠️ 多行内容超过 512KB 上限，本轮输入已丢弃")
					fmt.Println()
				}
			}
			continue
		}

		// ---- 单行模式 ----
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		switch strings.ToLower(input) {
		case "exit", "quit":
			return
		}

		// 斜杠命令：本地拦截，不进 Session、不调用 LLM、不计费
		if strings.HasPrefix(input, "/") {
			name, args := input, ""
			if i := strings.IndexByte(input, ' '); i >= 0 {
				name, args = input[:i], strings.TrimSpace(input[i+1:])
			}
			handler, ok := commands[strings.ToLower(name)]
			if !ok {
				fmt.Printf("未知命令 %s，输入 /help 查看可用命令\n\n", name)
				continue
			}
			if !handler(args) {
				return
			}
			continue
		}

		// 独占一行的 """：开启多行采集
		if isMultilineDelimiter(input) {
			collecting = true
			multiBuf, multiLen = nil, 0
			fmt.Println(`🧵 多行输入模式：逐行粘贴内容，单独一行 """ 结束提交（Ctrl+D 取消）`)
			continue
		}

		if submitTurn(input, input) {
			return
		}
	}
}

// isMultilineDelimiter 判断一行是否为独占一行的多行界定符。
// 允许行首尾空白，但不允许与正文同行，避免误伤正文中的引号。
func isMultilineDelimiter(line string) bool {
	return strings.TrimSpace(line) == `"""`
}
