package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	// "net/http"
	// "sync"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	// "github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	// "github.com/dk264874293/go-agent-claw/internal/feishu"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	// 加载 .env 文件
	godotenv.Overload()
	// 确保已设置 api key
	if os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatal("请先导出 OPENAI_API_KEY 环境变量")
	}
	// 1. 命令行参数解析：-prompt 留空时进入交互对话模式 (REPL)
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述 (留空则进入交互对话模式)")
	workDirPtr := flag.String("dir", ".", "Agent 运行的工作区目录路径 (默认为当前目录)")
	sessionPtr := flag.String("session", "cli_default_session", "指定会话 ID，支持断点续传")
	flag.Parse()

	interactive := *promptPtr == ""

	// 解析工作区绝对路径
	workDir, err := filepath.Abs(*workDirPtr)
	if err != nil {
		log.Fatalf("解析工作区路径失败: %v", err)
	}

	mode := "单任务模式"
	if interactive {
		mode = "交互对话模式"
	}
	fmt.Println("==================================================")
	fmt.Printf("🚀 启动 go-agent-claw CLI 引擎 (%s)...\n", mode)
	fmt.Printf("📁 锁定工作区: %s\n", workDir)
	fmt.Println("==================================================")

	var realProvider provider.LLMProvider
	modelName := "gpt-5.6-sol"
	realProvider = provider.NewOpenAIProvider(modelName)

	// 获取持久化 Session
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)
	// 【全息监控装配】：用 Cost Tracker 将真实大脑包裹起来
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	// 3. 初始化工具与执行层
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 为 Subagent (探路者) 准备受限的只读注册表：只能看，不能改
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
	readOnlyRegistry.Register(tools.NewBashTool(workDir))

	// 关闭慢思考 (EnableThinking=false)：每轮只发一次带工具的 LLM 调用；
	// 置 true 则每轮先做一次无工具规划再行动，LLM 调用次数翻倍
	eng := engine.NewAgentEngine(trackedProvider, registry, false, true)

	// 【信号处理】：Ctrl+C / SIGTERM 取消根 context。
	// Provider 链路透传 ctx，正在执行的 LLM 请求会被立即打断，随后走统一收尾
	// （结束 Root Span、导出聚合 trace、打印总账单）。
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 【全息追踪装配】：Root Span 覆盖整个进程生命周期。
	// 交互模式下每轮的 Agent.Run 会作为子 Span 级联进来，退出时导出完整聚合链路；
	// 引擎内部每轮结束还会自行导出一份单轮 trace，两者互补。
	spanName := "CLI.TaskRun"
	if interactive {
		spanName = "CLI.ReplSession"
	}
	ctx, rootSpan := observability.StartSpan(rootCtx, spanName)
	rootSpan.AddAttribute("SessionID", sess.ID)
	rootSpan.AddAttribute("Interactive", interactive)
	if !interactive {
		rootSpan.AddAttribute("Prompt", *promptPtr)
	}
	procStart := time.Now()
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, workDir, sess.ID)
	}()

	// 5. 初始化彩色终端输出器
	reporter := engine.NewTerminalReporter()

	// 【Subagent 装配】：为主智能体挂上"派出探路者"的委派能力。
	// 注册必须发生在首次 eng.Run 之前 —— 引擎每轮通过 registry.GetAvailableTools() 动态取工具。
	registry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))

	if interactive {
		// 6. 交互对话模式：读一行、跑一轮，直到用户主动退出
		runREPL(ctx, eng, sess, reporter, modelName)
		printSummary("👋 会话结束", procStart, sess)
	} else {
		// 6. 发起冲锋：单任务模式，行为与历史版本保持一致
		fmt.Printf("\n🎯 收到任务: %s\n\n", *promptPtr)
		// 将用户的 Prompt 压入 Session 记忆
		sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})

		if err := eng.Run(ctx, sess, reporter); err != nil {
			// 不再 log.Fatalf：让 defer 的 trace 导出有机会执行
			fmt.Printf("\n💥 引擎运行崩溃: %v\n", err)
		}
		printSummary("✨ 任务圆满结束", procStart, sess)
	}
}

// runREPL 启动交互对话循环：每读入一行用户输入，就唤醒一次引擎。
// 多轮上下文由 Session 历史天然承接 —— 引擎的 Run 是可重入的，
// 每次 Run 从 GetWorkingMemory 取最近上下文，跑到模型不再调用工具即返回。
// 斜杠命令（/help /cost 等）在本地拦截处理：不进 Session、不调用 LLM、不计费。
func runREPL(ctx context.Context, eng *engine.AgentEngine, sess *ctxpkg.Session, reporter engine.Reporter, modelName string) {
	// 独立 goroutine 负责 stdin：主循环同时 select 退出信号与用户输入，互不阻塞
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		// Scanner 默认 64KB 上限，粘贴长文本会报 token too long，扩容到 1MB
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines) // Ctrl+D (EOF)
	}()

	// 上一轮消耗增量（/cost 展示用，轮末由 submitTurn 更新）
	var (
		hasLastTurn         bool
		lastDeltaCost       float64
		lastDeltaPrompt     int
		lastDeltaCompletion int
	)

	// 斜杠命令表：返回 false 表示请求退出 REPL
	commands := map[string]func(args string) bool{
		"/help": func(string) bool {
			fmt.Println("📖 可用命令：")
			fmt.Println("  /help    显示本帮助")
			fmt.Println("  /exit    退出会话（等效 exit / quit / Ctrl+D）")
			fmt.Println("  /cost    查看累计消耗与上一轮增量")
			fmt.Println("  /status  查看会话概览")
			fmt.Println("  /clear   清空对话上下文（保留账单统计）")
			fmt.Println()
			fmt.Println(`🧵 输入 """ 进入多行输入模式：逐行粘贴内容，再单独一行 """ 结束提交`)
			fmt.Println()
			return true
		},
		"/exit": func(string) bool { return false },
		"/cost": func(string) bool {
			fmt.Printf("💰 Session 累计消耗: ¥%.6f | Token: Input %d, Output %d\n",
				sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
			if hasLastTurn {
				fmt.Printf("   上一轮增量: ¥%.6f | Token: Input %d, Output %d\n\n",
					lastDeltaCost, lastDeltaPrompt, lastDeltaCompletion)
			} else {
				fmt.Println("   上一轮增量: 尚无已完成的轮次")
				fmt.Println()
			}
			return true
		},
		"/status": func(string) bool {
			fmt.Println("📊 会话概览：")
			fmt.Printf("   会话 ID: %s\n", sess.ID)
			fmt.Printf("   工作区: %s\n", sess.WorkDir)
			fmt.Printf("   模型: %s | 思考模式: %v | 计划模式: %v\n", modelName, eng.EnableThinking, eng.PlanMode)
			fmt.Printf("   历史消息: %d 条\n", sess.MessageCount())
			fmt.Printf("   累计消耗: ¥%.6f | Token: Input %d, Output %d\n\n",
				sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
			return true
		},
		"/clear": func(string) bool {
			n := sess.MessageCount()
			sess.ResetHistory()
			fmt.Printf("🧹 已清空 %d 条对话上下文（累计消耗 ¥%.6f 保留）\n\n", n, sess.TotalCostCNY)
			return true
		},
	}

	// submitTurn 提交一轮任务：压入 Session、唤醒引擎、打印轮末统计。
	// 返回 true 表示收到退出信号，调用方应立即结束 REPL。
	submitTurn := func(content, display string) (exit bool) {
		fmt.Printf("\n🎯 收到任务: %s\n\n", display)
		// 将本轮用户输入压入 Session 记忆，多轮对话的连续性由此而来
		sess.Append(schema.Message{Role: schema.RoleUser, Content: content})

		costBefore := sess.TotalCostCNY
		promptBefore := sess.TotalPromptTokens
		completionBefore := sess.TotalCompletionTokens

		turnStart := time.Now()
		err := eng.Run(ctx, sess, reporter)

		// 失败轮也可能已产生若干次成功的 LLM 调用（已计入累计账单），
		// 因此增量统计不区分成败，保证 /cost 的增量与累计值口径一致
		hasLastTurn = true
		lastDeltaCost = sess.TotalCostCNY - costBefore
		lastDeltaPrompt = sess.TotalPromptTokens - promptBefore
		lastDeltaCompletion = sess.TotalCompletionTokens - completionBefore

		if err != nil {
			if ctx.Err() != nil {
				// 被信号打断：本轮作废，直接进入收尾
				fmt.Println("\n⚠️ 收到退出信号，正在收尾...")
				return true
			}
			// 可恢复错误（如 API 抖动）：打印后继续对话，绝不杀掉整个会话
			fmt.Printf("💥 本轮执行失败: %v（会话已保留，可继续输入）\n\n", err)
			return false
		}
		fmt.Printf("✅ 本轮耗时: %v | 本轮消耗: ¥%.6f (In %d / Out %d tk) | Session 累计: ¥%.6f\n\n",
			time.Since(turnStart).Round(time.Millisecond), lastDeltaCost, lastDeltaPrompt,
			lastDeltaCompletion, sess.TotalCostCNY)
		return false
	}

	fmt.Println("💡 输入任务描述开始对话；输入 /help 查看命令；exit / quit / Ctrl+D 退出，Ctrl+C 中断退出")

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

// printSummary 统一收尾：打印总耗时与 Session 累计账单
func printSummary(title string, procStart time.Time, sess *ctxpkg.Session) {
	fmt.Println("\n==================================================")
	fmt.Printf("%s。总耗时: %v\n", title, time.Since(procStart).Round(time.Millisecond))
	fmt.Printf("💰 Session 累计消耗: ¥%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}
