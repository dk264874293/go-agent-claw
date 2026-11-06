package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type budgetSnapshot struct {
	ModelCallsRemaining          int   `json:"model_calls_remaining"`
	ToolCallsRemaining           int   `json:"tool_calls_remaining"`
	RemainingMS                  int64 `json:"remaining_ms"`
	SubtaskModelCallsRemaining   *int  `json:"subtask_model_calls_remaining,omitempty"`
	SubtaskToolCallsRemaining    *int  `json:"subtask_tool_calls_remaining,omitempty"`
	EffectiveModelCallsRemaining *int  `json:"effective_model_calls_remaining,omitempty"`
	EffectiveToolCallsRemaining  *int  `json:"effective_tool_calls_remaining,omitempty"`
}

func (r *runState) budgetSnapshot() budgetSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.budgetSnapshotLocked()
}

func (r *runState) budgetSnapshotLocked() budgetSnapshot {
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

// requestBudgetSnapshot 在同一把锁下读取共享及当前子任务额度，保留共享原值。
// 各 remaining 都包含本次拟请求；快照不代替 reserve 的原子准入。
func (r *runState) requestBudgetSnapshot(ctx context.Context) budgetSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.budgetSnapshotLocked()
	if r.state.Delegation == nil {
		return snapshot
	}
	sub := r.state.Delegation.Subtasks[observe.Identity(ctx).AgentID]
	if sub == nil {
		return snapshot
	}
	models, tools := max(0, subtaskModelLimit-sub.ModelCalls), max(0, subtaskToolLimit-sub.ToolCalls)
	effectiveModels, effectiveTools := min(snapshot.ModelCallsRemaining, models), min(snapshot.ToolCallsRemaining, tools)
	if effectiveTools == 0 {
		effectiveModels = 0
	}
	snapshot.SubtaskModelCallsRemaining, snapshot.SubtaskToolCallsRemaining = &models, &tools
	snapshot.EffectiveModelCallsRemaining, snapshot.EffectiveToolCallsRemaining = &effectiveModels, &effectiveTools
	return snapshot
}

// withBudgetContext 仅修改本次请求副本，不把预算块写入会话或原始提示词。
func withBudgetContext(messages []schema.Message, snapshot budgetSnapshot) []schema.Message {
	encoded, _ := json.Marshal(snapshot)
	block := "\n\n运行预算（程序快照）：" + string(encoded) +
		"\n剩余模型次数包含本次拟请求；并发时该值仅为观察快照，实际准入由程序控制。请按剩余预算聚焦必要证据并及时交付，注明未验证项。"
	if snapshot.SubtaskModelCallsRemaining != nil {
		block += "\n当前子任务同时受共享和局部硬额度约束，实际可用次数取两者较小值；局部工具额度耗尽后不再调用模型补报告，请在已有请求中及时交付发现与缺口。"
	}
	result := append([]schema.Message(nil), messages...)
	for i := range result {
		if result[i].Role == schema.RoleSystem {
			result[i].Content += block
			return result
		}
	}
	return append([]schema.Message{{Role: schema.RoleSystem, Content: block}}, result...)
}
