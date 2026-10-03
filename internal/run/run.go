package run

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
)

const ConfigSchemaVersion = "s2-v1"

// Options 定义单次运行的硬性轮数和时间上限。
type Options struct {
	MaxTurns           int
	Timeout            time.Duration
	ToolTimeout        time.Duration
	MaxConcurrentTools int
	MaxTotalTokens     int64
	MaxOutputTokens    int
	MaxCostMicros      int64
	Price              *budget.Price
	Provider           string
	Model              string
	ConfigVersion      string
}

// Status 表示运行的技术状态，不代表用户任务已通过业务验收。
type Status string

const (
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusCancelled       Status = "cancelled"
	StatusBudgetExhausted Status = "budget_exhausted"
)

// Reason 表示运行结束的具体原因。
type Reason string

const (
	ReasonCompleted       Reason = "completed"
	ReasonMaxTurns        Reason = "max_turns"
	ReasonTimeLimit       Reason = "time_limit"
	ReasonCallerCancelled Reason = "caller_cancelled"
	ReasonProviderError   Reason = "provider_error"
	ReasonInvalidResponse Reason = "invalid_response"
	ReasonTokenBudget     Reason = "token_budget"
	ReasonCostBudget      Reason = "cost_budget"
	ReasonUsageUnknown    Reason = "usage_unknown"
)

var (
	ErrInvalidOptions  = errors.New("运行选项无效")
	ErrMaxTurns        = errors.New("运行达到最大轮数")
	ErrTimeLimit       = errors.New("运行达到总超时")
	ErrCallerCancelled = errors.New("调用方取消运行")
	ErrProvider        = errors.New("模型请求失败")
	ErrInvalidResponse = errors.New("模型响应无效")
	ErrUsageUnknown    = budget.ErrUsageUnknown
)

// Result 是一次运行结束后的快照。
type Result struct {
	RunID      string
	SessionID  string
	Status     Status
	Reason     Reason
	StartedAt  time.Time
	EndedAt    time.Time
	Turns      int
	Budget     budget.Snapshot
	TracePath  string
	TraceError string
}

// NewID 生成跨进程仍有足够碰撞余量的随机运行标识。
func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
