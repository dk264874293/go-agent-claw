package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func observedRun(t *testing.T, eng *AgentEngine, session *ctxpkg.Session, spec TaskSpec) (RunResult, []observe.Event) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAW_OBSERVE", "on")
	t.Setenv("CLAW_OBSERVE_DIR", dir)
	ctx, attempt := observe.StartFromEnv(context.Background(), eng.ObservationOptions(session.WorkDir, "test", spec))
	id := observe.Identity(ctx).AttemptID
	result, err := eng.Run(ctx, session, spec, nil)
	ctx = observe.WithTask(ctx, result.TaskID)
	attempt.Returned(ObservationResult(result, err))
	observe.Emit(ctx, "delivery_attempted", observe.Payload{Status: "printed"})
	attempt.Close()
	f, err := os.Open(filepath.Join(dir, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []observe.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e observe.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result, events
}

func eventIndex(events []observe.Event, kind string) int {
	for i, e := range events {
		if e.EventType == kind {
			return i
		}
	}
	return -1
}

func TestObservedAttemptLifecycleAndCachedResult(t *testing.T) {
	dir := t.TempDir()
	session := ctxpkg.NewSession("观测", dir)
	calls := 0
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		return &schema.Message{Role: schema.RoleAssistant, Content: "不能进入观测日志的答复正文", Usage: &schema.Usage{PromptTokens: 17, CompletionTokens: 4}}, nil
	})
	eng := NewAgentEngine(observability.NewCostTracker(p, "测试模型", session), NewWorkspaceRegistry(dir), EngineOptions{})
	result, events := observedRun(t, eng, session, TaskSpec{Goal: "测试"})
	if result.Status != Answered {
		t.Fatal(result)
	}
	finalized, returned, delivery := eventIndex(events, "run_finalized"), eventIndex(events, "run_returned"), eventIndex(events, "delivery_attempted")
	if finalized < 0 || returned <= finalized || delivery <= returned {
		t.Fatalf("收尾顺序错误: %+v", events)
	}
	committed := 0
	var modelID string
	for _, e := range events {
		if e.EventType == "task_event_committed" {
			committed++
			if e.SourceTaskSequence == 0 {
				t.Fatal("提交缺少序号")
			}
		}
		if e.EventType == "model_started" {
			modelID = e.ModelCallID
		}
		if e.EventType == "model_usage" && (modelID == "" || e.ModelCallID != modelID || e.Payload.InputTokens == nil || *e.Payload.InputTokens != 17 || e.Payload.Cost != nil) {
			t.Fatalf("用量关联错误: %+v", e)
		}
		encoded, _ := json.Marshal(e)
		if strings.Contains(string(encoded), "答复正文") {
			t.Fatal("泄漏答复正文")
		}
	}
	if committed == 0 {
		t.Fatal("缺少持久提交事件")
	}
	// 收尾完成后独占锁已释放。
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	cached, next := observedRun(t, eng, session, TaskSpec{ID: result.TaskID, Resume: true})
	if calls != 1 || cached.RunID != result.RunID || next[0].AttemptID == events[0].AttemptID || eventIndex(next, "model_started") >= 0 {
		t.Fatal("缓存返回身份或模型调用错误")
	}
	last := next[eventIndex(next, "run_returned")]
	if last.Payload.ResultRunID != result.RunID {
		t.Fatal("丢失结果身份")
	}
}

func TestObservedEarlyExitAndDisabledParity(t *testing.T) {
	for _, badBudget := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "不存在")
		opts := EngineOptions{}
		if badBudget {
			dir = t.TempDir()
			opts.Budget.MaxModelCalls = -1
		}
		eng := NewAgentEngine(scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			t.Fatal("不应调用模型")
			return nil, nil
		}), NewWorkspaceRegistry(dir), opts)
		result, events := observedRun(t, eng, ctxpkg.NewSession("早退", dir), TaskSpec{Goal: "测试"})
		if result.Status != Failed || eventIndex(events, "run_finalized") < 0 || eventIndex(events, "run_returned") < 0 || eventIndex(events, "model_started") >= 0 {
			t.Fatal("早退未记录或错误调用")
		}
	}
	var outcomes []RunResult
	for _, enabled := range []bool{false, true} {
		dir := t.TempDir()
		p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			return modelCalls(schema.ToolCall{ID: "write", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.txt","content":"正文不可进入观测"}`)})
		})
		eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 1}})
		session := ctxpkg.NewSession("边界", dir)
		spec := TaskSpec{Goal: "写入", Kind: TaskChange}
		var result RunResult
		if enabled {
			var events []observe.Event
			result, events = observedRun(t, eng, session, spec)
			for _, e := range events {
				b, _ := json.Marshal(e)
				if strings.Contains(string(b), "正文不可进入观测") {
					t.Fatal("泄漏写入正文")
				}
			}
		} else {
			result, _ = eng.Run(context.Background(), session, spec, nil)
		}
		outcomes = append(outcomes, result)
		if data, err := os.ReadFile(filepath.Join(dir, "result.txt")); err != nil || string(data) != "正文不可进入观测" {
			t.Fatal("写入缺失")
		}
	}
	a, b := outcomes[0], outcomes[1]
	if a.Status != BudgetExhausted || a.Status != b.Status || a.StopCode != b.StopCode || a.Usage.ModelCalls != b.Usage.ModelCalls || a.Usage.ToolCalls != b.Usage.ToolCalls || len(a.Delivery.Effects) != len(b.Delivery.Effects) {
		t.Fatalf("观测改变执行结果: %+v %+v", a, b)
	}
}

func TestObservedRejectedToolDoesNotStartHandler(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(schema.ToolCall{ID: "write", Name: "write_file", Arguments: json.RawMessage(`{"path":"x","content":"不可写"}`)})
		}
		return reply("已拒绝")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly})
	_, events := observedRun(t, eng, ctxpkg.NewSession("拒绝", dir), TaskSpec{Goal: "测试"})
	if eventIndex(events, "tool_attempted") < 0 || eventIndex(events, "tool_handler_started") >= 0 {
		t.Fatal("拒绝动作误报执行")
	}
	e := events[eventIndex(events, "tool_admission")]
	if e.Payload.Decision != "rejected" || e.Payload.Stage != "permission" {
		t.Fatalf("准入分类错误: %+v", e)
	}
}

func TestObservedSubagentIdentity(t *testing.T) {
	dir := t.TempDir()
	rootCalls := 0
	p := scriptedProvider(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if strings.HasPrefix(msgs[0].Content, "你是只读代码探路者") {
			return reply(`{"findings":"没有证据","evidence":[],"unverified":[]}`)
		}
		rootCalls++
		if rootCalls == 1 {
			return modelCalls(schema.ToolCall{ID: "delegate", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"只读调查"}`)})
		}
		return reply("调查结束")
	})
	r := NewWorkspaceRegistry(dir)
	eng := NewAgentEngine(p, r, EngineOptions{})
	r.Register(tools.NewSubagentTool(eng, NewExplorerRegistry(dir), nil))
	result, events := observedRun(t, eng, ctxpkg.NewSession("子任务观测", dir), TaskSpec{Goal: "调查", Delegation: "on"})
	if result.Status != Answered || result.Usage.ModelCalls != 3 {
		t.Fatal(result)
	}
	queued, started, finished := eventIndex(events, "agent_queued"), eventIndex(events, "agent_started"), eventIndex(events, "agent_finished")
	if queued < 0 || started <= queued || finished <= started {
		t.Fatal("缺少子任务生命周期")
	}
	e := events[started]
	if e.AgentID == "" || e.ParentAgentID != events[0].AgentID || e.AgentID == e.ParentAgentID || e.Payload.ParentActionID == "" {
		t.Fatalf("子任务关联错误: %+v", e)
	}
	for _, child := range events {
		if child.EventType == "model_started" && child.AgentID == e.AgentID && child.ActionID != "" {
			t.Fatal("子任务错误继承委派动作")
		}
	}
}

func TestObservedResumeAndWorkspaceBusy(t *testing.T) {
	dir := t.TempDir()
	session := ctxpkg.NewSession("恢复", dir)
	calls := 0
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(schema.ToolCall{ID: "write", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.txt","content":"完成"}`)})
		}
		return reply("已经写入，等待验收")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 1}})
	first, events := observedRun(t, eng, session, TaskSpec{Goal: "写入", Kind: TaskChange})
	eng = NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 3}})
	second, resumed := observedRun(t, eng, session, TaskSpec{ID: first.TaskID, Resume: true})
	if first.Status != BudgetExhausted || second.Status != NeedsVerification || first.RunID == second.RunID || events[0].AttemptID == resumed[0].AttemptID || len(second.Delivery.Effects) != 1 || calls != 2 {
		t.Fatalf("恢复观测改变行为: %+v %+v", first, second)
	}
	configured := resumed[eventIndex(resumed, "run_configured")]
	if configured.Payload.ModelCalls != 1 || configured.Payload.MaxModelCalls != 3 {
		t.Fatalf("恢复额度错误: %+v", configured)
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	busy, attempt := observedRun(t, eng, session, TaskSpec{Goal: "新任务"})
	if busy.Status != Blocked || eventIndex(attempt, "run_returned") < 0 || eventIndex(attempt, "model_started") >= 0 {
		t.Fatal("工作区占用早退未记录")
	}
}

func TestObservationStorageFailureDoesNotChangeEngineResult(t *testing.T) {
	dir := t.TempDir()
	observationPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(observationPath, []byte("占用"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, attempt := observe.Start(context.Background(), observe.Options{WorkDir: dir, Entry: "test"}, observe.Config{Dir: observationPath})
	eng := NewAgentEngine(scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return reply("正常完成")
	}), NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(ctx, ctxpkg.NewSession("观测故障", dir), TaskSpec{Goal: "回答"}, nil)
	attempt.Returned(ObservationResult(result, err))
	attempt.Close()
	if err != nil || result.Status != Answered || result.Usage.ModelCalls != 1 {
		t.Fatalf("纯观测故障影响任务: %+v %v", result, err)
	}
}
