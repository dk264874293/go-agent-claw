// cmd/agentops/main.go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/dk264874293/go-agent-claw/internal/config"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/executor"
	"github.com/dk264874293/go-agent-claw/internal/feishu"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/tools"
	"github.com/joho/godotenv"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
)

func main() {
	// 加载 .env 文件
	_ = godotenv.Load()
	log.Println("🚀 正在启动 go-tiny-claw AgentOps 飞书服务端...")

	if os.Getenv("FEISHU_APP_ID") == "" || os.Getenv("FEISHU_APP_SECRET") == "" {
		log.Fatal("请先设置飞书应用凭证")
	}

	// 1. 设定监控的物理工作区
	workDir, _ := os.Getwd()
	workDir += "/workspace"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("无法创建工作区: %v", err)
	}

	// 2. 初始化底层大脑与注册表
	defaults := config.Defaults()
	defaults.Provider, defaults.Model = "zhipu-openai", "glm-4.5-air"
	defaults.Permissions, defaults.ApprovalMode = "read-only", "deny"
	cfg, err := config.Parse(nil, os.Getenv, defaults)
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}
	opts, err := cfg.RunOptions()
	if err != nil {
		log.Fatalf("预算配置错误: %v", err)
	}
	modelName := cfg.Model
	llmProvider, err := provider.NewConfigured(cfg.Provider, cfg.Model, cfg.BaseURL)
	if err != nil {
		log.Fatalf("Provider 配置错误: %v", err)
	}

	if cfg.ApprovalMode != "deny" {
		log.Fatal("飞书入口尚无主体鉴权审批，请设置 CLAW_APPROVAL=deny")
	}
	shell, err := executor.NewDocker(cfg.ExecutorOptions())
	if err != nil {
		log.Fatalf("执行后端配置错误: %v", err)
	}
	registry := tools.NewWorkspaceRegistry(workDir, cfg.ToolPolicy(), nil, shell, cfg.AllowNetwork)
	log.Println("已启用权限检查；写入和进程执行没有受信审批时拒绝。")

	// 4. 动态 Factory 组装器：保证高并发调用的物理独立性与账单准确追踪
	engineFactory := func(session *ctxpkg.Session) *engine.AgentEngine {
		// 让 Tracker 绑定当前特定用户的 Session 账本
		trackedProvider := observability.NewCostTracker(llmProvider, modelName, session)

		// 返回一个新组装的 Engine 实例
		return engine.NewAgentEngine(trackedProvider, registry, cfg.Thinking, cfg.PlanMode)
	}

	// 5. 初始化飞书 Bot 调度中心
	bot := feishu.NewFeishuBotWithFactory(engineFactory, workDir)
	bot.RunOptions = opts
	handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())

	// 6. 注册 Webhook 路由并启动 HTTP Server
	http.HandleFunc("/webhook/event", handler)

	port := ":48080"
	log.Printf("📡 Webhook 服务已启动，正在监听端口 %s，请配置 ngrok...\n", port)

	err = http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
