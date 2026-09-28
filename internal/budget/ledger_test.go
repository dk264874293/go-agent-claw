package budget

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestPriceParsingAndExactMatch(t *testing.T) {
	if amount, err := ParseMicros("1.234567"); err != nil || amount != 1234567 {
		t.Fatalf("金额解析失败: %d %v", amount, err)
	}
	for _, bad := range []string{"-1", "1.1234567", "1e3", "", "0."} {
		if _, err := ParseMicros(bad); err == nil {
			t.Fatalf("非法金额被接受: %q", bad)
		}
	}
	path := filepath.Join(t.TempDir(), "pricing.json")
	data := `{"version":"2026-09","rates":[{"provider":"openai","base_url":"https://api.example/v1/","model":"model-a","currency":"USD","source":"测试价目","input_per_million":"1.25","output_per_million":"2.50"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	price, err := LoadPrice(path, "openai", "https://api.example/v1", "model-a")
	if err != nil || price == nil || price.Currency != "USD" || price.Version != "2026-09" {
		t.Fatalf("价目匹配错误: %+v %v", price, err)
	}
	cost, err := price.CostMicros(1000000, 1000000)
	if err != nil || cost != 3750000 {
		t.Fatalf("计算错误: %d %v", cost, err)
	}
	missing, err := LoadPrice(path, "openai", "https://other.example/v1", "model-a")
	if err != nil || missing != nil {
		t.Fatalf("错误网关不应复用价格: %+v %v", missing, err)
	}
	duplicate := `{"version":"v1","rates":[{"provider":"openai","base_url":"https://api.example/v1","model":"model-a","currency":"USD","input_per_million":"1","output_per_million":"2"},{"provider":"openai","base_url":"https://api.example/v1/","model":"model-a","currency":"USD","input_per_million":"3","output_per_million":"4"}]}`
	if err := os.WriteFile(path, []byte(duplicate), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrice(path, "openai", "https://api.example/v1", "model-a"); err == nil {
		t.Fatal("重复价目应拒绝")
	}
}

func TestLedgerConcurrentReservationAndSettlement(t *testing.T) {
	price := &Price{Currency: "USD", Version: "v1", InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000}
	ledger, err := NewLedger(Limits{MaxTokens: 20, MaxCostMicros: 30, MaxOutputTokens: 5, Price: price})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var allowed int
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, err := ledger.Reserve(10)
			if err == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
				_ = ledger.Settle(reservation, &schema.Usage{PromptTokens: 10, CompletionTokens: 5})
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("并发请求双花余额: %d", allowed)
	}
	snapshot := ledger.Snapshot()
	if snapshot.UsedTokens != 15 || snapshot.ReservedTokens != 0 || snapshot.KnownCostMicros != 20 || snapshot.Exhausted != "token_budget" {
		t.Fatalf("结算不正确: %+v", snapshot)
	}
}

func TestLedgerUnknownUsageKeepsReservation(t *testing.T) {
	ledger, _ := NewLedger(Limits{MaxTokens: 100, MaxOutputTokens: 5})
	reservation, err := ledger.Reserve(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(reservation, nil); !errors.Is(err, ErrUsageUnknown) {
		t.Fatal(err)
	}
	if ledger.Snapshot().ReservedTokens != 15 {
		t.Fatal("未知 Usage 被当作零释放")
	}
	if _, err := ledger.Reserve(1); !errors.Is(err, ErrUsageUnknown) {
		t.Fatal("用量未知后仍放行新请求")
	}
}

func TestLedgerExactBoundaryAndOneTokenShort(t *testing.T) {
	ledger, err := NewLedger(Limits{MaxTokens: 15, MaxOutputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(reservation, &schema.Usage{PromptTokens: 10, CompletionTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(0); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("用尽后未拒绝: %v", err)
	}
	short, _ := NewLedger(Limits{MaxTokens: 14, MaxOutputTokens: 5})
	if _, err := short.Reserve(10); !errors.Is(err, ErrTokenLimit) || short.Snapshot().Requests != 0 {
		t.Fatalf("差 1 Token 时应在请求前拒绝: %v %+v", err, short.Snapshot())
	}
}

func TestSnapshotCostLabelUnknown(t *testing.T) {
	if got := (Snapshot{Requests: 1}).CostLabel(); got != "费用未知（无匹配价格）" {
		t.Fatalf("缺价被误报为零: %s", got)
	}
	if got := (Snapshot{Requests: 1, PriceKnown: true, Currency: "USD", UnknownUsage: true}).CostLabel(); got != "USD 0.000000；另有用量待核对" {
		t.Fatalf("未知 Usage 未展示: %s", got)
	}
}
