package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestBudgetContextCoversPlanningThinkingAndSubagents(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "证据\n")
	calls := 0
	reporter := &deliveryReporter{}
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		const marker = "运行预算（程序快照）："
		if strings.Count(messages[0].Content, marker) != 1 {
			t.Fatalf("预算块缺失或累计: %s", messages[0].Content)
		}
		_, encoded, _ := strings.Cut(messages[0].Content, marker)
		encoded, _, _ = strings.Cut(encoded, "\n")
		var snapshot budgetSnapshot
		if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.ModelCallsRemaining != 8-calls || snapshot.RemainingMS <= 0 || snapshot.RemainingMS > 600000 {
			t.Fatalf("第 %d 次请求预算不符: %+v", calls, snapshot)
		}
		r := currentRun(ctx)
		r.mu.Lock()
		toolsRemaining := r.state.Limits.MaxToolCalls - r.state.Usage.ToolCalls
		r.mu.Unlock()
		if snapshot.ToolCallsRemaining != toolsRemaining {
			t.Fatalf("工具预算不符: %+v", snapshot)
		}
		switch calls {
		case 1:
			return reply(`[{"id":"investigate","goal":"核实源文件","depends_on":[]}]`)
		case 2, 6:
			return reply("准备核对证据。")
		case 3:
			return modelCalls(schema.ToolCall{ID: "委派", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"读取源文件","allowed_paths":["source.txt"]}`)})
		case 4:
			return modelCalls(readCall("读取", "source.txt"))
		case 5:
			version := fmt.Sprintf("%x", sha256.Sum256([]byte("证据\n")))
			return reply(fmt.Sprintf(`{"findings":"找到证据","evidence":[{"path":"source.txt","line":1,"version":%q}],"unverified":[]}`, version))
		case 7:
			return reply("已完成核对。")
		default:
			t.Fatal("发生预算外调用")
			return nil, nil
		}
	})
	registry := NewWorkspaceRegistry(dir)
	eng := NewAgentEngine(provider, registry, EngineOptions{Plan: true, EnableThinking: true, Budget: Budget{MaxModelCalls: 7, MaxToolCalls: 20}})
	registry.Register(tools.NewSubagentTool(eng, NewExplorerRegistry(dir), reporter))
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("预算覆盖", dir), TaskSpec{Goal: "委派核查文件", Delegation: "on"}, reporter)
	if err != nil || result.Status != Answered || result.Usage.ModelCalls != 7 || calls != 7 || reporter.messages != 0 || result.FinalText != "已完成核对。" {
		t.Fatalf("预算或文字发布结果不符: %+v %v，调用 %d，发布 %d", result, err, calls, reporter.messages)
	}
}

func TestBudgetContextCountsTowardCompactionWithoutMutatingHistory(t *testing.T) {
	messages := []schema.Message{{Role: schema.RoleSystem, Content: "规则"}, {Role: schema.RoleUser, Content: "问题"}}
	providerCalls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		providerCalls++
		return reply("答复")
	})
	eng := NewAgentEngine(provider, tools.NewRegistry(), EngineOptions{MaxContextBytes: 200})
	r := &runState{state: taskState{Limits: (Budget{}).defaults()}}
	if _, err := eng.compactor.CompactWithin(messages, nil); err != nil {
		t.Fatalf("原始请求应能放入预算: %v", err)
	}
	_, err := eng.generate(context.Background(), r, messages, nil)
	if !errors.Is(err, errContextBudget) || providerCalls != 0 || r.state.Usage.ModelCalls != 0 {
		t.Fatalf("预算块未被计入压缩: %v，调用 %d", err, providerCalls)
	}
	for i := 0; i < 3; i++ {
		request := withBudgetContext(messages, budgetSnapshot{ModelCallsRemaining: 3 - i})
		if strings.Count(request[0].Content, "运行预算（程序快照）") != 1 || messages[0].Content != "规则" {
			t.Fatal("预算快照污染原始历史")
		}
	}
}

func TestBudgetContextUsesEarlierParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		_, encoded, _ := strings.Cut(messages[0].Content, "运行预算（程序快照）：")
		encoded, _, _ = strings.Cut(encoded, "\n")
		var snapshot budgetSnapshot
		if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.RemainingMS <= 0 || snapshot.RemainingMS > 2000 {
			t.Fatalf("忽略父任务截止时间: %+v", snapshot)
		}
		return reply("答复")
	})
	eng := NewAgentEngine(provider, tools.NewRegistry(), EngineOptions{})
	r := &runState{state: taskState{Limits: (Budget{}).defaults()}, started: time.Now()}
	_, err := eng.generate(ctx, r, []schema.Message{{Role: schema.RoleUser, Content: "问题"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
}
