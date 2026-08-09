package main

import (
	"context"
	"log"
	// "net/http"
	"os"

	// "github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	// "github.com/dk264874293/go-agent-claw/internal/feishu"
	"github.com/dk264874293/go-agent-claw/internal/provider"
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
	workDir += "/workspace"

	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider("glm-4.7")

	// 3. 初始化真实的 Tool Registry 
	registry := tools.NewRegistry()

	// 4. 挂载极简工具集
    registry.Register(tools.NewReadFileTool(workDir))
    registry.Register(tools.NewWriteFileTool(workDir))
    registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, true)

	reporter := engine.NewTerminalReporter()
    prompt := `
    我需要在当前目录下新建一个 ping.go，提供一个简单的 http ping 接口。
    写完之后，帮我把代码用 git 提交一下。
    `
    err := eng.Run(context.Background(), prompt, reporter)
    if err != nil {
        log.Fatalf("引擎运行崩溃: %v", err)
    }

	// 初始化飞书 Bot 调度器
    // bot := feishu.NewFeishuBot(eng)
    // handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())
    // //  注册路由并启动 HTTP 服务
    // http.HandleFunc("/webhook/event", handler)
    // port := ":48080"
    // log.Printf("🚀 go-tiny-claw 飞书服务端已启动，正在监听 %s 端口\n", port)
	// err := http.ListenAndServe(port, nil) 
	// if err != nil { 
	// 	log.Fatalf("服务器启动失败: %v", err) 
	// }

}
