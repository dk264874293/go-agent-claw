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
	"os/exec"
	"time"

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
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	// 【驾驭底线 1】：Time Budgeting (时间预算与超时控制)
	// 给予 bash 命令一个最大执行时间，防止大模型卡死进程 (比如运行了 top 或持续监听的 Web 服务)
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 在 macOS/Linux 下，我们通过将指令包裹在 `bash -c` 中执行，以支持环境变量、管道和逻辑与(&&)等复杂 Shell 语法。
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)

	// 【驾驭底线 2】：绑定执行的工作区目录
	// 确保命令默认在用户指定的 WorkDir 下执行，而不是引擎启动时的绝对路径。
	cmd.Dir = t.workDir

	// 执行并捕获 CombinedOutput (合并 stdout 和 stderr)
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	// 保留已取得的终端输出，同时把超时和取消标记为工具失败。
	if timeoutCtx.Err() != nil {
		return outputStr, fmt.Errorf("命令执行中断: %w", timeoutCtx.Err())
	}

	// Registry 会把非零退出和已取得的 stdout/stderr 一并交给模型。
	if err != nil {
		return outputStr, fmt.Errorf("命令执行失败: %w", err)
	}

	// 如果没有终端输出（比如仅仅执行了 mkdir），给模型一个明确的执行成功的反馈
	if outputStr == "" {
		return "命令执行成功，无终端输出。", nil
	}

	// 【驾驭底线 4】：长度截断保护 (防 OOM)
	const maxLen = 8000
	if len(outputStr) > maxLen {
		return fmt.Sprintf("%s\n\n...[终端输出过长，已截断至前 %d 字节]...", outputStr[:maxLen], maxLen), nil
	}

	return outputStr, nil
}
