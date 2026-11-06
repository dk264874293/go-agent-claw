package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("等待模拟飞书事件超时")
		var zero T
		return zero
	}
}

func TestQueueSerializesSharedWorkspaceAndPersistsDedup(t *testing.T) {
	dir := t.TempDir()
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{
		"甲群": {WorkDir: dir, AllowedUsers: []string{"甲"}},
		"乙群": {WorkDir: dir, AllowedUsers: []string{"乙"}},
	}}
	type seen struct{ session, goal string }
	started := make(chan seen, 5)
	permits := make(chan struct{}, 5)
	var active, maximum atomic.Int32
	run := func(ctx context.Context, session *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for prev := maximum.Load(); n > prev && !maximum.CompareAndSwap(prev, n); prev = maximum.Load() {
		}
		started <- seen{session.ID, spec.Goal}
		select {
		case <-ctx.Done():
			return engine.RunResult{Status: engine.Cancelled}, nil
		case <-permits:
			return engine.RunResult{Status: engine.Answered}, nil
		}
	}
	send := func(context.Context, string, string) error { return nil }
	bot, err := newFeishuBot(context.Background(), config, run, send)
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()

	submit := func(ctx context.Context, bot *FeishuBot, message incomingMessage) {
		t.Helper()
		if err := bot.handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	first := incomingMessage{"event-a1", "甲群", "甲", "第一个"}
	second := incomingMessage{"event-a2", "甲群", "甲", "第二个"}
	third := incomingMessage{"event-b1", "乙群", "乙", "第三个"}
	requestCtx, cancel := context.WithCancel(context.Background())
	submit(requestCtx, bot, first)
	cancel() // Webhook 返回后，队列任务仍使用服务生命周期。
	if got := receive(t, started); got != (seen{"甲群", "第一个"}) {
		t.Fatalf("首个任务身份有误: %#v", got)
	}
	submit(context.Background(), bot, second)
	submit(context.Background(), bot, third)
	submit(context.Background(), bot, second)
	submit(context.Background(), bot, incomingMessage{"ignored", "甲群", "陌生人", "不应执行"})

	remaining := map[seen]bool{{"甲群", "第二个"}: true, {"乙群", "第三个"}: true}
	for range 2 {
		permits <- struct{}{}
		got := receive(t, started)
		if !remaining[got] {
			t.Fatalf("任务重复或身份不正确: %#v", got)
		}
		delete(remaining, got)
	}
	permits <- struct{}{}
	if err := bot.Close(); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("同一工作区内同时运行了 %d 个任务", maximum.Load())
	}
	secondBot, err := newFeishuBot(context.Background(), config, run, send)
	if err != nil {
		t.Fatal(err)
	}
	defer secondBot.Close()
	submit(context.Background(), secondBot, second)
	secondBot.mu.Lock()
	running := secondBot.chats["甲群"].running
	secondBot.mu.Unlock()
	if running {
		t.Fatal("服务重启后重复事件重新入队")
	}
}

func TestApprovalWaitBindsChatAndActorAndCancels(t *testing.T) {
	var manager approvalManager
	target := approvalTarget{ChatID: "工作群", ActorID: "发起人", ActionID: "动作", Tool: "bash"}
	ids := make(chan string, 1)
	done := make(chan error, 1)
	notify := func(_ context.Context, id string, _ time.Time) error { ids <- id; return nil }
	go func() { done <- manager.wait(context.Background(), target, []string{"审批人"}, notify) }()
	id := receive(t, ids)
	if manager.resolve("其他群", "审批人", id, true) || manager.resolve("工作群", "发起人", id, true) {
		t.Fatal("跨群或无权人员批准被接受")
	}
	if !manager.resolve("工作群", "审批人", id, true) || manager.resolve("工作群", "审批人", id, true) {
		t.Fatal("批准未被一次性消费")
	}
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- manager.wait(ctx, target, []string{"审批人"}, notify) }()
	id = receive(t, ids)
	cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) || manager.resolve("工作群", "审批人", id, true) {
		t.Fatalf("取消后审批仍可使用: %v", err)
	}
}

func TestApprovalWaitExpiresAtTaskDeadline(t *testing.T) {
	var manager approvalManager
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	target := approvalTarget{ChatID: "工作群"}
	ids, done := make(chan string, 1), make(chan error, 1)
	go func() {
		done <- manager.wait(ctx, target, []string{"审批人"}, func(_ context.Context, id string, expiry time.Time) error {
			deadline, _ := ctx.Deadline()
			if !expiry.Equal(deadline) {
				return errors.New("审批有效期超过任务期限")
			}
			ids <- id
			return nil
		})
	}()
	id := receive(t, ids)
	if err := receive(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("审批未随任务过期: %v", err)
	}
	if manager.resolve("工作群", "审批人", id, true) {
		t.Fatal("过期批准仍能执行")
	}
}

type modelFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f modelFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

func TestFeishuRejectsApprovalAfterFileChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "example.txt")
	initial := []byte("初始")
	if err := os.WriteFile(file, initial, 0600); err != nil {
		t.Fatal(err)
	}
	binding := ChatBinding{WorkDir: dir, AllowedUsers: []string{"用户"}, Approvers: []string{"审批人"}, Mode: tools.ModeWorkspace}
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群聊": binding}}
	args, err := json.Marshal(map[string]string{"path": "example.txt", "expected_version": fmt.Sprintf("%x", sha256.Sum256(initial)), "content": "模型写入"})
	if err != nil {
		t.Fatal(err)
	}
	approval := make(chan string, 1)
	observed := make(chan string, 1)
	finished := make(chan engine.RunResult, 1)
	run := func(ctx context.Context, session *ctxpkg.Session, spec engine.TaskSpec, reporter engine.Reporter, admit tools.MiddlewareFunc) (engine.RunResult, error) {
		calls := 0
		model := modelFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			calls++
			if calls == 1 {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "write-1", Name: "write_file", Arguments: args}}}, nil
			}
			for _, msg := range msgs {
				if msg.ToolCallID == "write-1" {
					observed <- msg.Content
					break
				}
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "已收集审批后的工具状态"}, nil
		})
		reg := engine.NewWorkspaceRegistry(dir)
		reg.Use(admit)
		eng := engine.NewAgentEngine(model, reg, engine.EngineOptions{Mode: tools.ModeWorkspace})
		result, err := eng.Run(ctx, session, spec, reporter)
		finished <- result
		return result, err
	}
	send := func(_ context.Context, _, text string) error {
		if strings.HasPrefix(text, "需要审批 ") {
			head, _, _ := strings.Cut(text, "\n")
			approval <- strings.TrimPrefix(head, "需要审批 ")
		}
		return nil
	}
	bot, err := newFeishuBot(context.Background(), config, run, send)
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	if err := bot.handle(context.Background(), incomingMessage{"request", "群聊", "用户", "修改 example.txt"}); err != nil {
		t.Fatal(err)
	}
	id := receive(t, approval)
	if err := os.WriteFile(file, []byte("其他版本"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bot.handle(context.Background(), incomingMessage{"approved", "群聊", "审批人", "approve " + id}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, observed); !strings.Contains(got, "审批后动作参数或对象版本已变化") {
		t.Fatalf("模型没有收到版本冲突: %s", got)
	}
	_ = receive(t, finished)
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "其他版本" {
		t.Fatalf("审批后覆盖了新版本: %q, %v", content, err)
	}
}

func TestFeishuVerificationUsesCommandApproval(t *testing.T) {
	dir := t.TempDir()
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{
		"群聊": {WorkDir: dir, AllowedUsers: []string{"用户"}, Approvers: []string{"审批人"},
			Mode: tools.ModeTrusted, VerifyCommand: "printf verified"},
	}}
	prompts := make(chan string, 1)
	finished := make(chan engine.RunResult, 1)
	run := func(ctx context.Context, session *ctxpkg.Session, spec engine.TaskSpec, reporter engine.Reporter, admit tools.MiddlewareFunc) (engine.RunResult, error) {
		model := modelFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			return &schema.Message{Role: schema.RoleAssistant, Content: "等待验收"}, nil
		})
		reg := engine.NewWorkspaceRegistry(dir)
		reg.Use(admit)
		eng := engine.NewAgentEngine(model, reg, engine.EngineOptions{Mode: tools.ModeTrusted})
		result, err := eng.Run(ctx, session, spec, reporter)
		finished <- result
		return result, err
	}
	bot, err := newFeishuBot(context.Background(), config, run, func(_ context.Context, _, text string) error {
		if strings.HasPrefix(text, "需要审批 ") {
			prompts <- text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	if err := bot.handle(context.Background(), incomingMessage{"request", "群聊", "用户", "执行修改任务"}); err != nil {
		t.Fatal(err)
	}
	prompt := receive(t, prompts)
	if !strings.Contains(prompt, "printf verified") {
		t.Fatalf("审批消息缺少实际验收命令: %s", prompt)
	}
	head, _, _ := strings.Cut(prompt, "\n")
	id := strings.TrimPrefix(head, "需要审批 ")
	if err := bot.handle(context.Background(), incomingMessage{"approved", "群聊", "审批人", "approve " + id}); err != nil {
		t.Fatal(err)
	}
	result := receive(t, finished)
	if result.Status != engine.Verified || result.Verification == nil || !strings.Contains(result.Verification.Output, "verified") {
		t.Fatalf("批准后的验收结果不正确: %#v", result)
	}
}
