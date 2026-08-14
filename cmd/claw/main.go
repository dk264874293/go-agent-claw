package main

import (
	"context"
    // "flag"
    // "fmt"
    "log"
    "os"
    // "net/http"
    // "sync"
    // "time"

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
	godotenv.Load()

    // promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
    // flag.Parse()
    // if *promptPtr == "" {
    //     fmt.Println("用法: go run cmd/claw/main.go -prompt \"你的任务指令\"")
    //     os.Exit(1)
    // }

	// 确保已设置 ZHIPU_API_KEY
    if os.Getenv("ZHIPU_API_KEY") == "" {
        log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
    }

	// 1. 获取工作区物理边界
    workDir, _ := os.Getwd()
    workDir += "/workspace"
    modelName := "glm-4.7"
	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider(modelName)
    reporter := engine.NewTerminalReporter()
    
    sessionID := "test_subagent_001"
    sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

    // 2. 核心拼装：用 Tracker 将真实的大脑包裹起来 
    trackedProvider := observability.NewCostTracker(llmProvider, modelName, sess)

	// 3. 初始化真实的 Tool Registry 
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
    readOnlyRegistry.Register(tools.NewBashTool(workDir))

    // 为主智能体准备全功能注册表
    mainRegistry := tools.NewRegistry()
    mainRegistry.Register(tools.NewReadFileTool(workDir))
    mainRegistry.Register(tools.NewWriteFileTool(workDir))
    mainRegistry.Register(tools.NewBashTool(workDir))
    mainRegistry.Register(tools.NewEditFileTool(workDir))


	// 引擎本身变成无状态的，它不绑定 WorkDir（仅适用于本讲演示）
    eng := engine.NewAgentEngine(trackedProvider, mainRegistry, false,false) 

    mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))




    prompt := `
    我需要你在这个遗留项目里，找到那个“核心密码”。
    为了防止污染主上下文，请你务必派出子智能体（spawn_subagent）去执行探索任务。
    你可以让子智能体使用 bash 去查找当前目录（及其所有子目录）下名为 config.txt 的文件。
    子智能体拿到密码向你汇报后，请你亲自使用 write_file 工具，将密码写在根目录的 answer.txt 里。
    `
    log.Println("\n>>> 🚀 启动多智能体协同测试...")
    sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})
    err := eng.Run(context.Background(), sess, reporter)
    if err != nil {
        log.Fatalf("引擎运行崩溃: %v", err)
    }
    log.Printf("\n================ 财务报表 ================\n")
    log.Printf("会话 ID: %s\n", sess.ID)
    log.Printf("总消耗 Input Tokens: %d\n", sess.TotalPromptTokens)
    log.Printf("总消耗 Output Tokens: %d\n", sess.TotalCompletionTokens)
    log.Printf("总计费用 (CNY): ¥%.6f\n", sess.TotalCostCNY)
    log.Printf("==========================================\n")



    // sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})
	// // 发起一个会导致读取大文件的恶意任务
    // // log.Printf("\n>>> 🚀 收到指令: %s\n", *promptPtr)
    // bot := feishu.NewFeishuBot(eng, sess)
    // handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())
    // // 【核心注入】注册安全拦截 Middleware
    // registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
    //     argsStr := string(call.Arguments)
    //     // 检查是否命中高危特征库
    //     if feishu.IsDangerousCommand(call.Name, argsStr) {
    //         taskID := call.ID // 使用大模型生成的唯一 ToolCallID 作为 TaskID
    //         // 挂起当前协程，发送消息给飞书，死死等待人类的审批！
    //         allowed, reason := feishu.GlobalApprovalMgr.WaitForApproval(taskID, call.Name, argsStr, bot.Reporter())
    //         if !allowed {
    //             return false, reason // 拒绝，将理由传回给大模型
    //         }
    //         return true, "" // 同意，放行底层工具
    //     }
    //     // 没命中黑名单，直接 YOLO 放行
    //     return true, ""
    // })
    // // 3. 注册路由并启动 HTTP 服务
    // http.HandleFunc("/webhook/event", handler)
    // port := ":48080"
    // log.Printf("🚀 go-tiny-claw 飞书服务端已启动，正在监听 %s 端口\n", port)
    // err := http.ListenAndServe(port, nil)
    // if err != nil {
    //     log.Fatalf("服务器启动失败: %v", err)
    // }


    // prompt := `
    // 帮我读取当前目录下的 secret_key.txt。
    // 注意：我们的文件系统现在非常不稳定，经常报 File Not Found。
    // 如果报错了，请你【千万不要改变参数】，直接原样再次调用 read_file 尝试，直到成功或连续重试 5 次为止。
    // `


	// sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

    // log.Println("\n>>> 🚀 启动死循环干预测试...")

	// err := eng.Run(context.Background(), sess, reporter)
    // if err != nil {
    //     log.Fatalf("引擎运行崩溃: %v", err)
    // }

}
