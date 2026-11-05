package engine

import (
	"encoding/json"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type budgetSnapshot struct {
	ModelCallsRemaining int   `json:"model_calls_remaining"`
	ToolCallsRemaining  int   `json:"tool_calls_remaining"`
	RemainingMS         int64 `json:"remaining_ms"`
}

func (r *runState) budgetSnapshot() budgetSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := r.elapsed
	if !r.started.IsZero() {
		elapsed += time.Since(r.started)
	}
	return budgetSnapshot{
		ModelCallsRemaining: max(0, r.state.Limits.MaxModelCalls-r.state.Usage.ModelCalls),
		ToolCallsRemaining:  max(0, r.state.Limits.MaxToolCalls-r.state.Usage.ToolCalls),
		RemainingMS:         max(int64(0), (r.state.Limits.Timeout - elapsed).Milliseconds()),
	}
}

// withBudgetContext 仅修改本次请求副本，不把预算块写入会话或原始提示词。
func withBudgetContext(messages []schema.Message, snapshot budgetSnapshot) []schema.Message {
	encoded, _ := json.Marshal(snapshot)
	block := "\n\n运行预算（程序快照）：" + string(encoded) +
		"\n剩余模型次数包含本次拟请求；并发时该值仅为观察快照，实际准入由程序控制。请按剩余预算聚焦必要证据并及时交付，注明未验证项。"
	result := append([]schema.Message(nil), messages...)
	for i := range result {
		if result[i].Role == schema.RoleSystem {
			result[i].Content += block
			return result
		}
	}
	return append([]schema.Message{{Role: schema.RoleSystem, Content: block}}, result...)
}
