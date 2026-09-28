package feishu

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

var feishuTestChatSequence atomic.Uint64

type orderedProvider struct {
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (p *orderedProvider) Generate(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	if p.calls == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "回复"}, nil
}

func TestFeishuSubmissionPreservesSessionOrder(t *testing.T) {
	p := &orderedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	b := &FeishuBot{workDir: t.TempDir(), factory: func(*ctxpkg.Session) *engine.AgentEngine {
		return engine.NewAgentEngine(p, tools.NewRegistry(), false, false)
	}}
	chatID := fmt.Sprintf("测试会话-%d", feishuTestChatSequence.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first := b.submitAgentRun(ctx, chatID, "第一条", nil)
	<-p.entered
	second := b.submitAgentRun(ctx, chatID, "第二条", nil)
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(chatID, b.workDir)
	if session.MessageCount() != 1 {
		t.Fatal("飞书排队消息提前写入会话")
	}
	close(p.release)
	if got := <-first; got.Err != nil {
		t.Fatal(got.Err)
	}
	if got := <-second; got.Err != nil {
		t.Fatal(got.Err)
	}
	var contents []string
	for _, msg := range session.GetWorkingMemory(20) {
		contents = append(contents, msg.Content)
	}
	if !reflect.DeepEqual(contents, []string{"第一条", "回复", "第二条", "回复"}) {
		t.Fatalf("飞书消息交叉: %v", contents)
	}
}

func waitForApprovalEntry(t *testing.T, m *ApprovalManager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		n := len(m.pendingTasks)
		m.mu.RUnlock()
		if n == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("审批未登记")
}

func TestApprovalCancellationCleansWaiter(t *testing.T) {
	m := &ApprovalManager{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		allowed, reason := m.WaitForApproval(ctx, "id", "bash", "{}", nil)
		if allowed {
			t.Error("取消审批被放行")
		}
		done <- reason
	}()
	waitForApprovalEntry(t, m)
	cancel()
	select {
	case reason := <-done:
		if !strings.Contains(reason, "中断") {
			t.Fatalf("缺少中断原因: %s", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后审批仍阻塞")
	}
	m.mu.RLock()
	n := len(m.pendingTasks)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatal("取消后审批未清理")
	}
}

func TestDuplicateApprovalNeverBlocks(t *testing.T) {
	m := &ApprovalManager{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { allowed, _ := m.WaitForApproval(ctx, "id", "bash", "{}", nil); done <- allowed }()
	waitForApprovalEntry(t, m)
	resolved := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			m.ResolveApproval("id", true, "通过")
		}
		close(resolved)
	}()
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("重复审批阻塞")
	}
	if !<-done {
		t.Fatal("有效审批未放行")
	}
}
