package main

import (
	"context"
	"log"
	"os"

	"github.com/dk264874293/go-agent-claw/internal/engine"
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

	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider("glm-4.7-flash")

	// 3. 初始化真实的 Tool Registry 
	registry := tools.NewRegistry()

	// 4. 挂载极简工具集
    registry.Register(tools.NewReadFileTool(workDir))
    registry.Register(tools.NewWriteFileTool(workDir))
    registry.Register(tools.NewBashTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 设定测试任务
    prompt := ` 请帮我执行以下操作： 
	1. 用 bash 查看一下我当前电脑的 Go 版本。 
	2. 帮我写一个简单的 helloworld.go 文件，输出 "Hello, go-tiny-claw!"。 
	3. 用 bash 编译并运行这个 go 文件，确认它能正常工作。 `

	err := eng.Run(context.Background(), prompt)

	if err != nil {
		log.Fatalf("引擎崩溃: %v", err)
	}
}
