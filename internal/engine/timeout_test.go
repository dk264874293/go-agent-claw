package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestRequestTimeoutDoesNotExhaustTaskBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options EngineOptions
		cause   error
	}{
		{name: "TCP连接超时", cause: context.DeadlineExceeded},
		{name: "网络读写超时", cause: os.ErrDeadlineExceeded},
		{name: "规划请求超时", options: EngineOptions{Plan: true}, cause: context.DeadlineExceeded},
		{name: "额外思考超时", options: EngineOptions{EnableThinking: true}, cause: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			requestErr := &url.Error{Op: "Post", URL: "https://example.invalid/v1/responses", Err: &net.OpError{Op: "dial", Net: "tcp", Err: tc.cause}}
			calls := 0
			p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if ctx.Err() != nil {
					t.Fatal("请求错误发生前任务上下文已经结束")
				}
				return nil, fmt.Errorf("模型接口请求失败: %w", requestErr)
			})
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), tc.options)
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("请求超时", dir), TaskSpec{Goal: "分析文件"}, nil)
			if !errors.Is(err, requestErr) || result.Status != Failed || result.StopCode != "request_timeout" || result.ExitCode() != 1 || result.FinalText != "" {
				t.Fatalf("请求超时被错误分类: %+v，错误 %v", result, err)
			}
			if payload := ObservationResult(result, err); payload.Status != string(Failed) || payload.StopCode != "request_timeout" || payload.ErrorClass != "timeout" {
				t.Fatalf("观测结果误报超时分类: %+v", payload)
			}
			if calls != 1 || result.Usage.ModelCalls != 1 || result.Usage.ToolCalls != 0 {
				t.Fatalf("失败请求改变调用计数: %+v，实际调用 %d", result.Usage, calls)
			}
			reader, err := taskstore.NewReadOnlyReader(dir, result.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			events, _, err := reader.Poll()
			if err != nil {
				t.Fatal(err)
			}
			var inspection Inspection
			for _, event := range events {
				if err := inspection.Apply(event); err != nil {
					t.Fatal(err)
				}
			}
			if inspection.Result == nil || inspection.Result.Status != Failed || inspection.Result.StopCode != "request_timeout" || inspection.Result.Reason != result.Reason {
				t.Fatalf("持久终态与返回结果不一致: %+v", inspection.Result)
			}
		})
	}
}

func TestRunDistinguishesContextDeadlineAndCancellation(t *testing.T) {
	for _, kind := range []string{"任务累计时限", "父上下文时限", "父上下文取消"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := EngineOptions{}
			calls := 0
			if kind == "任务累计时限" {
				options.Budget.Timeout = time.Second
			} else if kind == "父上下文时限" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
			}
			p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if kind == "父上下文取消" {
					cancel()
				}
				<-ctx.Done()
				return nil, fmt.Errorf("请求被上下文终止: %w", ctx.Err())
			})
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), options)
			session := ctxpkg.NewSession("上下文终止", dir)
			result, err := eng.Run(ctx, session, TaskSpec{Goal: "等待请求"}, nil)
			wantStatus, wantCode, wantExit := BudgetExhausted, "deadline", 3
			if kind == "父上下文取消" {
				wantStatus, wantCode, wantExit = Cancelled, "cancelled", 130
			}
			if err != nil || result.Status != wantStatus || result.StopCode != wantCode || result.ExitCode() != wantExit || result.Usage.ModelCalls != 1 {
				t.Fatalf("上下文终止分类错误: %+v，错误 %v", result, err)
			}
			if kind == "任务累计时限" {
				resumed, err := eng.Run(context.Background(), session, TaskSpec{ID: result.TaskID, Resume: true}, nil)
				if err != nil || resumed.Status != BudgetExhausted || resumed.StopCode != "deadline" || calls != 1 || resumed.Usage.ModelCalls != 1 {
					t.Fatalf("恢复已耗尽时间的任务再次调用模型: %+v，调用 %d，错误 %v", resumed, calls, err)
				}
			}
		})
	}
}

func TestAlreadyEndedParentContextDoesNotCallProvider(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}
		cancel()
		dir := t.TempDir()
		p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			t.Fatal("已终止上下文不应调用模型")
			return nil, nil
		})
		eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{})
		result, err := eng.Run(ctx, ctxpkg.NewSession("入场终止", dir), TaskSpec{Goal: "等待请求"}, nil)
		wantStatus, wantCode := Cancelled, "cancelled"
		if deadline {
			wantStatus, wantCode = BudgetExhausted, "deadline"
		}
		if err != nil || result.Status != wantStatus || result.StopCode != wantCode || result.Usage.ModelCalls != 0 {
			t.Fatalf("入场上下文分类错误: %+v，错误 %v", result, err)
		}
	}
}

func TestSubagentRequestTimeoutIsFailed(t *testing.T) {
	dir := t.TempDir()
	r := &runState{state: taskState{Limits: (Budget{}).defaults()}, started: time.Now(), subSlots: make(chan struct{}, 2)}
	ctx := context.WithValue(context.Background(), runContextKey{}, r)
	ctx = tools.WithScope(ctx, tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir})
	p := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return nil, fmt.Errorf("子请求连接超时: %w", context.DeadlineExceeded)
	})
	eng := NewAgentEngine(p, NewExplorerRegistry(dir), EngineOptions{})
	report, err := eng.RunSub(ctx, tools.SubtaskSpec{Goal: "调查文件"}, NewExplorerRegistry(dir), nil)
	if err == nil || report.Status != string(Failed) || r.state.Usage.ModelCalls != 1 {
		t.Fatalf("子任务请求超时被错误分类: %+v，错误 %v", report, err)
	}
}

func TestStorageErrorKeepsPriorityOverTimeout(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	failure := errors.Join(errStorage, context.DeadlineExceeded)
	result, err := (RunResult{}).stop(ctx, failure)
	if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" {
		t.Fatalf("存储失败被超时覆盖: %+v，错误 %v", result, err)
	}
	if payload := ObservationResult(result, err); payload.ErrorClass != "storage_error" {
		t.Fatalf("观测结果误报存储错误: %+v", payload)
	}
}

func TestSubagentSharedDeadlineIsBudgetExhausted(t *testing.T) {
	for _, queued := range []bool{false, true} {
		dir := t.TempDir()
		r := &runState{state: taskState{Limits: (Budget{}).defaults()}, started: time.Now(), subSlots: make(chan struct{}, 1)}
		if queued {
			r.subSlots <- struct{}{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		ctx = context.WithValue(ctx, runContextKey{}, r)
		ctx = tools.WithScope(ctx, tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir})
		calls := 0
		p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			calls++
			<-ctx.Done()
			return nil, ctx.Err()
		})
		eng := NewAgentEngine(p, NewExplorerRegistry(dir), EngineOptions{})
		report, err := eng.RunSub(ctx, tools.SubtaskSpec{Goal: "调查文件"}, NewExplorerRegistry(dir), nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || report.Status != string(BudgetExhausted) || (queued && calls != 0) {
			t.Fatalf("子任务共享时限分类错误: queued=%v report=%+v calls=%d err=%v", queued, report, calls, err)
		}
	}
}
