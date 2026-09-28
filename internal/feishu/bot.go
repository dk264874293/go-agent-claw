// internal/feishu/bot.go
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ==========================================
// 1. Context 传递机制：解决并发 Reporter 的提取
// ==========================================
// reporterKey 定义 Context 中存放 Reporter 的专属键
type reporterKey struct{}

// ContextWithReporter 将专属的 Reporter 封入上下文
func ContextWithReporter(ctx context.Context, r engine.Reporter) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFromContext 供底层的 Middleware 提取专属的 Reporter 发送审批卡片
func ReporterFromContext(ctx context.Context) engine.Reporter {
	if r, ok := ctx.Value(reporterKey{}).(engine.Reporter); ok {
		return r
	}
	return nil
}

// ==========================================
// 2. 飞书 Bot 核心调度器
// ==========================================
// AgentEngineFactory 允许每次收到消息时，根据 Session 动态创建引擎
type AgentEngineFactory func(session *ctxpkg.Session) *engine.AgentEngine
type FeishuBot struct {
	client    *lark.Client
	appID     string
	appSecret string
	workDir   string             // 保存从入口传来的工作区路径
	factory   AgentEngineFactory // 替换掉原来的单一 engine 引用
}

func NewFeishuBotWithFactory(factory AgentEngineFactory, workDir string) *FeishuBot {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}
	client := lark.NewClient(appID, appSecret)
	return &FeishuBot{
		client:    client,
		appID:     appID,
		appSecret: appSecret,
		workDir:   workDir, // 接收外部传入的路径
		factory:   factory,
	}
}

func (b *FeishuBot) GetEventDispatcher() *dispatcher.EventDispatcher {
	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verifyToken := os.Getenv("FEISHU_VERIFY_TOKEN")
	handler := dispatcher.NewEventDispatcher(verifyToken, encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			contentStr := *event.Event.Message.Content
			contentStr = strings.TrimPrefix(contentStr, `{"text":"`)
			contentStr = strings.TrimSuffix(contentStr, `"}`)
			chatId := *event.Event.Message.ChatId
			log.Printf("[Feishu] 收到会话 %s 消息: %s\n", chatId, contentStr)
			// 拦截人工审批的特殊口令，并唤醒挂起的 Registry 协程
			if strings.HasPrefix(contentStr, "approve ") {
				taskID := strings.TrimPrefix(contentStr, "approve ")
				taskID = strings.TrimSpace(taskID)
				GlobalApprovalMgr.ResolveApproval(taskID, true, "人类管理员已批准操作")
				log.Printf("[Feishu] 会话 %s: ✅ 已为您批准任务 %s", chatId, taskID)
				return nil
			}
			if strings.HasPrefix(contentStr, "reject ") {
				taskID := strings.TrimPrefix(contentStr, "reject ")
				taskID = strings.TrimSpace(taskID)
				GlobalApprovalMgr.ResolveApproval(taskID, false, "人类管理员认为该操作存在极高风险，已无情拒绝")
				log.Printf("[Feishu] 会话 %s: 🚫 已拒绝任务 %s", chatId, taskID)
				return nil
			}
			// 普通对话先同步登记会话票据，再异步执行，避免调度改变接收顺序。
			b.handleAgentRun(chatId, contentStr)
			return nil
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			// 消息已读事件，静默忽略
			return nil
		})
	return handler
}

func (b *FeishuBot) handleAgentRun(chatId string, prompt string) {
	reporter := &FeishuReporter{client: b.client, chatId: chatId}
	done := b.submitAgentRun(context.Background(), chatId, prompt, reporter)
	go func() {
		outcome := <-done
		if outcome.Err != nil {
			reporter.sendMsg(context.Background(), fmt.Sprintf("❌ Agent 运行失败 | RunID: %s | 原因: %s | 错误: %v", outcome.Result.RunID, outcome.Result.Reason, outcome.Err))
		}
	}()
}

// submitAgentRun 同步登记会话票据，再异步运行，避免 goroutine 调度改变接收顺序。
func (b *FeishuBot) submitAgentRun(ctx context.Context, chatID, prompt string, reporter engine.Reporter) <-chan engine.Outcome {
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(chatID, b.workDir)
	eng := b.factory(session)
	return eng.Start(ContextWithReporter(ctx, reporter), session, prompt, reporter, runpkg.Options{MaxTurns: 20, Timeout: 5 * time.Minute})
}

// ==========================================
// FeishuReporter: 将引擎的输出格式化后发给飞书
// ==========================================
type FeishuReporter struct {
	client *lark.Client
	chatId string
}

func (r *FeishuReporter) sendMsg(ctx context.Context, text string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	textContent := map[string]string{
		"text": text,
	}
	contentBytes, _ := json.Marshal(textContent)
	contentStr := string(contentBytes)
	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(r.chatId).
			MsgType(larkim.MsgTypeText).
			Content(contentStr).
			Build()).
		Build()
	_, _ = r.client.Im.Message.Create(ctx, msgReq)
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	r.sendMsg(ctx, "🤔 模型正在慢思考 (Thinking)...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(ctx, fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}
func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(ctx, fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result))
	} else {
		r.sendMsg(ctx, fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}
func (r *FeishuReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(ctx, content)
}

// 确保 FeishuReporter 实现了 Reporter 接口
var _ engine.Reporter = (*FeishuReporter)(nil)
