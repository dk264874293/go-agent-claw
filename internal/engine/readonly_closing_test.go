package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func listCall(id, path string) schema.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path})
	return schema.ToolCall{ID: id, Name: "list_dir", Arguments: args}
}

func TestReadonlyRepeatAcrossProgressClosesOnceWithoutPlanPatch(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "claw", "二进制占位")
	writeFixture(t, dir, "source.md", "已知文档\n")
	requests, mainCalls, plans, closing := 0, 0, 0, 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		switch modelStage(ctx) {
		case "planning":
			plans++
			if plans > 1 {
				return nil, errors.New("不应为已知局部阻塞额外规划")
			}
			return reply(`[{"id":"review","goal":"核对文档","depends_on":[]}]`)
		case "closing":
			closing++
			if len(defs) != 0 || strings.Contains(messages[0].Content, "工具额度已经耗尽") || !strings.Contains(messages[0].Content, "只读探索重复失败") {
				return nil, errors.New("禁工具收尾的原因或工具列表不正确")
			}
			return reply("已有文档读取依据；目录类型错误阻止继续探索，跨文档结论仍需核实。")
		default:
			mainCalls++
			if mainCalls > 9 {
				return reply("已在路径类型修正后完成核对。")
			}
			if mainCalls == 1 {
				return modelCalls(readCall("source", "source.md"))
			}
			if mainCalls == 2 || mainCalls == 7 || mainCalls == 9 {
				return modelCalls(listCall(fmt.Sprint(mainCalls), "claw"))
			}
			return modelCalls(listCall(fmt.Sprint(mainCalls), "."))
		}
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	session := ctxpkg.NewSession("只读重复错误", dir)
	first, err := eng.Run(context.Background(), session, TaskSpec{Goal: "分析文档", Plan: true}, nil)
	if err != nil || first.Status != Blocked || first.StopCode != "readonly_repeat" || first.ExitCode() != 2 || first.FinalText != "" || first.PartialReport == nil || first.PartialReport.Source != "model" || requests != 11 || first.Usage.ModelCalls != 11 || first.Usage.Turns != 10 || first.Usage.ToolCalls != 9 || plans != 1 || closing != 1 {
		t.Fatalf("已知局部阻塞结果不符: %+v %v requests=%d plans=%d closing=%d", first, err, requests, plans, closing)
	}
	state := readPlanSnapshot(t, dir, first.TaskID)
	if len(state.Plan) != 1 || state.Plan[0].Done || state.Verification != nil || len(state.ReadonlyClosings) != 1 {
		t.Fatal("收尾错误地完成计划/验收，或没有持久化")
	}
	for _, b := range state.ReadonlyFailures {
		if b.Count != 3 || b.LastActionID == "" || b.ErrorCode != tools.ErrPathNotDirectory {
			t.Fatalf("失败边界没有依据: %+v", b)
		}
	}
	second, err := eng.Run(context.Background(), session, TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || second.Status != Blocked || requests != 11 || second.PartialReport == nil || second.PartialReport.OriginRunID != first.RunID {
		t.Fatalf("恢复重复请求或丢失历史来源: %+v %v", second, err)
	}
	if err := os.Remove(filepath.Join(dir, "claw")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "claw"), 0700); err != nil {
		t.Fatal(err)
	}
	third, err := eng.Run(context.Background(), session, TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || third.Status != Answered || requests != 12 || third.PartialReport != nil {
		t.Fatalf("类型修正后不能恢复探索: %+v %v", third, err)
	}
}

func TestReadonlyClosingAdmissionFailureAndInvalidResponses(t *testing.T) {
	for _, mode := range []string{"empty", "tools", "request", "no_models", "no_turns", "started_storage", "finished_storage", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "claw", "文件")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if modelStage(ctx) != "closing" {
					if calls == 1 && strings.Contains(mode, "storage") {
						r := currentRun(ctx)
						r.mu.Lock()
						persist := r.persist
						kind := "readonly_closing_started"
						if mode == "finished_storage" {
							kind = "readonly_closing_finished"
						}
						r.persist = func(k string, s taskState) error {
							if k == kind {
								return errors.New("模拟收尾存储失败")
							}
							return persist(k, s)
						}
						r.mu.Unlock()
					}
					return modelCalls(listCall(fmt.Sprint(calls), "claw"))
				}
				switch mode {
				case "empty":
					return reply("")
				case "tools":
					return modelCalls(deliveryWrite("forbidden", "forbidden.txt", "x", "missing"))
				case "request":
					return nil, errors.New("模拟模型失败")
				case "cancel":
					cancel()
					return nil, context.Canceled
				default:
					return reply("部分分析，不代表完成。")
				}
			})
			budget := Budget{}
			if mode == "no_models" {
				budget.MaxModelCalls = 3
			}
			if mode == "no_turns" {
				budget.MaxTurns = 3
			}
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: budget})
			session := ctxpkg.NewSession(mode, dir)
			result, err := eng.Run(ctx, session, TaskSpec{Goal: "分析文档"}, nil)
			wantStatus, wantCode := Blocked, "readonly_repeat"
			if strings.Contains(mode, "storage") {
				wantStatus, wantCode = Failed, "storage_error"
				if !errors.Is(err, errStorage) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				wantStatus, wantCode = Cancelled, "cancelled"
			}
			if result.Status != wantStatus || result.StopCode != wantCode || result.FinalText != "" || result.PartialReport == nil {
				t.Fatalf("错误终态: %+v %v", result, err)
			}
			wantCalls := 4
			if mode == "no_models" || mode == "no_turns" || mode == "started_storage" {
				wantCalls = 3
			}
			if calls != wantCalls || result.Usage.ToolCalls != 3 {
				t.Fatalf("额外调用: %d %+v", calls, result.Usage)
			}
			if _, err := os.Stat(filepath.Join(dir, "forbidden.txt")); !os.IsNotExist(err) {
				t.Fatal("收尾执行了工具")
			}
			if mode == "empty" || mode == "tools" || mode == "request" {
				if result.PartialReport.Source != "recorded" {
					t.Fatal("不合格答复进入模型交付")
				}
				_, err = eng.Run(context.Background(), session, TaskSpec{ID: result.TaskID, Resume: true}, nil)
				if err != nil || calls != wantCalls {
					t.Fatal("恢复重新请求失败收尾")
				}
			}
		})
	}
}

func readonlyStateFixture() taskState {
	return taskState{Version: 1, Spec: TaskSpec{Goal: "分析", Kind: TaskAnswer, Mode: tools.ModeReadOnly}, Limits: (Budget{}).defaults(),
		ReadonlyStopID: "boundary", ReadonlyFailures: map[string]*ReadonlyFailureBoundary{"key": {ID: "boundary", Key: "key", Tool: "list_dir", Path: "claw", ErrorCode: tools.ErrPathNotDirectory, Count: 3, LastActionID: "action", Stopped: true}}}
}

func TestReadonlyClosingWhitelistAndInterruptedResume(t *testing.T) {
	for _, mode := range []string{"generic", "permission", "unknown", "pending", "change", "trusted", "cancelled", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "claw", "文件")
			state := readonlyStateFixture()
			ctx, cancel := context.WithCancel(tools.WithScope(context.Background(), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir}))
			defer cancel()
			code := "readonly_repeat"
			switch mode {
			case "generic":
				code = "blocked"
			case "permission":
				state.ReadonlyFailures["key"].ErrorCode = "permission_denied"
			case "unknown":
				state.EffectLedger = &Delivery{Effects: []EffectReceipt{{Effect: schema.EffectUnknown}}}
			case "pending":
				state.Pending = map[string]pendingAction{"x": {}}
			case "change":
				state.Spec.Kind = TaskChange
			case "trusted":
				state.Spec.Mode = tools.ModeTrusted
			case "cancelled":
				cancel()
			case "interrupted":
				state.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": {Status: "started", OriginRunID: "old-run"}}
			}
			r := &runState{state: state, started: time.Now(), runID: "run"}
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				t.Fatal("不应请求模型")
				return nil, nil
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
			partial, err := eng.partialForStop(ctx, r, ctxpkg.NewSession("whitelist", dir), schema.Message{}, RunResult{Status: Blocked, StopCode: code, Reason: "停止"})
			if err != nil || partial == nil || partial.Source != "recorded" || r.state.Usage.ModelCalls != 0 {
				t.Fatalf("白名单失效: %+v %v", partial, err)
			}
			if mode == "interrupted" && !strings.Contains(partial.Text, "结果未确认") {
				t.Fatal("中断缺口未展示")
			}
		})
	}
}

func TestReadonlyClosingPersistedAdmissionDoesNotReissue(t *testing.T) {
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "claw", "文件")
	session := ctxpkg.NewSession("持久收尾中断", dir)
	state := readonlyStateFixture()
	id := newID()
	state.Spec.ID = id
	state.SessionID = session.ID
	state.WorkDir = canonical
	state.Usage = Usage{ModelCalls: 4, Turns: 4, ToolCalls: 3}
	state.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": {Status: "started", OriginRunID: "old-run"}}
	// started 事件仍可能携带上次非只读阻塞的 LastResult，不能覆盖当前已确认停止边界。
	state.LastResult = &RunResult{Status: BudgetExhausted, StopCode: "model_limit", RunID: "earlier-run"}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = task.Append("readonly_closing_started", state); err != nil {
		t.Fatal(err)
	}
	if err = task.SaveSnapshot(state); err != nil {
		t.Fatal(err)
	}
	task.Close()
	store.Close()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("中断后重复请求")
		return nil, nil
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), session, TaskSpec{ID: id, Resume: true}, nil)
	if err != nil || result.Status != Blocked || result.StopCode != "readonly_repeat" || result.Usage.ModelCalls != 4 || result.PartialReport == nil || !strings.Contains(result.PartialReport.Text, "结果未确认") {
		t.Fatalf("恢复错误: %+v %v", result, err)
	}
}

func TestFirstReadonlyClosingAfterFreshSessionHasPersistedEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "claw", "文件")
	writeFixture(t, dir, "source.md", "跨进程证据标记\n")
	calls := 0
	provider := scriptedProvider(func(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if modelStage(ctx) == "closing" {
			if !strings.Contains(msgs[0].Content, "跨进程证据标记") || !strings.Contains(msgs[0].Content, "仅说明历史观察") {
				return nil, errors.New("新会话收尾没有持久证据线索")
			}
			if len(currentRun(ctx).catalogSnapshot().receipts) != 0 {
				return nil, errors.New("旧回执被提升为当前读取")
			}
			return reply("历史已读取 source.md，当前版本仍需核实。")
		}
		if calls == 1 {
			return modelCalls(readCall("read", "source.md"))
		}
		return modelCalls(listCall(fmt.Sprint(calls), "claw"))
	})
	firstEngine := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 4}})
	first, err := firstEngine.Run(context.Background(), ctxpkg.NewSession("fresh", dir), TaskSpec{Goal: "分析"}, nil)
	if err != nil || first.Status != Blocked || first.PartialReport == nil || first.PartialReport.Source != "recorded" || calls != 4 {
		t.Fatalf("初始额度场景不符: %+v %v", first, err)
	}
	secondEngine := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 5}})
	second, err := secondEngine.Run(context.Background(), ctxpkg.NewSession("fresh", dir), TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || second.Status != Blocked || second.PartialReport == nil || second.PartialReport.Source != "model" || calls != 5 {
		t.Fatalf("跨进程恢复丢失证据: %+v %v", second, err)
	}
}

type directoryChangeRegistry struct {
	tools.Registry
	dir    string
	failed chan struct{}
}

func (r *directoryChangeRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	if call.ID == "third" {
		result := r.Registry.Execute(ctx, call)
		close(r.failed)
		return result
	}
	if call.ID == "recovered" {
		select {
		case <-r.failed:
		case <-ctx.Done():
			return schema.NewToolResult(call.ID, "取消", schema.ToolCancelled, schema.EffectNone, "取消", nil)
		}
		if err := os.Remove(filepath.Join(r.dir, "claw")); err != nil {
			panic(err)
		}
		if err := os.Mkdir(filepath.Join(r.dir, "claw"), 0700); err != nil {
			panic(err)
		}
	}
	return r.Registry.Execute(ctx, call)
}

func TestReadonlyFailureFollowedBySuccessInSameBatchContinues(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "claw", "文件")
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1, 2:
			return modelCalls(listCall(fmt.Sprint(calls), "claw"))
		case 3:
			return modelCalls(listCall("third", "claw"), listCall("recovered", "claw"))
		default:
			return reply("目录类型已经修正，可继续正常交付。")
		}
	})
	registry := &directoryChangeRegistry{Registry: NewWorkspaceRegistry(dir), dir: dir, failed: make(chan struct{})}
	eng := NewAgentEngine(provider, registry, EngineOptions{})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("批次内恢复", dir), TaskSpec{Goal: "分析"}, nil)
	if err != nil || result.Status != Answered || calls != 4 || result.PartialReport != nil {
		t.Fatalf("已解决边界仍导致停止: %+v %v", result, err)
	}
	state := readPlanSnapshot(t, dir, result.TaskID)
	if state.ReadonlyStopID != "" || len(state.ReadonlyClosings) != 0 {
		t.Fatal("重新激活了已解决边界")
	}
}
