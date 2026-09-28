package feishu

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type feishuMeteredProvider struct{ calls int }

func (*feishuMeteredProvider) EstimateInput(context.Context, []schema.Message, []schema.ToolDefinition) (int64, error) {
	return 5, nil
}

func (p *feishuMeteredProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	return &schema.Message{Role: schema.RoleAssistant, Content: "完成", Usage: &schema.Usage{}}, nil
}

func TestFeishuUsesConfiguredRunBudget(t *testing.T) {
	p := &feishuMeteredProvider{}
	b := &FeishuBot{workDir: t.TempDir(), factory: func(session *ctxpkg.Session) *engine.AgentEngine {
		return engine.NewAgentEngine(observability.NewCostTracker(p, "mock", session), tools.NewRegistry(), false, false)
	}, RunOptions: runpkg.Options{MaxTurns: 2, Timeout: time.Second, MaxTotalTokens: 9, MaxOutputTokens: 5}}
	chatID := fmt.Sprintf("预算会话-%d", feishuTestChatSequence.Add(1))
	outcome := <-b.submitAgentRun(context.Background(), chatID, "任务", nil)
	if !errors.Is(outcome.Err, budget.ErrTokenLimit) || outcome.Result.Reason != runpkg.ReasonTokenBudget || p.calls != 0 {
		t.Fatalf("飞书未使用配置预算: %+v calls=%d", outcome, p.calls)
	}
}
