package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// hostBashTool 仅保留在测试二进制中，用于 M2 宿主进程组回归。
type hostBashTool struct{ workDir string }

func newHostBashTool(dir string) *hostBashTool          { return &hostBashTool{workDir: dir} }
func (*hostBashTool) Name() string                      { return "bash" }
func (*hostBashTool) Definition() schema.ToolDefinition { return schema.ToolDefinition{Name: "bash"} }
func (*hostBashTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ExecuteProcess}
}
func (t *hostBashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.ExecuteDetailed(ctx, args)
	return output, err
}
func (t *hostBashTool) ExecuteDetailed(ctx context.Context, args json.RawMessage) (string, schema.ToolMetadata, error) {
	var input bashArgs
	var meta schema.ToolMetadata
	if err := json.Unmarshal(args, &input); err != nil {
		return "", meta, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, ExecutionOptionsFrom(ctx).Timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)
	cmd.Dir = t.workDir
	if err := configureProcess(cmd); err != nil {
		return "", meta, err
	}
	var buffer boundedOutput
	cmd.Stdout, cmd.Stderr = &buffer, &buffer
	err := cmd.Run()
	_ = killProcessGroup(cmd)
	output, truncated := buffer.snapshot()
	meta.Truncated = truncated
	if timeoutCtx.Err() != nil {
		meta.TimedOut = errors.Is(timeoutCtx.Err(), context.DeadlineExceeded)
		meta.Cancelled = errors.Is(timeoutCtx.Err(), context.Canceled)
		return output, meta, timeoutCtx.Err()
	}
	if err != nil {
		return output, meta, fmt.Errorf("命令执行失败: %w", err)
	}
	return output, meta, nil
}

type testApprover struct{}

func (testApprover) Approve(_ context.Context, r policy.Request) (policy.Grant, error) {
	return policy.GrantFor(r, "测试操作者"), nil
}
func newExecutionTestRegistry() Registry {
	return NewRegistryWithPolicy(policy.Development(false), testApprover{})
}
func executionTestContext(ctx context.Context) context.Context {
	return policy.WithIdentity(ctx, "测试运行", "测试会话")
}
