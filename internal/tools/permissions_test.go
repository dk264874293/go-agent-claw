package tools

import (
	"context"
	"encoding/json"
	"github.com/dk264874293/go-agent-claw/internal/executor"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type approverFunc func(context.Context, policy.Request) (policy.Grant, error)

func (f approverFunc) Approve(ctx context.Context, r policy.Request) (policy.Grant, error) {
	return f(ctx, r)
}

type executorFunc func(context.Context, executor.Request) (executor.Result, error)

func (f executorFunc) Execute(ctx context.Context, r executor.Request) (executor.Result, error) {
	return f(ctx, r)
}

func TestPermissionChecksPrecedeMiddleware(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistryWithPolicy(policy.ReadOnly(), testApprover{})
	r.Register(NewWriteFileTool(dir))
	called := false
	r.Use(func(context.Context, schema.ToolCall) (bool, string) { called = true; return true, "" })
	result := r.Execute(executionTestContext(context.Background()), schema.ToolCall{ID: "1", Name: "write_file", Arguments: json.RawMessage(`{"path":"new","content":"写入"}`)})
	if !result.IsError || !result.Denied || called {
		t.Fatalf("权限拒绝不可靠: %+v middleware=%v", result, called)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatal("拒绝后产生文件")
	}
	if len(r.GetAvailableTools()) != 0 {
		t.Fatal("不允许的工具仍公开")
	}
}

func TestApprovalRequiredAndBoundToArguments(t *testing.T) {
	dir := t.TempDir()
	call := schema.ToolCall{ID: "1", Name: "write_file", Arguments: json.RawMessage(`{"path":"new","content":"原值"}`)}
	r := NewRegistry()
	r.Register(NewWriteFileTool(dir))
	result := r.Execute(executionTestContext(context.Background()), call)
	if !result.ApprovalUnavailable || !result.IsError {
		t.Fatal("无审批仍放行")
	}
	r = NewRegistryWithPolicy(policy.Development(false), approverFunc(func(_ context.Context, request policy.Request) (policy.Grant, error) {
		g := policy.GrantFor(request, "测试主体")
		g.RunID = "其他运行"
		return g, nil
	}))
	r.Register(NewWriteFileTool(dir))
	if result = r.Execute(executionTestContext(context.Background()), call); !result.ApprovalRejected {
		t.Fatal("跨运行审批放行")
	}
	r = NewRegistryWithPolicy(policy.Development(false), testApprover{})
	r.Register(NewWriteFileTool(dir))
	r.Use(func(_ context.Context, changed schema.ToolCall) (bool, string) {
		for i, b := range changed.Arguments {
			if b == 'n' {
				changed.Arguments[i] = 'x'
				break
			}
		}
		return true, ""
	})
	if result = r.Execute(executionTestContext(context.Background()), call); !result.ApprovalRejected {
		t.Fatalf("修改已批准参数未拒绝: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatal("拒绝后仍执行")
	}
}

func TestApprovalWaitCancellationAndChildRestriction(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistryWithPolicy(policy.Development(false), approverFunc(func(ctx context.Context, _ policy.Request) (policy.Grant, error) {
		<-ctx.Done()
		return policy.Grant{}, ctx.Err()
	}))
	r.Register(NewWriteFileTool(dir))
	ctx, cancel := context.WithTimeout(executionTestContext(context.Background()), 20*time.Millisecond)
	defer cancel()
	call := schema.ToolCall{Name: "write_file", Arguments: json.RawMessage(`{"path":"new","content":"写入"}`)}
	if result := r.Execute(ctx, call); !result.IsError || !result.TimedOut {
		t.Fatalf("审批等待未取消: %+v", result)
	}
	ctx = policy.Restrict(executionTestContext(context.Background()), policy.ReadOnly())
	if result := r.Execute(ctx, call); !result.Denied {
		t.Fatal("子任务权限被扩大")
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatal("取消后写入")
	}
}

func TestBashNetworkAndBackendErrors(t *testing.T) {
	dir := t.TempDir()
	called := false
	backend := executorFunc(func(_ context.Context, r executor.Request) (executor.Result, error) {
		called = true
		return executor.Result{Output: "后端错误", ResourceExhausted: true}, executor.ErrUnavailable
	})
	r := NewRegistryWithPolicy(policy.Development(false), testApprover{})
	r.Register(NewBashToolWithExecutor(dir, backend, true))
	call := schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo 不应执行"}`)}
	if result := r.Execute(executionTestContext(context.Background()), call); !result.Denied || called {
		t.Fatal("网络权限拒绝失效")
	}
	r = NewRegistryWithPolicy(policy.Development(false), testApprover{})
	r.Register(NewBashToolWithExecutor(dir, backend, false))
	result := r.Execute(executionTestContext(context.Background()), call)
	if !called || !result.IsError || !result.ResourceExhausted || !strings.Contains(result.Output, "后端错误") {
		t.Fatalf("后端错误语义丢失: %+v", result)
	}
}
