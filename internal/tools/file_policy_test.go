package tools

import (
	"context"
	"encoding/json"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedFileToolsAndEditFailures(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistryWithPolicy(policy.Development(false), testApprover{})
	r.Register(NewWriteFileTool(dir))
	r.Register(NewEditFileTool(dir))
	ctx := executionTestContext(context.Background())
	for _, call := range []schema.ToolCall{{Name: "write_file", Arguments: json.RawMessage(`{"path":"nested/file","content":"第一行\n原内容\n最后一行"}`)}, {Name: "edit_file", Arguments: json.RawMessage(`{"path":"nested/file","old_text":"原内容","new_text":"新内容"}`)}} {
		if result := r.Execute(ctx, call); result.IsError {
			t.Fatal(result.Output)
		}
	}
	file := filepath.Join(dir, "nested", "file")
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "第一行\n新内容\n最后一行" {
		t.Fatalf("编辑结果错误: %q %v", data, err)
	}
	for _, args := range []string{`{"path":"nested/file","old_text":"","new_text":"危险"}`, `{"path":"../outside","old_text":"a","new_text":"b"}`, `{"path":"missing","old_text":"a","new_text":"b"}`, `{"path":"nested/file","old_text":"不存在","new_text":"b"}`} {
		if result := r.Execute(ctx, schema.ToolCall{Name: "edit_file", Arguments: json.RawMessage(args)}); !result.IsError {
			t.Errorf("非法编辑放行: %s", args)
		}
	}
	data, _ = os.ReadFile(file)
	if string(data) != "第一行\n新内容\n最后一行" {
		t.Fatal("编辑失败却修改了文件")
	}
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte(strings.Repeat("a", 1024*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if result := r.Execute(ctx, schema.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"path":"large","old_text":"a","new_text":"b"}`)}); !result.IsError {
		t.Fatal("编辑超大文件未拒绝")
	}
}

type undeclaredTool struct{ called *bool }

func (t undeclaredTool) Name() string { return "undeclared" }
func (t undeclaredTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}
func (t undeclaredTool) ReadOnly() bool { return true }
func (t undeclaredTool) Execute(context.Context, json.RawMessage) (string, error) {
	*t.called = true
	return "副作用", nil
}

func TestUndeclaredToolDenied(t *testing.T) {
	called := false
	r := NewRegistry()
	r.Register(undeclaredTool{called: &called})
	if result := r.Execute(context.Background(), schema.ToolCall{Name: "undeclared"}); !result.Denied || called {
		t.Fatal("未声明的工具仍执行")
	}
	if len(r.GetAvailableTools()) != 0 || r.(*registryImpl).CanRunConcurrently("undeclared") {
		t.Fatal("未声明的工具仍公开或并发")
	}
}
