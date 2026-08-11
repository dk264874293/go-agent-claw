package main

import (
	"context"
    "log"
    "os"
    "sync"
    "time"

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
	workDir += "/workspace"

	// 2. 初始化LLM
    llmProvider := provider.NewZhipuOpenAIProvider("glm-4.7")

	// 3. 初始化真实的 Tool Registry 
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool("/tmp/project_front"))

	// 引擎本身变成无状态的，它不绑定 WorkDir（仅适用于本讲演示）
    eng := engine.NewAgentEngine(llmProvider, registry, false) 
    reporter := engine.NewTerminalReporter()

	var wg sync.WaitGroup

	wg.Add(1)
    go func() {
        defer wg.Done()
        sessionA := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_front_001", "/tmp/project_front")
        // 回合 1：获取机密
        log.Println("\n>>> 🙋‍♂️ [Session A / Turn 1]: 帮我看看 README.md 里记录了什么密钥？")
        sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "帮我看看 README.md 里记录了什么密钥？"})
        _ = eng.Run(context.Background(), sessionA, reporter)
        // 故意制造大量“废话”对话，刷掉记忆 (假设 Working Memory Limit=6)
        for i := 0; i < 6; i++ {
            sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "这只是一句闲聊占位符。"})
            sessionA.Append(schema.Message{Role: schema.RoleAssistant, Content: "好的，收到闲聊。"})
        }
        // 回合 2：验证记忆截断 (此时第一轮的密钥已经被挤出 Working Memory 了！)
        log.Println("\n>>> 🙋‍♂️ [Session A / Turn 2]: 请直接告诉我，刚才第一轮你查到的那个密钥是什么？")
        sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "请直接告诉我，刚才第一轮你查到的那个密钥是什么？不准调用工具！"})
        _ = eng.Run(context.Background(), sessionA, reporter)
    }()
	// ================= 模拟并发场景 2：飞书后端群 =================
    wg.Add(1)
    go func() {
        defer wg.Done()
        // 稍微错开一点时间发起请求
        time.Sleep(1 * time.Second)
        sessionB := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_back_002", "/tmp/project_back")
        log.Println("\n>>> 🙋‍♂️ [Session B]: 别人查到了一个密钥，你这里能看到吗？")
        sessionB.Append(schema.Message{Role: schema.RoleUser, Content: "别人查到了一个密钥，你这里能看到吗？不准调用工具！"})
        _ = eng.Run(context.Background(), sessionB, reporter)
    }()
    wg.Wait()

	// // 4. 挂载极简工具集
    // registry.Register(tools.NewReadFileTool(workDir))
    // registry.Register(tools.NewWriteFileTool(workDir))
    // registry.Register(tools.NewBashTool(workDir))
	// registry.Register(tools.NewEditFileTool(workDir))

	// // 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	// eng := engine.NewAgentEngine(llmProvider, registry, workDir, true)

	// reporter := engine.NewTerminalReporter()
    // prompt := `
    // 我需要在当前目录下新建一个 ping.go，提供一个简单的 http ping 接口。
    // 写完之后，帮我把代码用 git 提交一下。
    // `
    // err := eng.Run(context.Background(), prompt, reporter)
    // if err != nil {
    //     log.Fatalf("引擎运行崩溃: %v", err)
    // }


}
