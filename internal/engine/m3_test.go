package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type meteredProvider struct {
	calls int
	step  func(int, []schema.ToolDefinition) *schema.Message
}

func (p *meteredProvider) EstimateInput(context.Context, []schema.Message, []schema.ToolDefinition) (int64, error) {
	return 5, nil
}
func (p *meteredProvider) Generate(_ context.Context, _ []schema.Message, definitions []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	return p.step(p.calls, definitions), nil
}

func budgetOptions() runpkg.Options {
	return runpkg.Options{MaxTurns: 4, Timeout: time.Second, MaxTotalTokens: 25, MaxOutputTokens: 5}
}

func TestSharedBudgetStopsAfterSubagentObservation(t *testing.T) {
	p := &meteredProvider{step: func(call int, _ []schema.ToolDefinition) *schema.Message {
		if call == 1 {
			return &schema.Message{Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 3, CompletionTokens: 2}, ToolCalls: []schema.ToolCall{{ID: "sub-1", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"探索"}`)}}}
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "子任务完成", Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 10}}
	}}
	session := ctxpkg.NewSession("budget", t.TempDir())
	r := tools.NewRegistry()
	tracked := observability.NewCostTracker(p, "mock", session)
	eng := NewAgentEngine(tracked, r, false, false)
	r.Register(tools.NewSubagentTool(eng, tools.NewRegistry(), nil))
	result, err := eng.Submit(context.Background(), session, "任务", nil, budgetOptions())
	if !errors.Is(err, budget.ErrTokenLimit) || result.Reason != runpkg.ReasonTokenBudget || p.calls != 2 {
		t.Fatalf("共享预算未阻止主任务继续: %+v %v calls=%d", result, err, p.calls)
	}
	got := session.GetWorkingMemory(20)
	seen := false
	for _, msg := range got {
		if msg.ToolCallID == "sub-1" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("预算耗尽时丢失子任务 Observation")
	}
	if len(session.ModelCalls()) != 2 || result.Budget.UsedTokens != 25 {
		t.Fatalf("模型用量未共享: %+v %+v", result.Budget, session.ModelCalls())
	}
}

func TestCostBudgetRejectsBeforeProvider(t *testing.T) {
	p := &meteredProvider{step: func(int, []schema.ToolDefinition) *schema.Message {
		return &schema.Message{Role: schema.RoleAssistant, Content: "不应调用"}
	}}
	session := ctxpkg.NewSession("cost", t.TempDir())
	eng := NewAgentEngine(observability.NewCostTracker(p, "mock", session), tools.NewRegistry(), false, false)
	opts := budgetOptions()
	opts.Price = &budget.Price{Currency: "USD", Version: "v1", InputPerMillionMicros: 1000000, OutputPerMillionMicros: 1000000}
	opts.MaxCostMicros = 9
	result, err := eng.Submit(context.Background(), session, "任务", nil, opts)
	if !errors.Is(err, budget.ErrCostLimit) || result.Reason != runpkg.ReasonCostBudget || p.calls != 0 {
		t.Fatalf("费用预算未在请求前拒绝: %+v %v calls=%d", result, err, p.calls)
	}
}

func TestBudgetCannotBypassTracker(t *testing.T) {
	p := &meteredProvider{step: func(int, []schema.ToolDefinition) *schema.Message {
		return &schema.Message{Role: schema.RoleAssistant, Content: "不应调用"}
	}}
	eng := NewAgentEngine(p, tools.NewRegistry(), false, false)
	result, err := eng.Submit(context.Background(), ctxpkg.NewSession("untracked", t.TempDir()), "任务", nil, budgetOptions())
	if !errors.Is(err, runpkg.ErrInvalidOptions) || result.RunID != "" || p.calls != 0 {
		t.Fatalf("未跟踪的 Provider 绕过了预算: %+v %v calls=%d", result, err, p.calls)
	}
}

func TestThinkingAndActionShareBudget(t *testing.T) {
	p := &meteredProvider{step: func(int, []schema.ToolDefinition) *schema.Message {
		return &schema.Message{Role: schema.RoleAssistant, Content: "思考完毕", Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 5}}
	}}
	session := ctxpkg.NewSession("thinking", t.TempDir())
	eng := NewAgentEngine(observability.NewCostTracker(p, "mock", session), tools.NewRegistry(), true, false)
	opts := budgetOptions()
	opts.MaxTotalTokens = 15
	result, err := eng.Submit(context.Background(), session, "任务", nil, opts)
	if !errors.Is(err, budget.ErrTokenLimit) || result.Reason != runpkg.ReasonTokenBudget || p.calls != 1 {
		t.Fatalf("Thinking 后仍调用 Action: %+v %v calls=%d", result, err, p.calls)
	}
	if result.Budget.EstimateMethod == "" || session.ModelCalls()[0].EstimatedInputTokens != 5 {
		t.Fatalf("估算元数据缺失: %+v %+v", result.Budget, session.ModelCalls())
	}
}

func TestTraceFailureDoesNotHideRunResult(t *testing.T) {
	parent := t.TempDir()
	blocked := filepath.Join(parent, "file")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	session := ctxpkg.NewSession("trace", blocked)
	p := functionProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "完成"}, nil
	})
	eng := NewAgentEngine(p, tools.NewRegistry(), false, false)
	opts := budgetOptions()
	opts.MaxTotalTokens = 0
	result, err := eng.Submit(context.Background(), session, "任务", nil, opts)
	if err != nil || result.Status != runpkg.StatusSucceeded || result.TraceError == "" {
		t.Fatalf("Trace 失败覆盖了模型结果或未反馈: %+v %v", result, err)
	}
}
