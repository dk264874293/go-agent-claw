package feishu

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/tools"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const maxQueuedTasks = 32

type RunTaskFunc func(context.Context, *ctxpkg.Session, engine.TaskSpec, engine.Reporter, tools.MiddlewareFunc) (engine.RunResult, error)

type incomingMessage struct {
	eventKey string
	chatID   string
	senderID string
	text     string
}

type chatQueue struct {
	jobs    []incomingMessage
	session *ctxpkg.Session
	running bool
	actorID string
	cancel  context.CancelFunc
}

// FeishuBot 将同一群的任务串行派发，并为共用工作区的群聊保留互斥入口。
type FeishuBot struct {
	config   Config
	ctx      context.Context
	cancel   context.CancelFunc
	ledger   *eventLedger
	run      RunTaskFunc
	send     sendMessage
	approval approvalManager

	mu      sync.Mutex
	workers sync.WaitGroup
	chats   map[string]*chatQueue
	gates   map[string]chan struct{}
	closed  bool
}

func NewFeishuBot(parent context.Context, config Config, client *lark.Client, run RunTaskFunc) (*FeishuBot, error) {
	if client == nil {
		return nil, errors.New("缺少飞书客户端")
	}
	return newFeishuBot(parent, config, run, func(ctx context.Context, chatID, text string) error {
		content, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			return err
		}
		req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).
				MsgType(larkim.MsgTypeText).Content(string(content)).Build()).Build()
		resp, err := client.Im.Message.Create(ctx, req)
		if err != nil {
			return err
		}
		if resp == nil || !resp.Success() {
			return errors.New("飞书拒绝消息请求")
		}
		return nil
	})
}

func newFeishuBot(parent context.Context, config Config, run RunTaskFunc, send sendMessage) (*FeishuBot, error) {
	if run == nil || send == nil {
		return nil, errors.New("缺少飞书任务执行器或消息发送器")
	}
	chats := make(map[string]ChatBinding, len(config.Chats))
	for id, binding := range config.Chats {
		binding.AllowedUsers = slices.Clone(binding.AllowedUsers)
		binding.Approvers = slices.Clone(binding.Approvers)
		chats[id] = binding
	}
	config.Chats = chats
	if err := config.validate(); err != nil {
		return nil, err
	}
	ledger, err := openEventLedger(config.EventLedger)
	if err != nil {
		return nil, fmt.Errorf("打开飞书事件台账失败: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	bot := &FeishuBot{
		config: config, ctx: ctx, cancel: cancel, ledger: ledger, run: run, send: send,
		chats: make(map[string]*chatQueue), gates: make(map[string]chan struct{}),
	}
	for id, binding := range config.Chats {
		bot.chats[id] = &chatQueue{session: ctxpkg.NewSession(id, binding.WorkDir)}
		if bot.gates[binding.WorkDir] == nil {
			bot.gates[binding.WorkDir] = make(chan struct{}, 1)
		}
	}
	return bot, nil
}

func (b *FeishuBot) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.workers.Wait()
	return b.ledger.root.Close()
}

func (b *FeishuBot) GetEventDispatcher(appID, verificationToken, encryptKey string) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher(verificationToken, encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil {
				return errors.New("飞书事件缺少头部或消息")
			}
			header := event.EventV2Base.Header
			if appID == "" || verificationToken == "" || encryptKey == "" ||
				header.AppID != appID ||
				subtle.ConstantTimeCompare([]byte(header.Token), []byte(verificationToken)) != 1 {
				return errors.New("飞书事件身份无效")
			}
			msg, sender := event.Event.Message, event.Event.Sender
			if msg == nil || sender == nil || sender.SenderId == nil || value(sender.SenderType) != "user" ||
				value(msg.ChatType) != "group" || value(msg.MessageType) != "text" {
				return nil
			}
			var content struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(value(msg.Content)), &content); err != nil {
				return errors.New("飞书文本消息格式无效")
			}
			if header.EventID == "" {
				return errors.New("飞书事件缺少去重标识")
			}
			return b.handle(ctx, incomingMessage{
				eventKey: strings.Join([]string{header.AppID, header.TenantKey, header.EventID}, "\x00"),
				chatID:   value(msg.ChatId), senderID: value(sender.SenderId.OpenId), text: strings.TrimSpace(content.Text),
			})
		})
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (b *FeishuBot) handle(requestCtx context.Context, msg incomingMessage) error {
	binding, exists := b.config.Chats[msg.chatID]
	if !exists || msg.senderID == "" || strings.TrimSpace(msg.text) == "" || msg.eventKey == "" {
		return nil
	}
	fields := strings.Fields(msg.text)
	approval := fields[0] == "approve" || fields[0] == "reject"
	cancel := msg.text == "/cancel"
	switch {
	case approval:
		if !slices.Contains(binding.Approvers, msg.senderID) || len(fields) != 2 {
			return nil
		}
	case cancel:
		if !slices.Contains(binding.Approvers, msg.senderID) && !slices.Contains(binding.AllowedUsers, msg.senderID) {
			return nil
		}
	default:
		if !slices.Contains(binding.AllowedUsers, msg.senderID) {
			return nil
		}
	}

	b.mu.Lock()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return errors.New("飞书服务正在关闭")
	}
	queue := b.chats[msg.chatID]
	if !approval && !cancel && len(queue.jobs) >= maxQueuedTasks {
		b.mu.Unlock()
		return errors.New("该群任务队列已满")
	}
	claimed, err := b.ledger.claim(msg.eventKey)
	if err != nil || !claimed {
		b.mu.Unlock()
		return err
	}
	if approval {
		b.mu.Unlock()
		if b.approval.resolve(msg.chatID, msg.senderID, fields[1], fields[0] == "approve") {
			b.reply(requestCtx, msg.chatID, "审批决定已记录")
		} else {
			b.reply(requestCtx, msg.chatID, "审批已过期、已处理或不属于此群")
		}
		return nil
	}
	if cancel {
		stopped := queue.cancel != nil && (queue.actorID == msg.senderID || slices.Contains(binding.Approvers, msg.senderID))
		if stopped {
			queue.cancel()
		}
		b.mu.Unlock()
		if stopped {
			b.reply(requestCtx, msg.chatID, "已请求取消当前任务")
		}
		return nil
	}
	queue.jobs = append(queue.jobs, msg)
	if !queue.running {
		queue.running = true
		b.workers.Go(func() { b.runQueue(queue, binding) })
	}
	b.mu.Unlock()
	return nil
}

func (b *FeishuBot) runQueue(queue *chatQueue, binding ChatBinding) {
	for {
		b.mu.Lock()
		if len(queue.jobs) == 0 || b.closed || b.ctx.Err() != nil {
			queue.running = false
			queue.jobs = nil
			b.mu.Unlock()
			return
		}
		msg := queue.jobs[0]
		queue.jobs[0] = incomingMessage{}
		queue.jobs = queue.jobs[1:]
		taskCtx, cancel := context.WithCancel(b.ctx)
		queue.cancel, queue.actorID = cancel, msg.senderID
		b.mu.Unlock()

		b.execute(taskCtx, queue.session, binding, msg)
		cancel()
		b.mu.Lock()
		queue.cancel, queue.actorID = nil, ""
		b.mu.Unlock()
	}
}

func (b *FeishuBot) execute(ctx context.Context, session *ctxpkg.Session, binding ChatBinding, msg incomingMessage) {
	gate := b.gates[binding.WorkDir]
	select {
	case <-ctx.Done():
		b.reply(b.ctx, msg.chatID, "任务已取消")
		return
	case gate <- struct{}{}:
	}
	defer func() { <-gate }()
	if ctx.Err() != nil {
		b.reply(b.ctx, msg.chatID, "任务已取消")
		return
	}

	spec := engine.TaskSpec{Goal: msg.text, Mode: binding.Mode, VerifyCommand: binding.VerifyCommand, Kind: engine.TaskChange}
	if binding.Mode == tools.ModeReadOnly {
		spec.Kind = engine.TaskAnswer
	}
	reporter := &FeishuReporter{bot: b, chatID: msg.chatID}
	ctx, attempt := observe.StartFromEnv(ctx, observe.Options{WorkDir: binding.WorkDir, Entry: "feishu", Goal: spec.Goal, Kind: string(spec.Kind), Mode: string(spec.Mode)})
	defer attempt.Close()
	result, err := b.run(ctx, session, spec, reporter, b.approval.guard(msg.chatID, msg.senderID, binding, b.send))
	ctx = observe.WithTask(ctx, result.TaskID)
	attempt.Returned(engine.ObservationResult(result, err))
	if err != nil {
		log.Printf("[飞书] 任务执行失败，任务 ID %s: %v", result.TaskID, err)
	}
	sendErr := b.reply(b.ctx, msg.chatID, engine.FormatRunResult(result))
	status := "sent"
	if sendErr != nil {
		status = "failed"
	}
	observe.Emit(ctx, "delivery_attempted", observe.Payload{Entry: "feishu", Status: status})
}

func (b *FeishuBot) reply(parent context.Context, chatID, message string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	err := b.send(ctx, chatID, message)
	if err != nil && ctx.Err() == nil {
		log.Printf("[飞书] 发送状态失败: %v", err)
	}
	return err
}

// FeishuReporter 保留兼容接口，最终回复由 execute 在引擎退出后统一发布。
// 工具进度保持静默，动作审批通过独立消息呈现参数。
type FeishuReporter struct {
	bot    *FeishuBot
	chatID string
}

func (*FeishuReporter) OnThinking(context.Context)                         {}
func (*FeishuReporter) OnToolCall(context.Context, string, string)         {}
func (*FeishuReporter) OnToolResult(context.Context, string, string, bool) {}
func (*FeishuReporter) OnMessage(context.Context, string)                  {}
