package context

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestSessionFIFOAndCancelledWaiter(t *testing.T) {
	s := NewSession("queue", t.TempDir())
	first, cancelled, last := s.ReserveRun(), s.ReserveRun(), s.ReserveRun()
	defer first.Release()
	defer cancelled.Release()
	defer last.Release()
	if err := first.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelled.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("等待取消结果错误: %v", err)
	}
	if len(s.runQueue) != 2 {
		t.Fatal("取消票据未清理")
	}
	acquired := make(chan error, 1)
	go func() { acquired <- last.Wait(context.Background()) }()
	select {
	case <-acquired:
		t.Fatal("后续票据提前获得执行权")
	default:
	}
	first.Release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("队列未推进")
	}
	last.Release()
	last.Release()
	if len(s.runQueue) != 0 {
		t.Fatal("票据未全部释放")
	}
}

func TestSessionCopiesNestedMessageData(t *testing.T) {
	s := NewSession("copies", t.TempDir())
	message := schema.Message{Role: schema.RoleAssistant,
		ToolCalls: []schema.ToolCall{{ID: "call", Arguments: []byte(`{"x":1}`)}},
		Usage:     &schema.Usage{PromptTokens: 5}, ToolMetadata: &schema.ToolMetadata{Truncated: true}}
	s.Append(message)
	message.ToolCalls[0].Arguments[0] = '!'
	message.Usage.PromptTokens = 9
	for _, limit := range []int{0, 1, 20} {
		got := s.GetWorkingMemory(limit)
		if got[0].ToolCalls[0].Arguments[0] != '{' || got[0].Usage.PromptTokens != 5 || !got[0].ToolMetadata.Truncated {
			t.Fatal("消息与调用方共享可变对象")
		}
		got[0].ToolCalls[0].Arguments[0] = '!'
		got[0].Usage.PromptTokens = 10
		got[0].ToolMetadata.Truncated = false
	}
}

func TestUsageSnapshotConcurrent(t *testing.T) {
	s := NewSession("usage", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.RecordUsage(1, 2, 0.5)
				got := s.UsageSnapshot()
				if got.TotalCompletionTokens != 2*got.TotalPromptTokens || got.TotalCostCNY != float64(got.TotalPromptTokens)/2 {
					t.Error("账单快照不一致")
					return
				}
			}
		}()
	}
	wg.Wait()
	if s.UsageSnapshot().TotalPromptTokens != 4000 {
		t.Fatal("并发计费丢失")
	}
}
