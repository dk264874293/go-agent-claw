/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 11:28:21
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 11:28:47
 * @FilePath: /go-agent-claw/internal/tools/bash.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/tools/bash.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/executor"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type BashTool struct {
	workDir  string
	executor executor.Executor
	network  bool
}

func NewBashTool(workDir string) *BashTool {
	backend, _ := executor.NewDocker(executor.Defaults())
	return NewBashToolWithExecutor(workDir, backend, false)
}

func NewBashToolWithExecutor(workDir string, backend executor.Executor, network bool) *BashTool {
	return &BashTool{workDir: workDir, executor: backend, network: network}
}

func (t *BashTool) RequiredCapabilities() []policy.Capability {
	capabilities := []policy.Capability{policy.ReadWorkspace, policy.WriteWorkspace, policy.ExecuteProcess}
	if t.network {
		capabilities = append(capabilities, policy.Network)
	}
	return capabilities
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "经审批后在 Linux 容器的 /workspace 中执行 bash 命令；网络由运行配置决定，后端不可用时拒绝执行。返回有界的标准输出和错误。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的 bash 命令，例如: ls -la 或 go test ./...",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.ExecuteDetailed(ctx, args)
	return output, err
}

// ExecuteDetailed 在有界缓冲中收集输出，由隔离后端在结束时回收容器。
func (t *BashTool) ExecuteDetailed(ctx context.Context, args json.RawMessage) (string, schema.ToolMetadata, error) {
	var input bashArgs
	var meta schema.ToolMetadata
	if err := json.Unmarshal(args, &input); err != nil {
		return "", meta, fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(input.Command) == "" {
		return "", meta, fmt.Errorf("command 不能为空")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, ExecutionOptionsFrom(ctx).Timeout)
	defer cancel()
	if t.executor == nil {
		return "", meta, executor.ErrUnavailable
	}
	result, err := t.executor.Execute(timeoutCtx, executor.Request{WorkDir: t.workDir, Command: input.Command, Network: t.network})
	meta = schema.ToolMetadata{Truncated: result.Truncated, TimedOut: result.TimedOut, Cancelled: result.Cancelled, ResourceExhausted: result.ResourceExhausted}
	output := result.Output
	if err != nil {
		return output, meta, err
	}
	if output == "" {
		output = "命令执行成功，无终端输出。"
	}
	return output, meta, nil
}
