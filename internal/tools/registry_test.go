package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type resultTool struct {
	name   string
	output string
	err    error
}

func (t resultTool) Name() string { return t.name }
func (t resultTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.name}
}
func (t resultTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.output, t.err
}

type failingRunner struct{}

func (failingRunner) RunSub(context.Context, string, Registry, any) (string, error) {
	return "", errors.New("子任务失败")
}

func TestRegistryPreservesOutputOnFailure(t *testing.T) {
	r := NewRegistry()
	r.Register(resultTool{name: "broken", output: "已写入的诊断输出", err: errors.New("执行失败")})
	got := r.Execute(context.Background(), schema.ToolCall{ID: "call-1", Name: "broken"})
	if !got.IsError || got.ToolCallID != "call-1" {
		t.Fatalf("错误状态或调用关联不正确: %+v", got)
	}
	if !strings.Contains(got.Output, "已写入的诊断输出") || !strings.Contains(got.Output, "执行失败") {
		t.Fatalf("失败结果丢失输出或错误原因: %q", got.Output)
	}
}

func TestRegistryRejectsCancelledContext(t *testing.T) {
	r := NewRegistry()
	r.Register(resultTool{name: "noop"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := r.Execute(ctx, schema.ToolCall{ID: "call-2", Name: "noop"})
	if !got.IsError || got.ToolCallID != "call-2" {
		t.Fatalf("取消结果不正确: %+v", got)
	}
}

func TestBashFailureKeepsStderr(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf '诊断信息' >&2; exit 7"}`))
	if err == nil || !strings.Contains(output, "诊断信息") {
		t.Fatalf("Shell 失败应同时返回 stderr 和错误: output=%q err=%v", output, err)
	}
	if success, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf ok"}`)); err != nil || success != "ok" {
		t.Fatalf("Shell 成功结果不正确: output=%q err=%v", success, err)
	}
}

func TestSubagentFailureReturnsError(t *testing.T) {
	tool := NewSubagentTool(failingRunner{}, NewRegistry(), nil)
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"task_prompt":"检查"}`))
	if err == nil || output != "" || !strings.Contains(err.Error(), "子任务失败") {
		t.Fatalf("子任务失败应返回 Go 错误: output=%q err=%v", output, err)
	}
}
