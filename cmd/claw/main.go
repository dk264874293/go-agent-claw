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
	// 确保已设置 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
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
	modelName := "glm-4.7-flashx"
	realProvider = provider.NewZhipuOpenAIProvider(modelName)

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

	// 主agent
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
		runREPL(ctx, eng, sess, reporter)
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
func runREPL(ctx context.Context, eng *engine.AgentEngine, sess *ctxpkg.Session, reporter engine.Reporter) {
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

	fmt.Println("💡 输入任务描述开始对话；输入 exit / quit 或 Ctrl+D 退出，Ctrl+C 中断当前任务并退出")

	for {
		var line string
		select {
		case <-ctx.Done():
			fmt.Println("\n⚠️ 收到退出信号，正在收尾...")
			return
		case l, ok := <-lines:
			if !ok {
				return // EOF：用户按下了 Ctrl+D
			}
			line = l
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		switch strings.ToLower(input) {
		case "exit", "quit":
			return
		}

		fmt.Printf("\n🎯 收到任务: %s\n\n", input)
		// 将本轮用户输入压入 Session 记忆，多轮对话的连续性由此而来
		sess.Append(schema.Message{Role: schema.RoleUser, Content: input})

		turnStart := time.Now()
		if err := eng.Run(ctx, sess, reporter); err != nil {
			if ctx.Err() != nil {
				// 被信号打断：本轮作废，直接进入收尾
				fmt.Println("\n⚠️ 收到退出信号，正在收尾...")
				return
			}
			// 可恢复错误（如 API 抖动）：打印后继续对话，绝不杀掉整个会话
			fmt.Printf("💥 本轮执行失败: %v（会话已保留，可继续输入）\n\n", err)
			continue
		}
		fmt.Printf("✅ 本轮耗时: %v | Session 累计消耗: ¥%.6f | Token: Input %d, Output %d\n\n",
			time.Since(turnStart).Round(time.Millisecond), sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	}
}

// printSummary 统一收尾：打印总耗时与 Session 累计账单
func printSummary(title string, procStart time.Time, sess *ctxpkg.Session) {
	fmt.Println("\n==================================================")
	fmt.Printf("%s。总耗时: %v\n", title, time.Since(procStart).Round(time.Millisecond))
	fmt.Printf("💰 Session 累计消耗: ¥%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}
