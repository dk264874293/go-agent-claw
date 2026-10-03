package approval

import (
	"context"
	"errors"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"testing"
	"time"
)

func TestLocalApprovalAndCancellation(t *testing.T) {
	ctx := policy.WithIdentity(context.Background(), "运行", "会话")
	r, _ := policy.NewRequest(ctx, "bash", []byte(`{"command":"测试"}`))
	l := NewLocal()
	l.verify = func(context.Context, policy.Request) (bool, error) { return true, nil }
	g, err := l.Approve(ctx, r)
	if err != nil || policy.Validate(r, g, time.Now()) != nil {
		t.Fatalf("本地授权失败: %+v %v", g, err)
	}
	l.verify = func(context.Context, policy.Request) (bool, error) { return false, nil }
	if _, err := l.Approve(ctx, r); !errors.Is(err, policy.ErrApprovalRejected) {
		t.Fatal("拒绝未生效")
	}
	entered := make(chan struct{})
	l.verify = func(ctx context.Context, _ policy.Request) (bool, error) {
		close(entered)
		<-ctx.Done()
		return false, ctx.Err()
	}
	first, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := l.Approve(first, r); done <- err }()
	<-entered
	queued, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := l.Approve(queued, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队审批未超时: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("等待审批未取消: %v", err)
	}
	l.verify = func(context.Context, policy.Request) (bool, error) { return true, nil }
	if _, err := l.Approve(ctx, r); err != nil {
		t.Fatalf("取消后审批未释放: %v", err)
	}
	r.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := l.Approve(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("过期请求被批准")
	}
}
