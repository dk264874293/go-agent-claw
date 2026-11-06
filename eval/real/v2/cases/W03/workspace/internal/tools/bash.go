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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type BashTool struct{ workDir string }

func NewBashTool(workDir string) *BashTool { return &BashTool{workDir: workDir} }
func (t *BashTool) Name() string           { return "bash" }

func (t *BashTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "在可信本机模式运行 Bash，命令拥有当前用户权限。返回合并后的标准输出和标准错误。", map[string]any{
		"command": stringParam("要运行的 Bash 命令，例如 go test ./internal/context/"),
	}, "command")
}

type bashArgs struct {
	Command string `json:"command"`
}

func parseBash(args json.RawMessage) (input bashArgs, err error) {
	if err := decodeArgs(args, &input, "command"); err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Command) == "" || strings.ContainsRune(input.Command, 0) {
		return input, reject("参数错误", fmt.Errorf("command 必须是有效的非空命令"))
	}
	return input, nil
}

func (t *BashTool) validate(_ context.Context, args json.RawMessage) error {
	_, err := parseBash(args)
	return err
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.execute(ctx, args)
	return output, err
}

type boundedOutput struct {
	bytes.Buffer
	dropped int
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	const maxCommandOutput = 1024 * 1024
	kept := min(len(data), maxCommandOutput-b.Len())
	_, _ = b.Buffer.Write(data[:kept])
	b.dropped += len(data) - kept
	return len(data), nil
}

func (b *boundedOutput) text() string {
	result := strings.ToValidUTF8(b.String(), "�")
	if b.dropped > 0 {
		result += fmt.Sprintf("\n[命令输出已达 1 MiB 上限，省略 %d 字节]", b.dropped)
	}
	return result
}

func (t *BashTool) execute(ctx context.Context, args json.RawMessage) (string, *int, error) {
	if err := requireTool(ctx, t.Name()); err != nil {
		return "", nil, err
	}
	input, err := parseBash(args)
	if err != nil {
		return "", nil, err
	}
	dir := workDir(ctx, t.workDir)
	if dir == "" {
		return "", nil, reject("路径越界", fmt.Errorf("未指定命令工作区"))
	}

	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, "bash", "-c", input.Command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		if deadline.Err() != nil {
			return "", nil, bashCancellation(deadline.Err(), schema.EffectNone)
		}
		return "", nil, &executionError{status: schema.ToolFailed, effect: schema.EffectNone, kind: "无法启动命令", err: err}
	}

	err = cmd.Wait()
	var exitCode *int
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		exitCode = &code
	}
	result := output.text()
	if err != nil {
		if errors.Is(err, exec.ErrWaitDelay) {
			// 首进程已退出但后代仍持有输出管道，清理同组后代。
			_ = cmd.Cancel()
		}
		if deadline.Err() != nil {
			return result, exitCode, bashCancellation(deadline.Err(), schema.EffectUnknown)
		}
		return result, exitCode, &executionError{status: schema.ToolFailed, effect: schema.EffectUnknown, kind: "命令失败", err: err}
	}
	if result == "" {
		result = "命令执行成功，无终端输出。"
	}
	return result, exitCode, nil
}

func bashCancellation(err error, effect schema.EffectStatus) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &executionError{status: schema.ToolCancelled, effect: effect, kind: "超时", err: fmt.Errorf("命令执行超时: %w", err)}
	}
	return &executionError{status: schema.ToolCancelled, effect: effect, kind: "取消", err: fmt.Errorf("命令已取消: %w", err)}
}
