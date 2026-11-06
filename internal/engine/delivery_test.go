package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type deliveryReporter struct{ messages int }

func (*deliveryReporter) OnThinking(context.Context)                         {}
func (*deliveryReporter) OnToolCall(context.Context, string, string)         {}
func (*deliveryReporter) OnToolResult(context.Context, string, string, bool) {}
func (r *deliveryReporter) OnMessage(context.Context, string)                { r.messages++ }

type cancelAfterToolReporter struct {
	deliveryReporter
	cancel context.CancelFunc
}

func (r *cancelAfterToolReporter) OnToolResult(context.Context, string, string, bool) { r.cancel() }

func deliveryWrite(id, path, content, version string) schema.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content, "expected_version": version})
	return schema.ToolCall{ID: id, Name: "write_file", Arguments: args}
}

func TestLastModelWriteHasDeliveryWithoutFalseCompletion(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		msg, err := modelCalls(deliveryWrite("写入", "result.txt", "内容", "missing"))
		msg.Content = "模型附带的完成声明不能成为终答"
		return msg, err
	})
	reporter := &deliveryReporter{}
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 1}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("最后写入", dir), TaskSpec{Goal: "创建结果"}, reporter)
	if err != nil || result.Status != BudgetExhausted || result.StopCode != "model_limit" || result.ExitCode() != 3 || calls != 1 || result.FinalText != "" || reporter.messages != 0 {
		t.Fatalf("终态或模型发布不符: %+v，调用 %d，发布 %d，错误 %v", result, calls, reporter.messages, err)
	}
	if result.Delivery == nil || !result.Delivery.HistoryComplete || len(result.Delivery.Effects) != 1 {
		t.Fatalf("缺少完整效果交付: %+v", result.Delivery)
	}
	receipt := result.Delivery.Effects[0]
	if receipt.ActionID == "" || receipt.Status != schema.ToolSucceeded || receipt.Effect != schema.EffectConfirmed || receipt.File == nil || receipt.File.Path != "result.txt" || receipt.File.BeforeVersion != "missing" || receipt.File.AfterVersion != fmt.Sprintf("%x", sha256.Sum256([]byte("内容"))) {
		t.Fatalf("确认回执错误: %+v", receipt)
	}
	state := readPlanSnapshot(t, dir, result.TaskID)
	if state.LastResult.Delivery.Effects[0].ActionID != receipt.ActionID || len(state.Pending) != 0 {
		t.Fatal("交付结果未持久化，或成功动作仍待处理")
	}
	if err := os.Remove(filepath.Join(dir, "result.txt")); err != nil {
		t.Fatal(err)
	}
	text := FormatRunResult(result)
	if !strings.Contains(text, "当时已确认") || !strings.Contains(text, "尚未取得有效验收证据") || strings.Contains(text, "模型附带") {
		t.Fatalf("终报声称当前完成: %s", text)
	}
}

func TestDeliveryDistinguishesRejectedConflictAndNoChange(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        tools.Mode
		version     string
		wantEffects int
		effect      schema.EffectStatus
	}{
		{"权限拒绝", tools.ModeReadOnly, "missing", 0, ""},
		{"版本冲突", tools.ModeWorkspace, "missing", 1, schema.EffectNone},
		{"确认无变化", tools.ModeWorkspace, fmt.Sprintf("%x", sha256.Sum256([]byte("原文"))), 1, schema.EffectConfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "原文")
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return modelCalls(deliveryWrite("写入", "source.txt", "原文", tc.version))
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tc.mode, Budget: Budget{MaxModelCalls: 1}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("效果", dir), TaskSpec{Goal: "写入"}, nil)
			if err != nil || result.Delivery == nil || len(result.Delivery.Effects) != tc.wantEffects {
				t.Fatalf("结果: %+v %v", result, err)
			}
			if tc.wantEffects > 0 {
				e := result.Delivery.Effects[0]
				if e.Effect != tc.effect {
					t.Fatalf("效果: %+v", e)
				}
				if tc.effect == schema.EffectNone && e.File != nil {
					t.Fatal("拒绝被计为文件改动")
				}
				if tc.effect == schema.EffectConfirmed && (e.File == nil || e.File.BeforeVersion != e.File.AfterVersion || !strings.Contains(FormatRunResult(result), "确认无变化")) {
					t.Fatal("无变化被计为修改")
				}
			}
		})
	}
}

func TestDeliveryPreservesUnknownShellAfterSuccessAndResume(t *testing.T) {
	dir := t.TempDir()
	sess := ctxpkg.NewSession("命令回执", dir)
	call := schema.ToolCall{ID: "命令", Name: "bash", Arguments: json.RawMessage(`{"command":"printf x >> proof.txt"}`)}
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1, 2:
			return modelCalls(call)
		default:
			return reply("命令已结束，效果待验收。")
		}
	})
	firstEngine := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted, Budget: Budget{MaxModelCalls: 1}})
	first, err := firstEngine.Run(context.Background(), sess, TaskSpec{Goal: "追加一次"}, nil)
	if err != nil || len(first.Delivery.Effects) != 1 {
		t.Fatalf("首次结果: %+v %v", first, err)
	}
	effect := first.Delivery.Effects[0]
	if effect.Status != schema.ToolSucceeded || effect.Effect != schema.EffectUnknown || effect.File != nil || !strings.Contains(FormatRunResult(first), "命令已结束，文件效果未核实") {
		t.Fatalf("丢失命令未知效果: %+v", effect)
	}
	if len(readPlanSnapshot(t, dir, first.TaskID).Pending) != 0 {
		t.Fatal("场景应为已结束命令，不依赖 Pending")
	}
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted, Budget: Budget{MaxModelCalls: 3}})
	resumed, err := eng.Run(context.Background(), sess, TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || len(resumed.Delivery.Effects) != 1 || resumed.Delivery.Effects[0].ActionID != effect.ActionID {
		t.Fatalf("恢复重复登记效果: %+v %v", resumed, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "proof.txt"))
	if err != nil || string(data) != "x" {
		t.Fatalf("命令被重复执行: %q %v", data, err)
	}
}

func TestLegacyDeliveryKeepsHistoryGapAndReconciles(t *testing.T) {
	dir := t.TempDir()
	sess := ctxpkg.NewSession("旧日志", dir)
	call := deliveryWrite("旧写入", "source.txt", "已写入", "missing")
	version := fmt.Sprintf("%x", sha256.Sum256([]byte("已写入")))
	id := saveInterruptedTask(t, dir, sess, pendingAction{ID: "旧动作", Call: call,
		Before: &fileVersion{Path: "source.txt", Version: "missing"}, Expected: &fileVersion{Path: "source.txt", Version: version}})
	writeFixture(t, dir, "source.txt", "已写入")
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(deliveryWrite("新写入", "new.txt", "新内容", "missing"))
		}
		return reply("恢复后继续写入。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
	result, err := eng.Run(context.Background(), sess, TaskSpec{ID: id, Resume: true}, nil)
	if err != nil || result.Delivery.HistoryComplete || len(result.Delivery.Effects) != 2 || result.Delivery.Effects[0].ActionID != "旧动作" || result.Delivery.Effects[0].File.AfterVersion != version {
		t.Fatalf("旧历史缺口或恢复证据错误: %+v %v", result.Delivery, err)
	}
}

func TestStorageFailuresSuppressFinalTextAndExposeObservedEffects(t *testing.T) {
	for _, failKind := range []string{"action_result", "finished", "artifact"} {
		t.Run(failKind, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					r := currentRun(ctx)
					r.mu.Lock()
					persist := r.persist
					r.persist = func(kind string, s taskState) error {
						if kind == failKind {
							return errors.New("模拟磁盘故障")
						}
						return persist(kind, s)
					}
					if failKind == "artifact" {
						r.artifact = func(string, []byte) error { return errors.New("模拟证据故障") }
					}
					r.mu.Unlock()
					if failKind == "artifact" {
						return modelCalls(schema.ToolCall{ID: "大量输出", Name: "bash", Arguments: json.RawMessage(`{"command":"printf 'x%.0s' {1..14000}"}`)})
					}
					return modelCalls(deliveryWrite("写入", "proof.txt", "已写入", "missing"))
				}
				return reply("这段成功终答不能在存储失败时发布")
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("故障", dir), TaskSpec{Goal: "写入"}, nil)
			if err == nil || result.Status != Failed || result.StopCode != "storage_error" || result.FinalText != "" || result.Delivery == nil || len(result.Delivery.Effects) != 1 {
				t.Fatalf("存储故障结果: %+v %v", result, err)
			}
			text := FormatRunResult(result)
			if !strings.Contains(text, "仅为现场执行观察") || strings.Contains(text, "这段成功终答") {
				t.Fatalf("错误终报: %s", text)
			}
		})
	}
}

func TestInvalidToolTextNeverBecomesFinalText(t *testing.T) {
	dir := t.TempDir()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "未经执行的完成声明", ToolCalls: []schema.ToolCall{readCall("", "missing")}}, nil
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("无效工具", dir), TaskSpec{Goal: "核查"}, nil)
	if err != nil || result.Status != Blocked || result.FinalText != "" {
		t.Fatalf("工具声明污染终答: %+v %v", result, err)
	}
}

func TestCancellationRetainsConfirmedDelivery(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return modelCalls(deliveryWrite("写入", "proof.txt", "已写入", "missing"))
	})
	reporter := &cancelAfterToolReporter{cancel: cancel}
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := eng.Run(ctx, ctxpkg.NewSession("写后取消", dir), TaskSpec{Goal: "创建文件"}, reporter)
	if err != nil || result.Status != Cancelled || result.FinalText != "" || result.Delivery == nil || len(result.Delivery.Effects) != 1 || result.Delivery.Effects[0].Effect != schema.EffectConfirmed {
		t.Fatalf("取消丢失确认效果: %+v %v", result, err)
	}
}

func TestDeliveryPreservesDistinctWritesInEventOrder(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	firstVersion := fmt.Sprintf("%x", sha256.Sum256([]byte("第一版")))
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(deliveryWrite("首次", "proof.txt", "第一版", "missing"))
		}
		if calls == 2 {
			return modelCalls(deliveryWrite("再次", "proof.txt", "第二版", firstVersion))
		}
		return reply("修改已落盘，待验收。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("连续写入", dir), TaskSpec{Goal: "连续修改文件"}, nil)
	if err != nil || len(result.Delivery.Effects) != 2 {
		t.Fatalf("动作历史不符: %+v %v", result, err)
	}
	first, second := result.Delivery.Effects[0], result.Delivery.Effects[1]
	if first.ActionID == second.ActionID || first.File.AfterVersion != firstVersion || second.File.BeforeVersion != firstVersion || second.File.AfterVersion != fmt.Sprintf("%x", sha256.Sum256([]byte("第二版"))) {
		t.Fatal("同路径回执混淆或乱序")
	}
}
