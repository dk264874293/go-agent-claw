package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type terminalModel func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f terminalModel) Generate(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, messages, defs)
}

type terminalProgress struct{ messages int }

func (*terminalProgress) OnThinking(context.Context)                         {}
func (*terminalProgress) OnToolCall(context.Context, string, string)         {}
func (*terminalProgress) OnToolResult(context.Context, string, string, bool) {}
func (p *terminalProgress) OnMessage(context.Context, string)                { p.messages++ }

func TestSubmitReportsOnlyFinalizedResult(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "正常答复"
		if exhausted {
			name = "写入后预算耗尽"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			model := terminalModel(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if exhausted {
					return &schema.Message{Role: schema.RoleAssistant, Content: "提前宣称全部完成", ToolCalls: []schema.ToolCall{{
						ID: "写入", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.txt","content":"已落盘"}`),
					}}}, nil
				}
				return &schema.Message{Role: schema.RoleAssistant, Content: "最终答复标记"}, nil
			})
			options := engine.EngineOptions{Mode: tools.ModeWorkspace, Budget: engine.Budget{MaxModelCalls: 1}}
			eng := engine.NewAgentEngine(model, engine.NewWorkspaceRegistry(dir), options)
			progress := &terminalProgress{}
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			os.Stdout = write
			defer func() { os.Stdout = original }()
			result := submit(context.Background(), eng, ctxpkg.NewSession("终报测试", dir), progress, engine.TaskSpec{Goal: "交付结果"})
			os.Stdout = original
			_ = write.Close()
			output, err := io.ReadAll(read)
			_ = read.Close()
			if err != nil {
				t.Fatal(err)
			}
			text := string(output)
			if progress.messages != 0 || calls != 1 || strings.Count(text, "任务 "+result.TaskID) != 1 {
				t.Fatalf("终报重复或多调用了模型：回调 %d，调用 %d，输出 %s", progress.messages, calls, text)
			}
			if exhausted {
				if result.Status != engine.BudgetExhausted || result.ExitCode() != 3 || !strings.Contains(text, "result.txt") || strings.Contains(text, "提前宣称全部完成") {
					t.Fatalf("预算终报没有保留真实效果和终态：%+v\n%s", result, text)
				}
			} else if strings.Count(text, "最终答复标记") != 1 {
				t.Fatalf("最终答复未恰好展示一次：%s", text)
			}
			raw, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", result.TaskID, "snapshot.json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				State struct {
					LastResult engine.RunResult `json:"last_result"`
				} `json:"state"`
			}
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.State.LastResult.Status != result.Status || snapshot.State.LastResult.Delivery == nil {
				t.Fatalf("终报发布时持久结果不完整：%s", raw)
			}
		})
	}
}

// 默认测试不向用户的全局观测目录写入；接线测试单独启用临时目录。
func TestMain(m *testing.M) {
	os.Setenv("CLAW_OBSERVE", "off")
	os.Exit(m.Run())
}
