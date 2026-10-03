package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type scheduledTool struct {
	name     string
	readOnly bool
	fn       func(context.Context, json.RawMessage) (string, error)
}

func (t scheduledTool) Name() string { return t.name }
func (t scheduledTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace}
}
func (t scheduledTool) Definition() schema.ToolDefinition { return schema.ToolDefinition{Name: t.name} }
func (t scheduledTool) ReadOnly() bool                    { return t.readOnly }
func (t scheduledTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.fn(ctx, args)
}

func TestBatchConcurrencyAndWriteBarrier(t *testing.T) {
	var active, peak, readDone atomic.Int32
	var wrote atomic.Bool
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	r := NewRegistry()
	r.Register(scheduledTool{name: "read", readOnly: true, fn: func(_ context.Context, args json.RawMessage) (string, error) {
		if string(args) == `"after"` {
			if !wrote.Load() {
				t.Error("写入之后的读取提前执行")
			}
			return "after", nil
		}
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		readDone.Add(1)
		return string(args), nil
	}})
	r.Register(scheduledTool{name: "write", fn: func(context.Context, json.RawMessage) (string, error) {
		if active.Load() != 0 || readDone.Load() != 3 {
			t.Error("写入越过了读取屏障")
		}
		wrote.Store(true)
		return "written", nil
	}})
	calls := []schema.ToolCall{
		{ID: "1", Name: "read", Arguments: json.RawMessage(`1`)},
		{ID: "2", Name: "read", Arguments: json.RawMessage(`2`)},
		{ID: "3", Name: "read", Arguments: json.RawMessage(`3`)},
		{ID: "4", Name: "write"},
		{ID: "5", Name: "read", Arguments: json.RawMessage(`"after"`)},
	}
	ctx := WithExecutionOptions(context.Background(), ExecutionOptions{Timeout: time.Second, MaxConcurrent: 2})
	done := make(chan []schema.ToolResult, 1)
	go func() { done <- ExecuteBatch(ctx, r, calls) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("读取未并行启动")
		}
	}
	select {
	case <-started:
		t.Error("并发数超过上限")
	default:
	}
	close(release)
	results := <-done
	if peak.Load() != 2 || !wrote.Load() {
		t.Fatalf("调度结果错误: peak=%d wrote=%v", peak.Load(), wrote.Load())
	}
	for i, result := range results {
		if result.ToolCallID != calls[i].ID {
			t.Fatal("结果顺序被打乱")
		}
	}
}

func TestBatchTimeoutAndUnstartedCancellation(t *testing.T) {
	r := NewRegistry()
	var started atomic.Int32
	r.Register(scheduledTool{name: "wait", fn: func(ctx context.Context, _ json.RawMessage) (string, error) {
		started.Add(1)
		<-ctx.Done()
		return "保留诊断", ctx.Err()
	}})
	ctx := WithExecutionOptions(context.Background(), ExecutionOptions{Timeout: 20 * time.Millisecond, MaxConcurrent: 2})
	results := ExecuteBatch(ctx, r, []schema.ToolCall{{ID: "1", Name: "wait"}})
	if !results[0].TimedOut || !results[0].IsError {
		t.Fatalf("工具超时缺少结构化标记: %+v", results[0])
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	results = ExecuteBatch(ctx, r, []schema.ToolCall{{ID: "2", Name: "wait"}, {ID: "3", Name: "wait"}})
	if started.Load() != 1 {
		t.Fatal("取消后仍启动工具")
	}
	for _, result := range results {
		if !result.Cancelled || !result.IsError {
			t.Fatal("未启动工具未生成取消结果")
		}
	}
}
