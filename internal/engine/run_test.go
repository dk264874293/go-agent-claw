package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type scriptedProvider struct {
	calls int
	step  func(context.Context, int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)
}

func (p *scriptedProvider) Generate(ctx context.Context, messages []schema.Message, definitions []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	return p.step(ctx, p.calls, messages, definitions)
}

type fakeTool struct {
	name   string
	output string
	err    error
	wait   bool
}

func (t fakeTool) Name() string { return t.name }
func (t fakeTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace}
}
func (t fakeTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.name}
}
func (t fakeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.wait {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return t.output, t.err
}

func newRunFixture(t *testing.T, p *scriptedProvider, thinking bool, registered ...fakeTool) (*AgentEngine, *ctxpkg.Session) {
	t.Helper()
	registry := tools.NewRegistry()
	for _, tool := range registered {
		registry.Register(tool)
	}
	session := ctxpkg.NewSession("测试会话", t.TempDir())
	session.Append(schema.Message{Role: schema.RoleUser, Content: "完成测试任务"})
	return NewAgentEngine(p, registry, thinking, false), session
}

func testOptions(maxTurns int, timeout time.Duration) runpkg.Options {
	return runpkg.Options{MaxTurns: maxTurns, Timeout: timeout}
}

func TestRunCompletesWithoutTools(t *testing.T) {
	p := &scriptedProvider{step: func(_ context.Context, _ int, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "完成"}, nil
	}}
	eng, session := newRunFixture(t, p, false)
	got, err := eng.Run(context.Background(), session, nil, testOptions(2, time.Second))
	if err != nil || got.Status != runpkg.StatusSucceeded || got.Reason != runpkg.ReasonCompleted || got.Turns != 1 {
		t.Fatalf("正常运行结果不正确: result=%+v err=%v", got, err)
	}
	if len(got.RunID) != 32 || got.SessionID != session.ID || got.StartedAt.IsZero() || got.EndedAt.Before(got.StartedAt) {
		t.Fatalf("运行元数据不完整: %+v", got)
	}
	second, err := eng.Run(context.Background(), session, nil, testOptions(2, time.Second))
	if err != nil || second.RunID == got.RunID {
		t.Fatalf("两次运行应有不同 ID: first=%s second=%s err=%v", got.RunID, second.RunID, err)
	}
}

func TestRunParallelFailureReachesNextModelAndReminder(t *testing.T) {
	var secondInput, fourthInput []schema.Message
	p := &scriptedProvider{step: func(_ context.Context, n int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if n == 2 {
			secondInput = append([]schema.Message(nil), messages...)
		}
		if n == 4 {
			fourthInput = append([]schema.Message(nil), messages...)
			return &schema.Message{Role: schema.RoleAssistant, Content: "已经处理"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: fmt.Sprintf("good-%d", n), Name: "good", Arguments: json.RawMessage(`{}`)},
			{ID: fmt.Sprintf("bad-%d", n), Name: "bash", Arguments: json.RawMessage(`{"command":"missing"}`)},
		}}, nil
	}}
	eng, session := newRunFixture(t, p, false,
		fakeTool{name: "good", output: "正常结果"},
		fakeTool{name: "bash", output: "command not found，原始 stderr", err: errors.New("exit status 127")},
	)
	got, err := eng.Run(context.Background(), session, nil, testOptions(5, time.Second))
	if err != nil || got.Status != runpkg.StatusSucceeded {
		t.Fatalf("运行应在模型修正后结束: result=%+v err=%v", got, err)
	}
	goodIndex, badIndex := -1, -1
	for i, msg := range secondInput {
		switch msg.ToolCallID {
		case "good-1":
			goodIndex = i
		case "bad-1":
			badIndex = i
			if !strings.Contains(msg.Content, "command not found，原始 stderr") ||
				!strings.Contains(msg.Content, "exit status 127") || !strings.Contains(msg.Content, "系统救援指南") {
				t.Fatalf("下一轮模型未收到完整错误与救援建议: %q", msg.Content)
			}
		}
	}
	if goodIndex < 0 || badIndex <= goodIndex {
		t.Fatalf("并行工具结果顺序错误: good=%d bad=%d", goodIndex, badIndex)
	}
	if len(fourthInput) == 0 || !strings.Contains(fourthInput[len(fourthInput)-1].Content, "SYSTEM REMINDER") {
		t.Fatalf("非首项工具连续失败未触发提醒: %+v", fourthInput)
	}
	t.Logf("正常工具任务: RunID=%s Status=%s Reason=%s Observation=%q", got.RunID, got.Status, got.Reason, secondInput[badIndex].Content)
}

func TestRunMaxTurnsStopsFurtherModelRequests(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		t.Run(fmt.Sprintf("thinking=%v", thinking), func(t *testing.T) {
			actions := 0
			p := &scriptedProvider{step: func(_ context.Context, _ int, _ []schema.Message, definitions []schema.ToolDefinition) (*schema.Message, error) {
				if thinking && len(definitions) == 0 {
					return &schema.Message{Role: schema.RoleAssistant, Content: "先思考"}, nil
				}
				actions++
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
					ID: fmt.Sprintf("call-%d", actions), Name: "echo", Arguments: json.RawMessage(`{}`),
				}}}, nil
			}}
			eng, session := newRunFixture(t, p, thinking, fakeTool{name: "echo", output: "ok"})
			got, err := eng.Run(context.Background(), session, nil, testOptions(2, time.Second))
			wantCalls := 2
			if thinking {
				wantCalls = 4
			}
			if !errors.Is(err, runpkg.ErrMaxTurns) || got.Reason != runpkg.ReasonMaxTurns ||
				got.Status != runpkg.StatusBudgetExhausted || got.Turns != 2 || p.calls != wantCalls || actions != 2 {
				t.Fatalf("轮数限制错误: result=%+v err=%v providerCalls=%d actions=%d", got, err, p.calls, actions)
			}
			memory := session.GetWorkingMemory(20)
			if len(memory) == 0 || memory[len(memory)-1].ToolCallID != "call-2" {
				t.Fatalf("末轮工具结果未完整写入 Session: %+v", memory)
			}
			t.Logf("轮数终止任务: RunID=%s Status=%s Reason=%s Observation=%q", got.RunID, got.Status, got.Reason, memory[len(memory)-1].Content)
		})
	}
}

func TestRunTimeLimitAndCallerCancellation(t *testing.T) {
	newBlocking := func(t *testing.T, started chan struct{}) (*AgentEngine, *ctxpkg.Session, *scriptedProvider) {
		p := &scriptedProvider{step: func(ctx context.Context, _ int, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			if started != nil {
				close(started)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		eng, session := newRunFixture(t, p, false)
		return eng, session, p
	}
	t.Run("总超时", func(t *testing.T) {
		eng, session, p := newBlocking(t, nil)
		got, err := eng.Run(context.Background(), session, nil, testOptions(5, 20*time.Millisecond))
		if !errors.Is(err, runpkg.ErrTimeLimit) || got.Status != runpkg.StatusBudgetExhausted || got.Reason != runpkg.ReasonTimeLimit || p.calls != 1 {
			t.Fatalf("超时结果不正确: result=%+v err=%v calls=%d", got, err, p.calls)
		}
	})
	t.Run("调用方取消", func(t *testing.T) {
		started := make(chan struct{})
		eng, session, p := newBlocking(t, started)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resultCh := make(chan struct {
			result runpkg.Result
			err    error
		}, 1)
		go func() {
			result, err := eng.Run(ctx, session, nil, testOptions(5, time.Second))
			resultCh <- struct {
				result runpkg.Result
				err    error
			}{result, err}
		}()
		<-started
		cancel()
		select {
		case got := <-resultCh:
			if !errors.Is(got.err, runpkg.ErrCallerCancelled) || got.result.Reason != runpkg.ReasonCallerCancelled || p.calls != 1 {
				t.Fatalf("调用方取消结果不正确: result=%+v err=%v calls=%d", got.result, got.err, p.calls)
			}
		case <-time.After(time.Second):
			t.Fatal("调用方取消后运行没有退出")
		}
	})
}

func TestRunTimeoutPreservesToolCallPair(t *testing.T) {
	p := &scriptedProvider{step: func(_ context.Context, _ int, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID: "waiting-tool", Name: "wait", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}}
	eng, session := newRunFixture(t, p, false, fakeTool{name: "wait", wait: true})
	got, err := eng.Run(context.Background(), session, nil, testOptions(5, 20*time.Millisecond))
	if !errors.Is(err, runpkg.ErrTimeLimit) || got.Reason != runpkg.ReasonTimeLimit {
		t.Fatalf("工具阶段超时结果不正确: result=%+v err=%v", got, err)
	}
	memory := session.GetWorkingMemory(20)
	if len(memory) != 3 || len(memory[1].ToolCalls) != 1 || memory[2].ToolCallID != "waiting-tool" {
		t.Fatalf("超时后工具调用与结果未配对: %+v", memory)
	}
}

func TestRunProviderErrorsAndInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output *schema.Message
		err    error
		reason runpkg.Reason
		want   error
	}{
		{"模型错误", nil, errors.New("上游失败"), runpkg.ReasonProviderError, runpkg.ErrProvider},
		{"空响应", nil, nil, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse},
		{"工具调用缺少 ID", &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{Name: "echo"}}}, nil, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedProvider{step: func(context.Context, int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return tc.output, tc.err
			}}
			eng, session := newRunFixture(t, p, false)
			got, err := eng.Run(context.Background(), session, nil, testOptions(2, time.Second))
			if !errors.Is(err, tc.want) || got.Status != runpkg.StatusFailed || got.Reason != tc.reason || got.EndedAt.IsZero() {
				t.Fatalf("错误结果不正确: result=%+v err=%v", got, err)
			}
		})
	}
	p := &scriptedProvider{step: func(context.Context, int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("非法选项不应调用模型")
		return nil, nil
	}}
	eng, session := newRunFixture(t, p, false)
	_, err := eng.Run(context.Background(), session, nil, runpkg.Options{})
	if !errors.Is(err, runpkg.ErrInvalidOptions) || p.calls != 0 {
		t.Fatalf("非法选项验证失败: err=%v calls=%d", err, p.calls)
	}
}
