package main

import (
	"context"
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

	// 确保已设置 ZHIPU_API_KEY
    if os.Getenv("ZHIPU_API_KEY") == "" {
        log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
    }

	// 1. 获取工作区物理边界
    workDir, _ := os.Getwd()

	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider("glm-4.7")

	// 3. 初始化真实的 Tool Registry 
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
    registry.Register(tools.NewWriteFileTool(workDir))
    registry.Register(tools.NewBashTool(workDir))

	// 引擎本身变成无状态的，它不绑定 WorkDir（仅适用于本讲演示）
    eng := engine.NewAgentEngine(llmProvider, registry, false) 
    reporter := engine.NewTerminalReporter()

	sessionID := "test_oom_protection_001"
    sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	// 发起一个会导致读取大文件的恶意任务
    prompt := `
    请帮我执行以下三个步骤：
    1. 使用 bash 执行 echo "开始排查日志"
    2. 使用 read_file 工具读取当前目录下的巨大文件 mock_log.txt
    3. 使用 bash 执行 date 命令获取当前时间，并告诉我任务全部完成。
    `

	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	err := eng.Run(context.Background(), sess, reporter)
    if err != nil {
        log.Fatalf("引擎运行崩溃: %v", err)
    }

}
