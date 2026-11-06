package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func assertClosedBudget(t *testing.T, result RunResult, err error, models, tools int, source string) {
	t.Helper()
	if err != nil || result.Status != BudgetExhausted || result.StopCode != "tool_limit" || result.ExitCode() != 3 || result.FinalText != "" || result.Usage.ModelCalls != models || result.Usage.ToolCalls != tools || result.PartialReport == nil || result.PartialReport.Source != source {
		t.Fatalf("收尾结果不符: %+v，partial=%+v，错误=%v", result, result.PartialReport, err)
	}
}

func TestToolBudgetClosingIsOneToollessTurnAndResumable(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "证据\n")
	session := ctxpkg.NewSession("预算收尾", dir)
	requests := 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		if requests%2 == 1 {
			return modelCalls(readCall("read", "source.txt"))
		}
		if modelStage(ctx) != "closing" || len(defs) != 0 || !strings.Contains(messages[0].Content, "工具额度已经耗尽") {
			t.Fatalf("收尾没有禁用探索: stage=%s tools=%d", modelStage(ctx), len(defs))
		}
		// 请求中的每条工具观察都应有前置调用，包括未执行调用的拒绝回执。
		pairs := map[string]bool{}
		for _, m := range messages {
			for _, call := range m.ToolCalls {
				pairs[call.ID] = true
			}
			if m.ToolCallID != "" && !pairs[m.ToolCallID] {
				t.Fatalf("孤立观察: %+v", m)
			}
		}
		return reply("已确认读取 source.txt:1，跨文档结论尚未核实。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 1, MaxModelCalls: 5}})
	first, err := eng.Run(context.Background(), session, TaskSpec{Goal: "分析文件"}, nil)
	assertClosedBudget(t, first, err, 2, 1, "model")
	if first.Usage.Turns != 2 || first.PartialReport.OriginRunID != first.RunID {
		t.Fatalf("收尾未占一轮或身份错误: %+v", first)
	}
	saved := readPlanSnapshot(t, dir, first.TaskID)
	if saved.BudgetClosing == nil || saved.BudgetClosing.Status != "completed" || saved.LastResult.PartialReport == nil {
		t.Fatal("收尾未保存")
	}
	resume := TaskSpec{ID: first.TaskID, Resume: true}
	second, err := eng.Run(context.Background(), session, resume, nil)
	assertClosedBudget(t, second, err, 2, 1, "model")
	if requests != 2 || second.RunID == first.RunID || second.PartialReport.OriginRunID != first.RunID {
		t.Fatal("恢复重发请求或丢失来源")
	}
	// 仅提高模型额度不能在同一工具边界再次收尾。
	moreModels := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 1, MaxModelCalls: 6}})
	third, err := moreModels.Run(context.Background(), session, resume, nil)
	assertClosedBudget(t, third, err, 2, 1, "model")
	if requests != 2 {
		t.Fatal("同一边界重复收尾")
	}
	// 调高工具额度后可以继续探索并在新边界收尾。
	moreTools := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 2, MaxModelCalls: 6}})
	fourth, err := moreTools.Run(context.Background(), session, resume, nil)
	assertClosedBudget(t, fourth, err, 4, 2, "model")
	if requests != 4 || fourth.PartialReport.OriginRunID != fourth.RunID {
		t.Fatalf("新边界没有继续执行: %+v", fourth)
	}
}

func TestClosingInvalidResponseNeverExecutesOrRetries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *schema.Message
		err      error
	}{
		{"空答复", &schema.Message{}, nil},
		{"没有消息", nil, nil},
		{"请求失败", nil, errors.New("模拟请求错误")},
		{"再次调用工具", &schema.Message{Content: "完成声明", ToolCalls: []schema.ToolCall{deliveryWrite("不能执行", "forbidden.txt", "x", "missing")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "证据")
			calls := 0
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return modelCalls(readCall("read", "source.txt"))
				}
				return tc.response, tc.err
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxToolCalls: 1, MaxModelCalls: 5}})
			session := ctxpkg.NewSession(tc.name, dir)
			result, err := eng.Run(context.Background(), session, TaskSpec{Goal: "分析"}, nil)
			assertClosedBudget(t, result, err, 2, 1, "recorded")
			if calls != 2 || strings.Contains(result.PartialReport.Text, "完成声明") {
				t.Fatal("无效答复进入交付或出现重试")
			}
			if _, err := os.Stat(filepath.Join(dir, "forbidden.txt")); !os.IsNotExist(err) {
				t.Fatal("收尾执行了工具")
			}
			again, err := eng.Run(context.Background(), session, TaskSpec{ID: result.TaskID, Resume: true}, nil)
			assertClosedBudget(t, again, err, 2, 1, "recorded")
			if calls != 2 {
				t.Fatal("失败收尾被重新请求")
			}
		})
	}
}

func TestClosingRequiresRemainingModelTurnAndTime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget Budget
		expire bool
	}{
		{"模型耗尽", Budget{MaxToolCalls: 1, MaxModelCalls: 1}, false},
		{"轮数耗尽", Budget{MaxToolCalls: 1, MaxTurns: 1}, false},
		{"时间耗尽", Budget{MaxToolCalls: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "证据")
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if tc.expire {
					r := currentRun(ctx)
					r.mu.Lock()
					r.elapsed = r.state.Limits.Timeout
					r.mu.Unlock()
				}
				return modelCalls(readCall("read", "source.txt"))
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: tc.budget})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession(tc.name, dir), TaskSpec{Goal: "分析"}, nil)
			assertClosedBudget(t, result, err, 1, 1, "recorded")
			if calls != 1 || readPlanSnapshot(t, dir, result.TaskID).BudgetClosing != nil {
				t.Fatal("没有余量仍准入收尾")
			}
		})
	}
}

func TestClosingStorageErrorsSuppressRequestOrSavedAnswer(t *testing.T) {
	for _, failKind := range []string{"budget_closing_started", "budget_closing_finished"} {
		t.Run(failKind, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "证据")
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					r := currentRun(ctx)
					r.mu.Lock()
					persist := r.persist
					r.persist = func(kind string, s taskState) error {
						if kind == failKind {
							return errors.New("模拟存储失败")
						}
						return persist(kind, s)
					}
					r.mu.Unlock()
					return modelCalls(readCall("read", "source.txt"))
				}
				return reply("模型部分分析")
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 1}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession(failKind, dir), TaskSpec{Goal: "分析"}, nil)
			if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" || result.FinalText != "" || result.PartialReport == nil || result.PartialReport.Source != "recorded" {
				t.Fatalf("存储失败出口错误: %+v %v", result, err)
			}
			if failKind == "budget_closing_started" && calls != 1 {
				t.Fatal("准入未可靠保存仍调用模型")
			}
			if failKind == "budget_closing_finished" && calls != 2 {
				t.Fatal("收尾调用计数错误")
			}
		})
	}
}

func TestInterruptedClosingAdmissionIsNotRequestedOnResume(t *testing.T) {
	dir := t.TempDir()
	session := ctxpkg.NewSession("中断收尾", dir)
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := newID()
	task, err := store.CreateTask(id)
	if err != nil {
		t.Fatal(err)
	}
	state := taskState{Version: 1, Spec: TaskSpec{ID: id, Goal: "分析", Kind: TaskAnswer, Mode: tools.ModeReadOnly}, WorkDir: canonicalDir, SessionID: session.ID,
		Limits: Budget{MaxTurns: 30, MaxModelCalls: 60, MaxToolCalls: 1, Timeout: time.Minute}, Usage: Usage{ModelCalls: 2, ToolCalls: 1, Turns: 2},
		BudgetClosing: &BudgetClosing{OriginRunID: "interrupted-run", ToolLimit: 1, Status: "started"}}
	if _, err := task.Append("budget_closing_started", state); err != nil {
		t.Fatal(err)
	}
	if err := task.SaveSnapshot(state); err != nil {
		t.Fatal(err)
	}
	task.Close()
	store.Close()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("中断收尾重新调用了模型")
		return nil, nil
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), session, TaskSpec{ID: id, Resume: true}, nil)
	assertClosedBudget(t, result, err, 2, 1, "recorded")
	if !strings.Contains(result.PartialReport.Text, "结果未确认") {
		t.Fatalf("未展示中断缺口: %+v", result.PartialReport)
	}
}

func TestReserveClosingAtomicPersistenceAndLimits(t *testing.T) {
	r := &runState{runID: "run", started: time.Now(), state: taskState{Limits: Budget{MaxTurns: 2, MaxModelCalls: 2, MaxToolCalls: 1, Timeout: time.Minute}, Usage: Usage{Turns: 1, ModelCalls: 1, ToolCalls: 1}}}
	commits := 0
	r.persist = func(kind string, s taskState) error {
		commits++
		if kind != "budget_closing_started" || s.Usage.ModelCalls != 2 || s.Usage.Turns != 2 || s.BudgetClosing == nil || s.BudgetClosing.Status != "started" {
			t.Fatalf("准入不是原子事件: %s %+v", kind, s)
		}
		return nil
	}
	if err := r.reserveClosing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.reserveClosing(context.Background()); !errors.Is(err, errClosingUsed) {
		t.Fatalf("重复准入: %v", err)
	}
	if commits != 1 {
		t.Fatal("重复扣费或提交")
	}
	encoded, _ := json.Marshal(r.state)
	var restored taskState
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.BudgetClosing == nil {
		t.Fatal("新状态不能持久恢复")
	}
}

func TestElapsedPersistenceCoversClosingRequest(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "证据")
	requests := 0
	provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		if requests == 1 {
			return modelCalls(readCall("read", "source.txt"))
		}
		r := currentRun(ctx)
		r.mu.Lock()
		persist := r.persist
		r.persist = func(kind string, s taskState) error {
			if kind == "elapsed" {
				return errors.New("模拟收尾期间的计时写入失败")
			}
			return persist(kind, s)
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(4 * time.Second):
			return nil, errors.New("收尾期间计时器没有运行")
		}
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 1}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("收尾计时", dir), TaskSpec{Goal: "分析"}, nil)
	if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" || requests != 2 || result.FinalText != "" || result.PartialReport == nil {
		t.Fatalf("收尾计时故障未覆盖: %+v %v", result, err)
	}
}

func TestClosingCancellationKeepsCancellationStatus(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "证据")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("read", "source.txt"))
		}
		cancel()
		return nil, context.Canceled
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxToolCalls: 1}})
	result, err := eng.Run(ctx, ctxpkg.NewSession("取消收尾", dir), TaskSpec{Goal: "分析"}, nil)
	if err != nil || result.Status != Cancelled || result.ExitCode() != 130 || result.FinalText != "" || calls != 2 || result.PartialReport == nil || result.PartialReport.Source != "recorded" {
		t.Fatalf("取消被收尾掩盖: %+v %v", result, err)
	}
}

func TestStorageFailureLatchesBeforeAnyNewAdmission(t *testing.T) {
	r := &runState{state: taskState{Limits: (Budget{}).defaults()}}
	r.persist = func(string, taskState) error { return errors.New("模拟事件失败") }
	if err := r.record("action_result", func(*taskState) error { return nil }); !errors.Is(err, errStorage) {
		t.Fatal(err)
	}
	for _, kind := range []string{"turn", "model", "tool"} {
		if err := r.reserve(context.Background(), kind); !errors.Is(err, errStorage) {
			t.Fatalf("存储失败后仍准入 %s: %v", kind, err)
		}
	}
	if r.state.Usage.ModelCalls != 0 || r.state.Usage.ToolCalls != 0 || r.state.Usage.Turns != 0 {
		t.Fatalf("存储失败后仍扣费: %+v", r.state.Usage)
	}
}

func TestRecordedPartialCountsUnknownEffectsBeyondPending(t *testing.T) {
	r := &runState{state: taskState{Pending: map[string]pendingAction{"pending": {}}, EffectLedger: &Delivery{Effects: []EffectReceipt{
		{ActionID: "pending", Effect: schema.EffectUnknown},
		{ActionID: "shell-exited", Status: schema.ToolSucceeded, Effect: schema.EffectUnknown},
		{ActionID: "no-change", Effect: schema.EffectConfirmed},
	}}}}
	partial := r.recordedPartial("停止", "")
	if !strings.Contains(partial.Text, "2 个动作效果仍待核实") {
		t.Fatalf("未知效果被Pending掩盖或重复计算: %s", partial.Text)
	}
}
