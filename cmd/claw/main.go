package main

import (
	"context"
    // "flag"
    // "fmt"
    "log"
    "os"
    // "sync"
    // "time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/engine"
	// "github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/dk264874293/go-agent-claw/internal/engine"
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
	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider("glm-4.7")

	// 3. 初始化真实的 Tool Registry 
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
    registry.Register(tools.NewWriteFileTool(workDir))
    registry.Register(tools.NewBashTool(workDir))
    registry.Register(tools.NewEditFileTool(workDir))

	// 引擎本身变成无状态的，它不绑定 WorkDir（仅适用于本讲演示）
    eng := engine.NewAgentEngine(llmProvider, registry, false,false) 
    reporter := engine.NewTerminalReporter()

	sessionID := "test_recovery_001"
    sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	// 发起一个会导致读取大文件的恶意任务
    // log.Printf("\n>>> 🚀 收到指令: %s\n", *promptPtr)
    prompt := `
	我当前目录下有一个 auth.go 文件。
	请修改 auth.go 中的 login 函数。
	请直接使用 edit_file 工具替换下面的代码块，将判断条件改为同时允许"admin"、"root"和"guest"三种用户登录：

    // 鉴权入口函数
    func login(user string) bool {
        // 检查用户名
        if user == "admin" {
            return true
        }
        return false
    }
`


	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	err := eng.Run(context.Background(), sess, reporter)
    if err != nil {
        log.Fatalf("引擎运行崩溃: %v", err)
    }

}
