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

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDeliveryReviewBoundaryMixedBatchPreservesRejectionsAndHistoryWithoutHandlers(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "源材料。\n")
	writeFixture(t, dir, "b.md", "本批尚不应读取。\n")
	writeFixture(t, dir, "prior.md", "历史确认产物")
	history := schema.ToolCall{ID: "history", Name: "write_file", Arguments: json.RawMessage(`{"path":"prior.md","content":"历史确认产物","expected_version":"missing"}`)}
	calls := 0
	p := scriptedProvider(func(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("initial", "a.md"))
		case 2:
			r := currentRun(ctx)
			r.mu.Lock()
			r.resumeReceipts = map[string]schema.ToolResult{actionKey("", history): schema.NewToolResult(history.ID, "已复用历史确认回执", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)}
			r.mu.Unlock()
			return modelCalls(readCall("fresh-read", "b.md"), schema.ToolCall{ID: "invalid", Name: "read_file", Arguments: json.RawMessage(`{"path":7}`)}, schema.ToolCall{ID: "denied", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`)}, history, reviewWriteCall("candidate", "同一业务内容", nil))
		case 3:
			if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("混合批次提前写入")
			}
			r := currentRun(ctx)
			if len(r.analysisRuntime.sources) != 1 || len(r.state.Pending) != 0 {
				t.Fatal("退回批次出现新读取或Pending")
			}
			got := map[string]string{}
			for _, m := range msgs {
				if m.ToolCallID != "" {
					got[m.ToolCallID] = m.Content
				}
			}
			if !strings.Contains(got["fresh-read"], "delivery_review_required") || !strings.Contains(got["candidate"], "delivery_review_required") {
				t.Fatalf("未整批退回新执行: %+v", got)
			}
			if strings.Contains(got["invalid"], "delivery_review_required") || !strings.Contains(got["invalid"], "参数") {
				t.Fatalf("参数拒绝被覆盖: %s", got["invalid"])
			}
			if strings.Contains(got["denied"], "delivery_review_required") || !strings.Contains(got["denied"], "不允许使用 bash") {
				t.Fatalf("权限拒绝被覆盖: %s", got["denied"])
			}
			if !strings.Contains(got["history"], "历史确认回执") {
				t.Fatal("历史回执未复用")
			}
			// 模型忽略清单直接重提；仍只退回一次，不强制填写假的发现。
			return modelCalls(reviewWriteCall("ignored-feedback", "同一业务内容", nil))
		case 4:
			return reply("已交付；未解决内容仍待核实。")
		}
		return nil, fmt.Errorf("交付处理重复：%d", calls)
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 4, MaxToolCalls: 20, MaxTurns: 4}})
	result, events := observedRun(t, eng, ctxpkg.NewSession("混合退回边界", dir), deliverySpec(tools.ModeWorkspace))
	if result.Status != NeedsVerification || calls != 4 || result.Usage.ToolCalls != 7 || result.DeliveryReview.AdmissionCount != 1 {
		t.Fatalf("结果或一次性边界错误: %+v", result)
	}
	deferred := 0
	reused := false
	newWrite := 0
	for _, e := range events {
		if e.EventType == "tool_handler_started" {
			switch e.Payload.ToolCallID {
			case "fresh-read", "candidate", "invalid", "denied", "history":
				t.Fatalf("退回/拒绝/历史调用进入Handler: %+v", e)
			case "ignored-feedback":
				newWrite++
			}
		}
		if e.EventType == "tool_finished" && e.Payload.ErrorClass == "delivery_review_required" {
			deferred++
		}
		if e.EventType == "receipt_reused" && e.Payload.ToolCallID == "history" {
			reused = true
		}
	}
	if deferred != 2 || !reused || newWrite != 1 {
		t.Fatalf("执行事件边界错误: deferred=%d reused=%v writes=%d", deferred, reused, newWrite)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "result.md"))
	if string(raw) != "同一业务内容" {
		t.Fatal("正常重新提交没有写入")
	}
	if result.DeliveryReview == nil || len(result.DeliveryReview.Unresolved) == 0 {
		t.Fatal("忽略清单被冒充全部解决")
	}
	compared := false
	for _, c := range result.DeliveryReview.ContentComparisons {
		if c.Path == "result.md" {
			compared = c.Status == "unchanged" && c.ActionID != ""
		}
		if c.Path == "prior.md" && (c.Status != "not_compared" || c.ActionID != "") {
			t.Fatalf("复用历史回执被当作本次新发布: %+v", c)
		}
	}
	if !compared {
		t.Fatalf("只重提相同正文却被误报修订: %+v", result.DeliveryReview.ContentComparisons)
	}
}

func TestDeliveryReviewBoundaryEventFailureNeverExecutesCandidate(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "来源。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("initial", "a.md"))
		}
		if calls == 2 {
			r := currentRun(ctx)
			r.mu.Lock()
			prior := r.persist
			r.persist = func(kind string, s taskState) error {
				if kind == "delivery_review_evaluated" {
					return errors.New("注入事件写入失败")
				}
				return prior(kind, s)
			}
			r.mu.Unlock()
			return modelCalls(reviewWriteCall("candidate", "不能在事件失败后写入", nil))
		}
		return nil, fmt.Errorf("存储失败后额外调用")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 5, MaxToolCalls: 8, MaxTurns: 5}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("交付事件失败", dir), deliverySpec(tools.ModeWorkspace), nil)
	if result.Status != Failed || result.StopCode != "storage_error" || calls != 2 {
		t.Fatalf("存储失败被吞掉或额外调用: %+v / %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("事件失败后候选提前写入")
	}
	if result.Delivery != nil && len(result.Delivery.Effects) != 0 {
		t.Fatalf("未执行候选生成效果: %+v", result.Delivery)
	}
}

func TestDeliveryReviewBoundaryExactToolBudgetKeepsLastWriteWithModelsRemaining(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "来源。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("source", "a.md"))
		}
		if calls == 2 {
			return modelCalls(reviewWriteCall("last-write", "工具额度恰好覆盖本次写入", nil))
		}
		if calls == 3 && modelStage(ctx) == "closing" {
			return reply("已确认最后写入；工具预算耗尽，任务未验收。")
		}
		return nil, fmt.Errorf("工具耗尽后多调用")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 6, MaxToolCalls: 2, MaxTurns: 6}})
	result, _ := eng.Run(context.Background(), ctxpkg.NewSession("工具票据精确边界", dir), deliverySpec(tools.ModeWorkspace), nil)
	if result.Status != BudgetExhausted || result.StopCode != "tool_limit" || calls != 3 || result.Usage.ModelCalls != 3 || result.Usage.ToolCalls != 2 {
		t.Fatalf("虚占最后工具额度: %+v calls=%d", result, calls)
	}
	if result.DeliveryReview.AdmissionCount != 0 || result.DeliveryReview.ErrorClass != "budget_unavailable" {
		t.Fatalf("错误准入额外请求: %+v", result.DeliveryReview)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "result.md"))
	if string(data) != "工具额度恰好覆盖本次写入" || len(result.Delivery.Effects) != 1 {
		t.Fatalf("最后正常写入丢失: %s %+v", data, result.Delivery)
	}
}

func TestDeliveryReviewBoundarySourceChangeAndResumeNeverResetOpportunity(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "原版本。\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("source", "a.md"))
		case 2:
			return modelCalls(reviewWriteCall("candidate", "候选", nil))
		case 3:
			if modelStage(ctx) != "delivery_review" {
				t.Fatal("唯一处理机会未标阶段")
			}
			writeFixture(t, dir, "a.md", "外部新版本。\n")
			return modelCalls(reviewWriteCall("publish", "旧来源已变化，保留待核实", nil))
		case 4:
			return reply("来源变化未验收。")
		}
		return nil, fmt.Errorf("来源变化重开机会")
	})
	eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 8, MaxToolCalls: 10, MaxTurns: 8}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("来源变化恢复", dir), deliverySpec(tools.ModeWorkspace), nil)
	if err != nil || result.Status != NeedsVerification || calls != 4 || result.DeliveryReview.AdmissionCount != 1 || len(result.DeliveryReview.Unresolved) == 0 {
		t.Fatalf("来源变化后重置机会或假完成: %+v / %v", result, err)
	}
	resumedCalls := 0
	eng.provider = scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		resumedCalls++
		if modelStage(ctx) == "delivery_review" {
			t.Fatal("恢复重新准入处理请求")
		}
		if resumedCalls == 1 {
			return modelCalls(readCall("new-source", "a.md"))
		}
		if resumedCalls == 2 {
			return reply("当前源是新版本；此前交付处理保留历史状态。")
		}
		return nil, fmt.Errorf("恢复重复处理")
	})
	resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("来源变化恢复", dir), TaskSpec{ID: result.TaskID, Resume: true, Mode: tools.ModeWorkspace}, nil)
	if err != nil || resumed.Status != NeedsVerification || resumedCalls != 2 || resumed.DeliveryReview.AdmissionCount != 1 {
		t.Fatalf("恢复重置额度机会: %+v calls=%d / %v", resumed, resumedCalls, err)
	}
	if resumed.DeliveryReview.OriginRunID != result.DeliveryReview.OriginRunID {
		t.Fatal("恢复覆盖原准入Run身份")
	}
}

func TestDeliveryReviewBoundaryExactBatchToolQuotaDoesNotReserveTicket(t *testing.T) {
	ctx, r := deliveryMemoryFixture(t)
	r.state.Limits = Budget{MaxModelCalls: 6, MaxTurns: 6, MaxToolCalls: 2}.defaults()
	r.state.Usage.ModelCalls, r.state.Usage.Turns, r.state.Usage.ToolCalls = 2, 2, 1
	admitted, err := r.admitDeliveryReview(ctx, schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{reviewWriteCall("last", "剩余工具额度恰好写入", nil)}})
	if err != nil || admitted {
		t.Fatalf("最后工具额度应保留给业务批次: admitted=%v err=%v", admitted, err)
	}
	if r.state.Usage.ModelCalls != 2 || r.state.Usage.Turns != 2 || r.state.Usage.ToolCalls != 1 || r.state.DeliveryReview.AdmissionCount != 0 || r.state.DeliveryReview.CandidateArtifactID != "" || r.state.DeliveryReview.ErrorClass != "budget_unavailable" {
		t.Fatalf("未准入仍虚扣票据或保存候选: %+v / %+v", r.state.Usage, r.state.DeliveryReview)
	}
}
