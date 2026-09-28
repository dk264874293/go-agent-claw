package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

var (
	ErrTokenLimit   = errors.New("Token 预算已耗尽")
	ErrCostLimit    = errors.New("费用预算已耗尽")
	ErrUsageUnknown = errors.New("模型用量未知，预算不能继续结算")
)

type Limits struct {
	MaxTokens       int64
	MaxCostMicros   int64
	MaxOutputTokens int
	Price           *Price
	EstimateMethod  string
	ConfigVersion   string
}

type Snapshot struct {
	RunID              string
	Provider           string
	Model              string
	UsedTokens         int64
	ReservedTokens     int64
	KnownCostMicros    int64
	ReservedCostMicros int64
	Requests           int
	UnknownUsage       bool
	Exhausted          string
	Currency           string
	PriceVersion       string
	PriceKnown         bool
	EstimateMethod     string
	ConfigVersion      string
	MaxTokens          int64
	MaxCostMicros      int64
	MaxOutputTokens    int
}

func (s Snapshot) CostLabel() string {
	if s.Requests == 0 {
		return "尚无模型请求"
	}
	if !s.PriceKnown {
		return "费用未知（无匹配价格）"
	}
	label := fmt.Sprintf("%s %s", s.Currency, FormatMicros(s.KnownCostMicros))
	if s.UnknownUsage {
		return label + "；另有用量待核对"
	}
	return label
}

type Ledger struct {
	mu       sync.Mutex
	limits   Limits
	snapshot Snapshot
}

func (l *Ledger) SetIdentity(runID, provider, model string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.snapshot.RunID, l.snapshot.Provider, l.snapshot.Model = runID, provider, model
}

type Reservation struct {
	ledger   *Ledger
	tokens   int64
	cost     int64
	settled  bool
	sequence int
	input    int64
}

func (r *Reservation) Sequence() int {
	if r == nil {
		return 0
	}
	return r.sequence
}

func (r *Reservation) EstimatedInputTokens() int64 {
	if r == nil {
		return 0
	}
	return r.input
}

func NewLedger(limits Limits) (*Ledger, error) {
	if limits.MaxTokens < 0 || limits.MaxCostMicros < 0 || limits.MaxOutputTokens <= 0 || (limits.MaxCostMicros > 0 && limits.Price == nil) {
		return nil, fmt.Errorf("预算配置无效")
	}
	ledger := &Ledger{limits: limits}
	ledger.snapshot.EstimateMethod = limits.EstimateMethod
	ledger.snapshot.ConfigVersion = limits.ConfigVersion
	ledger.snapshot.MaxTokens = limits.MaxTokens
	ledger.snapshot.MaxCostMicros = limits.MaxCostMicros
	ledger.snapshot.MaxOutputTokens = limits.MaxOutputTokens
	if limits.Price != nil {
		price := *limits.Price
		ledger.limits.Price = &price
		ledger.snapshot.Currency = price.Currency
		ledger.snapshot.PriceVersion = price.Version
		ledger.snapshot.PriceKnown = true
	}
	return ledger, nil
}

func (l *Ledger) Reserve(inputTokens int64) (*Reservation, error) {
	if inputTokens < 0 || inputTokens > int64(^uint64(0)>>1)-int64(l.limits.MaxOutputTokens) {
		return nil, fmt.Errorf("输入 Token 估算无效")
	}
	neededTokens := inputTokens + int64(l.limits.MaxOutputTokens)
	var neededCost int64
	var err error
	if l.limits.Price != nil {
		neededCost, err = l.limits.Price.CostMicros(inputTokens, int64(l.limits.MaxOutputTokens))
		if err != nil {
			return nil, err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.snapshot.UnknownUsage {
		return nil, ErrUsageUnknown
	}
	if l.snapshot.Exhausted == "token_budget" {
		return nil, ErrTokenLimit
	}
	if l.snapshot.Exhausted == "cost_budget" {
		return nil, ErrCostLimit
	}
	if l.limits.MaxTokens > 0 && neededTokens > l.limits.MaxTokens-l.snapshot.UsedTokens-l.snapshot.ReservedTokens {
		l.snapshot.Exhausted = "token_budget"
		return nil, ErrTokenLimit
	}
	if l.limits.MaxCostMicros > 0 && neededCost > l.limits.MaxCostMicros-l.snapshot.KnownCostMicros-l.snapshot.ReservedCostMicros {
		l.snapshot.Exhausted = "cost_budget"
		return nil, ErrCostLimit
	}
	l.snapshot.ReservedTokens += neededTokens
	l.snapshot.ReservedCostMicros += neededCost
	l.snapshot.Requests++
	return &Reservation{ledger: l, tokens: neededTokens, cost: neededCost, sequence: l.snapshot.Requests, input: inputTokens}, nil
}

// Settle 用实际 Usage 结算。缺失 Usage 时保留预留并禁止后续请求。
func (l *Ledger) Settle(reservation *Reservation, usage *schema.Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if reservation == nil || reservation.ledger != l || reservation.settled {
		return fmt.Errorf("预算预留无效或已结算")
	}
	reservation.settled = true
	if usage == nil || usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		l.snapshot.UnknownUsage = true
		return ErrUsageUnknown
	}
	if int64(usage.PromptTokens) > math.MaxInt64-int64(usage.CompletionTokens) {
		l.snapshot.UnknownUsage = true
		return fmt.Errorf("模型用量溢出")
	}
	used := int64(usage.PromptTokens) + int64(usage.CompletionTokens)
	var cost int64
	if l.limits.Price != nil {
		var err error
		cost, err = l.limits.Price.CostMicros(int64(usage.PromptTokens), int64(usage.CompletionTokens))
		if err != nil {
			l.snapshot.UnknownUsage = true
			return err
		}
	}
	if used > math.MaxInt64-l.snapshot.UsedTokens || cost > math.MaxInt64-l.snapshot.KnownCostMicros {
		l.snapshot.UnknownUsage = true
		return fmt.Errorf("累计模型用量或费用溢出")
	}
	l.snapshot.ReservedTokens -= reservation.tokens
	l.snapshot.ReservedCostMicros -= reservation.cost
	l.snapshot.UsedTokens += used
	l.snapshot.KnownCostMicros += cost
	if l.limits.MaxTokens > 0 && l.snapshot.UsedTokens >= l.limits.MaxTokens {
		l.snapshot.Exhausted = "token_budget"
	}
	if l.limits.MaxCostMicros > 0 && l.snapshot.KnownCostMicros >= l.limits.MaxCostMicros {
		l.snapshot.Exhausted = "cost_budget"
	}
	return nil
}

func (l *Ledger) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshot
}

func (l *Ledger) RequiresEstimate() bool { return l.limits.MaxTokens > 0 || l.limits.MaxCostMicros > 0 }

func (l *Ledger) Price() *Price {
	if l.limits.Price == nil {
		return nil
	}
	copy := *l.limits.Price
	return &copy
}

func (l *Ledger) StopReason() error {
	snapshot := l.Snapshot()
	if snapshot.UnknownUsage {
		return ErrUsageUnknown
	}
	switch snapshot.Exhausted {
	case "token_budget":
		return ErrTokenLimit
	case "cost_budget":
		return ErrCostLimit
	default:
		return nil
	}
}

type ledgerKey struct{}

func WithLedger(ctx context.Context, ledger *Ledger) context.Context {
	return context.WithValue(ctx, ledgerKey{}, ledger)
}

func FromContext(ctx context.Context) *Ledger {
	value, _ := ctx.Value(ledgerKey{}).(*Ledger)
	return value
}
