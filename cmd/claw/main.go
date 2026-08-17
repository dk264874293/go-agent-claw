package main

import (
	"context"
    "flag"
    "fmt"
    "log"
    "os"
    // "net/http"
    // "sync"
    "path/filepath"
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
    // 1. 命令行参数解析
    promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
    workDirPtr := flag.String("dir", ".", "Agent 运行的工作区目录路径 (默认为当前目录)")
    sessionPtr := flag.String("session", "cli_default_session", "指定会话 ID，支持断点续传")
    flag.Parse()

    if *promptPtr == "" {
        fmt.Println("用法: go-tiny-claw -prompt \"你的任务描述\" [-dir /path/to/workdir] [-session session_id]")
        os.Exit(1)
    }

    // 解析工作区绝对路径
    workDir, err := filepath.Abs(*workDirPtr)
    if err != nil {
        log.Fatalf("解析工作区路径失败: %v", err)
    }

    fmt.Println("==================================================")
    fmt.Printf("🚀 启动 go-agent-claw CLI 引擎...\n")
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

	// 3. 初始化真实的 Tool Registry 
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
    readOnlyRegistry.Register(tools.NewBashTool(workDir))

    // 开启 EnableThinking = true
    eng := engine.NewAgentEngine(trackedProvider, registry, false, true)

    // 【全息追踪装配】：初始化链路追踪 Root Span
    ctx, rootSpan := observability.StartSpan(context.Background(), "CLI.TaskRun")
    rootSpan.AddAttribute("Prompt", *promptPtr)
    defer func() {
        rootSpan.EndSpan()
        _ = observability.ExportTraceToFile(rootSpan, workDir, sess.ID)
    }()

    // 5. 初始化彩色终端输出器
    reporter := engine.NewTerminalReporter()
    fmt.Printf("\n🎯 收到任务: %s\n\n", *promptPtr)
    // 将用户的 Prompt 压入 Session 记忆
    sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})

    // 6. 发起冲锋：启动 Main Loop！
    err = eng.Run(ctx, sess, reporter)
    if err != nil {
        log.Fatalf("\n💥 引擎运行崩溃: %v", err)
    }
    fmt.Println("\n==================================================")
    fmt.Printf("✨ 任务圆满结束。总耗时: %v\n", time.Since(rootSpan.StartTime))
    fmt.Printf("💰 Session 累计消耗: $%.6f | Token: Input %d, Output %d\n",
        sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
    fmt.Println("==================================================")

    // 为主智能体准备全功能注册表
    // mainRegistry := tools.NewRegistry()
    // mainRegistry.Register(tools.NewReadFileTool(workDir))
    // mainRegistry.Register(tools.NewWriteFileTool(workDir))
    // mainRegistry.Register(tools.NewBashTool(workDir))
    // mainRegistry.Register(tools.NewEditFileTool(workDir))


	// // 引擎本身变成无状态的，它不绑定 WorkDir（仅适用于本讲演示）
    // eng := engine.NewAgentEngine(trackedProvider, mainRegistry, false,false) 

    // mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))



}
