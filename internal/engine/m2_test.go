package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type functionProvider func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f functionProvider) Generate(ctx context.Context, messages []schema.Message, definitions []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, messages, definitions)
}

func TestStartSerializesPromptAndSkipsCancelledWaiters(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &scriptedProvider{step: func(_ context.Context, n int, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "回答"}, nil
	}}
	eng, session := newRunFixture(t, p, false)
	session.ResetHistory()
	opts := testOptions(2, time.Second)
	first := eng.Start(context.Background(), session, "第一条", nil, opts)
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	second := eng.Start(ctx, session, "取消的消息", nil, opts)
	cancel()
	if got := <-second; !errors.Is(got.Err, runpkg.ErrCallerCancelled) {
		t.Fatalf("排队取消未生效: %+v", got)
	}
	expired := eng.Start(context.Background(), session, "排队超时", nil, testOptions(2, 20*time.Millisecond))
	if got := <-expired; !errors.Is(got.Err, runpkg.ErrTimeLimit) {
		t.Fatalf("总超时未覆盖排队: %+v", got)
	}
	third := eng.Start(context.Background(), session, "第三条", nil, opts)
	if session.MessageCount() != 1 {
		t.Fatal("等待中的用户消息提前进入历史")
	}
	close(release)
	if got := <-first; got.Err != nil {
		t.Fatal(got.Err)
	}
	if got := <-third; got.Err != nil {
		t.Fatal(got.Err)
	}
	var contents []string
	for _, msg := range session.GetWorkingMemory(20) {
		contents = append(contents, msg.Content)
	}
	if !reflect.DeepEqual(contents, []string{"第一条", "回答", "第三条", "回答"}) || p.calls != 2 {
		t.Fatalf("会话内容交叉: %v calls=%d", contents, p.calls)
	}
}

func TestDifferentSessionsCanRunConcurrently(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	p := functionProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return &schema.Message{Role: schema.RoleAssistant, Content: "完成"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	eng := NewAgentEngine(p, tools.NewRegistry(), false, false)
	first := eng.Start(context.Background(), ctxpkg.NewSession("1", t.TempDir()), "一", nil, testOptions(2, time.Second))
	second := eng.Start(context.Background(), ctxpkg.NewSession("2", t.TempDir()), "二", nil, testOptions(2, time.Second))
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("不同会话被串行阻塞")
		}
	}
	close(release)
	if got := <-first; got.Err != nil {
		t.Fatal(got.Err)
	}
	if got := <-second; got.Err != nil {
		t.Fatal(got.Err)
	}
}

func TestToolTimeoutMetadataReachesNextModel(t *testing.T) {
	p := &scriptedProvider{step: func(_ context.Context, n int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if n == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "wait", Name: "wait"}}}, nil
		}
		last := messages[len(messages)-1]
		if last.ToolCallID != "wait" || last.ToolMetadata == nil || !last.ToolMetadata.TimedOut {
			t.Errorf("下一轮没有工具超时元数据: %+v", last)
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "已处理超时"}, nil
	}}
	eng, session := newRunFixture(t, p, false, fakeTool{name: "wait", wait: true})
	opts := testOptions(3, time.Second)
	opts.ToolTimeout = 20 * time.Millisecond
	result, err := eng.Run(context.Background(), session, nil, opts)
	if err != nil || result.Status != runpkg.StatusSucceeded {
		t.Fatalf("工具超时不应直接终止仍有时间的父任务: %+v %v", result, err)
	}
}

func TestRunSubInheritsCancellation(t *testing.T) {
	p := &scriptedProvider{step: func(context.Context, int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "wait", Name: "wait"}}}, nil
	}}
	eng, _ := newRunFixture(t, p, false)
	r := tools.NewRegistry()
	r.Register(fakeTool{name: "wait", wait: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := eng.RunSub(ctx, "子任务", r, nil)
	if !errors.Is(err, context.DeadlineExceeded) || p.calls != 1 {
		t.Fatalf("子任务未响应父任务取消: err=%v calls=%d", err, p.calls)
	}
}
