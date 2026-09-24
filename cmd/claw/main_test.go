package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type replProvider struct{ calls int }

func (p *replProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	if p.calls == 1 {
		return nil, errors.New("模拟上游错误")
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "第二轮完成"}, nil
}

func TestReportSingleRunExitCode(t *testing.T) {
	var out bytes.Buffer
	result := runpkg.Result{RunID: "run-123", Reason: runpkg.ReasonMaxTurns, Status: runpkg.StatusBudgetExhausted}
	if code := reportSingleRun(&out, result, runpkg.ErrMaxTurns); code != 1 {
		t.Fatalf("失败任务应返回非零退出码，实际: %d", code)
	}
	if !strings.Contains(out.String(), "run-123") || !strings.Contains(out.String(), "max_turns") {
		t.Fatalf("失败输出缺少运行 ID 或终止原因: %q", out.String())
	}
	out.Reset()
	result.Reason = runpkg.ReasonCompleted
	result.Status = runpkg.StatusSucceeded
	if code := reportSingleRun(&out, result, nil); code != 0 {
		t.Fatalf("正常任务应返回零退出码，实际: %d", code)
	}
}

func TestREPLContinuesAfterRunFailure(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := writer.WriteString("第一条\n第二条\nexit\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	previous := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previous }()

	p := &replProvider{}
	session := ctxpkg.NewSession("repl-test", t.TempDir())
	eng := engine.NewAgentEngine(p, tools.NewRegistry(), false, false)
	runREPL(context.Background(), eng, session, nil, "mock-model")
	if p.calls != 2 || session.MessageCount() != 3 {
		t.Fatalf("失败后 REPL 没有继续处理下一条输入: calls=%d messages=%d", p.calls, session.MessageCount())
	}
}
