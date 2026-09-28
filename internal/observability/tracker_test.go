package observability

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/budget"
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

type estimatedUsageProvider struct{ usage *schema.Usage }

func (p estimatedUsageProvider) EstimateInput(context.Context, []schema.Message, []schema.ToolDefinition) (int64, error) {
	return 3, nil
}

type failedUsageProvider struct{}

func (failedUsageProvider) EstimateInput(context.Context, []schema.Message, []schema.ToolDefinition) (int64, error) {
	return 3, nil
}
func (failedUsageProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 3, CompletionTokens: 2}}, errors.New("网关报告失败")
}

func TestTrackerRecordsUsageEvenWhenProviderFails(t *testing.T) {
	ledger, _ := budget.NewLedger(budget.Limits{MaxTokens: 100, MaxOutputTokens: 5})
	session := ctxpkg.NewSession("failure", t.TempDir())
	tracker := NewCostTracker(failedUsageProvider{}, "mock", session)
	_, err := tracker.Generate(budget.WithLedger(context.Background(), ledger), nil, nil)
	if err == nil || ledger.Snapshot().UsedTokens != 5 || session.UsageSnapshot().TotalPromptTokens != 3 || session.UsageSnapshot().UnknownCostRequests != 1 {
		t.Fatalf("Provider 错误丢失已知 Usage: %v %+v %+v", err, ledger.Snapshot(), session.UsageSnapshot())
	}
}
func (p estimatedUsageProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: "完成", Usage: p.usage}, nil
}

func TestTrackerKnownCostAndUnknownUsage(t *testing.T) {
	price := &budget.Price{Currency: "USD", Version: "v1", InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000}
	ledger, _ := budget.NewLedger(budget.Limits{MaxTokens: 100, MaxOutputTokens: 5, Price: price})
	ledger.SetIdentity("run", "openai", "mock")
	session := ctxpkg.NewSession("meter", t.TempDir())
	ctx := budget.WithLedger(context.Background(), ledger)
	tracker := NewCostTracker(estimatedUsageProvider{usage: &schema.Usage{PromptTokens: 3, CompletionTokens: 2}}, "mock", session)
	if _, err := tracker.Generate(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := session.UsageSnapshot()
	if snapshot.CostLabel() != "USD 0.000007" || len(session.ModelCalls()) != 1 || session.ModelCalls()[0].PriceVersion != "v1" {
		t.Fatalf("计量记录错误: %+v %+v", snapshot, session.ModelCalls())
	}
	unknownLedger, _ := budget.NewLedger(budget.Limits{MaxTokens: 100, MaxOutputTokens: 5, Price: price})
	unknown := NewCostTracker(estimatedUsageProvider{}, "mock", session)
	if _, err := unknown.Generate(budget.WithLedger(context.Background(), unknownLedger), nil, nil); !errors.Is(err, budget.ErrUsageUnknown) {
		t.Fatalf("缺失 Usage 未阻止预算继续: %v", err)
	}
	if session.UsageSnapshot().UnknownCostRequests != 1 || session.UsageSnapshot().UnknownUsageRequests != 1 || unknownLedger.Snapshot().ReservedTokens == 0 {
		t.Fatal("未知费用被当作零")
	}
}

func TestTrackerRejectsInvalidUsageWithoutNegativeAccounting(t *testing.T) {
	ledger, _ := budget.NewLedger(budget.Limits{MaxTokens: 100, MaxOutputTokens: 5})
	session := ctxpkg.NewSession("invalid", t.TempDir())
	tracker := NewCostTracker(estimatedUsageProvider{usage: &schema.Usage{PromptTokens: -1, CompletionTokens: 2}}, "mock", session)
	_, err := tracker.Generate(budget.WithLedger(context.Background(), ledger), nil, nil)
	if !errors.Is(err, budget.ErrUsageUnknown) {
		t.Fatalf("非法 Usage 应标记未知: %v", err)
	}
	snapshot := session.UsageSnapshot()
	if snapshot.TotalPromptTokens != 0 || snapshot.TotalCompletionTokens != 0 || snapshot.UnknownUsageRequests != 1 || ledger.Snapshot().ReservedTokens == 0 {
		t.Fatalf("非法 Usage 破坏账单: %+v %+v", snapshot, ledger.Snapshot())
	}
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
