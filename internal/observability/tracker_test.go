package observability

import (
	"context"
	"sync"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type usageProvider struct{ empty bool }

func (p usageProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	if p.empty {
		return nil, nil
	}
	return &schema.Message{Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 20}}, nil
}

func TestCostTrackerConcurrentUsage(t *testing.T) {
	session := ctxpkg.NewSession("账单", t.TempDir())
	tracker := NewCostTracker(usageProvider{}, "glm-4.7", session)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tracker.Generate(context.Background(), nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	usage := session.UsageSnapshot()
	if usage.TotalPromptTokens != 80 || usage.TotalCompletionTokens != 160 {
		t.Fatalf("并发账单丢失: %+v", usage)
	}
}

func TestCostTrackerPassesNilResponseToEngine(t *testing.T) {
	tracker := NewCostTracker(usageProvider{empty: true}, "mock", nil)
	message, err := tracker.Generate(context.Background(), nil, nil)
	if message != nil || err != nil {
		t.Fatalf("空响应应由 Engine 判定: %v %v", message, err)
	}
}
