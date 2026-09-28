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
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type BashTool struct {
	workDir string // 进程默认工作目录，不构成文件访问边界
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在当前工作区执行任意的 bash 命令。支持链式命令(如 &&)。返回标准输出(stdout)和标准错误(stderr)。",
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

// ExecuteDetailed 在有界缓冲中收集输出，并在结束时回收同组进程。
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
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)
	cmd.Dir = t.workDir
	if err := configureProcess(cmd); err != nil {
		return "", meta, err
	}
	var buffer boundedOutput
	cmd.Stdout, cmd.Stderr = &buffer, &buffer
	err := cmd.Run()
	// 正常退出也清理同组后台进程；主动脱离进程组需后续隔离后端治理。
	_ = killProcessGroup(cmd)
	output, truncated := buffer.snapshot()
	meta.Truncated = truncated
	if timeoutCtx.Err() != nil {
		meta.TimedOut = errors.Is(timeoutCtx.Err(), context.DeadlineExceeded)
		meta.Cancelled = errors.Is(timeoutCtx.Err(), context.Canceled)
		return output, meta, fmt.Errorf("命令执行中断: %w", timeoutCtx.Err())
	}
	if err != nil {
		return output, meta, fmt.Errorf("命令执行失败: %w", err)
	}
	if output == "" {
		output = "命令执行成功，无终端输出。"
	}
	return output, meta, nil
}
