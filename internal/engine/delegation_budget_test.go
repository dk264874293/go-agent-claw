package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDelegationBudgetSnapshotShowsLocalAndSharedLimits(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "on", 1)
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{}, "child"); err != nil {
		t.Fatal(err)
	}
	r.state.Usage.ModelCalls = 20
	r.state.Usage.ToolCalls = 17
	sub := r.state.Delegation.Subtasks["child"]
	sub.ModelCalls = 3
	sub.ToolCalls = 6
	child := observe.WithAgent(ctx, "child", "root")
	snapshot := r.requestBudgetSnapshot(child)
	if snapshot.ModelCallsRemaining != 40 || snapshot.ToolCallsRemaining != 103 {
		t.Fatal("共享额度被局部额度覆盖")
	}
	if snapshot.SubtaskModelCallsRemaining == nil || *snapshot.SubtaskModelCallsRemaining != 1 || snapshot.SubtaskToolCallsRemaining == nil || *snapshot.SubtaskToolCallsRemaining != 2 {
		t.Fatalf("局部余量不正确: %+v", snapshot)
	}
	if *snapshot.EffectiveModelCallsRemaining != 1 || *snapshot.EffectiveToolCallsRemaining != 2 {
		t.Fatal("实际可用额度不是较小值")
	}
	r.state.Limits.MaxToolCalls = 18
	snapshot = r.requestBudgetSnapshot(child)
	if snapshot.ToolCallsRemaining != 1 || *snapshot.SubtaskToolCallsRemaining != 2 || *snapshot.EffectiveToolCallsRemaining != 1 {
		t.Fatal("共享工具剩余少于局部时未取较小值")
	}
	messages := []schema.Message{{Role: schema.RoleSystem, Content: "已有约束"}, {Role: schema.RoleUser, Content: "只读调查"}}
	request := withBudgetContext(messages, snapshot)
	if !strings.Contains(request[0].Content, `"subtask_model_calls_remaining":1`) || !strings.Contains(request[0].Content, "局部工具额度耗尽后不再调用模型补报告") {
		t.Fatal("模型没有看到局部硬限制")
	}
	if messages[0].Content != "已有约束" {
		t.Fatal("预算注入改写原始消息")
	}
	parent := r.requestBudgetSnapshot(ctx)
	if parent.SubtaskModelCallsRemaining != nil || parent.SubtaskToolCallsRemaining != nil || parent.EffectiveModelCallsRemaining != nil {
		t.Fatal("根任务被错误套用子额度")
	}
	raw, _ := json.Marshal(parent)
	if strings.Contains(string(raw), "subtask_") {
		t.Fatal("不存在局部额度时编造了子任务余量")
	}
}

func TestDelegationExhaustedToolsCannotStartAnotherModel(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "on", 1)
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{}, "child"); err != nil {
		t.Fatal(err)
	}
	child := observe.WithAgent(ctx, "child", "root")
	for range subtaskToolLimit {
		if err := r.reserve(child, "tool"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := r.requestBudgetSnapshot(child)
	if *snapshot.SubtaskToolCallsRemaining != 0 || *snapshot.EffectiveModelCallsRemaining != 0 || snapshot.ModelCallsRemaining != 60 {
		t.Fatal("工具耗尽后的可用模型次数未归零或修改了共享计数")
	}
	providerCalls := 0
	eng := NewAgentEngine(scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		providerCalls++
		return reply("根任务可以继续")
	}), nil, EngineOptions{})
	_, err := eng.generate(child, r, []schema.Message{{Role: schema.RoleUser, Content: "试图在局部工具耗尽后补报告"}}, nil)
	var limit subtaskLimit
	if !errors.As(err, &limit) || limit.SubtaskLimitCode() != "subtask_tool_limit" || errors.Is(err, errBudget) {
		t.Fatalf("没有作为局部工具耗尽停止: %v", err)
	}
	if providerCalls != 0 || r.state.Usage.ModelCalls != 0 || r.state.Delegation.Subtasks["child"].ModelCalls != 0 {
		t.Fatal("局部工具耗尽后多调用或多扣了一次模型")
	}
	if _, err := eng.generate(ctx, r, []schema.Message{{Role: schema.RoleUser, Content: "根任务继续交付"}}, nil); err != nil || providerCalls != 1 {
		t.Fatalf("局部工具耗尽误停根任务: calls=%d err=%v", providerCalls, err)
	}
}
