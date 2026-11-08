package main

import (
	"context"
	"encoding/json"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestCLIRequestDiagnosticsUseCurrentEntryPolicyWithoutTaskPersistence(t *testing.T) {
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "on_error")
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS_DIR", t.TempDir())
	t.Setenv("CLAW_OBSERVE", "off")
	cfg, err := cliRequestDiagnostics(context.Background())
	if err != nil || cfg.Mode != requestdiag.OnError {
		t.Fatalf("明确环境诊断应独立于观测关闭: %+v %v", cfg, err)
	}
	explicit, err := requestdiag.ResolveConfig("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := withCLIRequestDiagnostics(context.Background(), explicit)
	got, err := cliRequestDiagnostics(ctx)
	if err != nil || got.Mode != requestdiag.Off || got.Dir != explicit.Dir {
		t.Fatalf("启动入口配置未覆盖环境: %+v %v", got, err)
	}
	// 恢复只改变TaskSpec身份，不把旧任务的记录策略当成本轮授权。
	raw, _ := json.Marshal(engine.TaskSpec{ID: "原任务", Resume: true})
	var spec map[string]any
	json.Unmarshal(raw, &spec)
	if _, exists := spec["request_diagnostics"]; exists {
		t.Fatal("入口诊断意外持久化到TaskSpec")
	}
}

func TestCLISubmissionPassesExplicitDiagnosticsIndependently(t *testing.T) {
	t.Setenv("CLAW_OBSERVE", "off")
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "off")
	dir := t.TempDir()
	config, err := requestdiag.ResolveConfig("on_error", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := terminalModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if requestdiag.Mode(ctx) != requestdiag.OnError {
			t.Fatal("启动配置没有传入模型上下文")
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "已回答"}, nil
	})
	eng := engine.NewAgentEngine(model, engine.NewWorkspaceRegistry(dir), engine.EngineOptions{Mode: tools.ModeReadOnly})
	result := submit(withCLIRequestDiagnostics(context.Background(), config), eng, ctxpkg.NewSession("诊断配置", dir), nil, engine.TaskSpec{Goal: "回答", Kind: engine.TaskAnswer})
	if result.Status != engine.Answered || calls != 1 {
		t.Fatalf("诊断改变引擎结果: %+v calls=%d", result, calls)
	}
}

func TestCLIInvalidRequestDiagnosticsRejectBeforeSubmission(t *testing.T) {
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "capture-everything")
	if _, err := cliRequestDiagnostics(context.Background()); err == nil {
		t.Fatal("无效诊断模式没有在运行前拒绝")
	}
}
