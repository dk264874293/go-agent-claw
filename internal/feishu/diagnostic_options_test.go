package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestFeishuDiagnosticsBindingIsCurrentPolicyNotTaskData(t *testing.T) {
	t.Setenv("CLAW_OBSERVE", "off")
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "off")
	dir := t.TempDir()
	binding := ChatBinding{WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly, RequestDiagnostics: "on_error", RequestDiagnosticsDir: dir}
	calls := 0
	bot, err := newFeishuBot(context.Background(), Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}, func(ctx context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
		calls++
		if requestdiag.Mode(ctx) != requestdiag.OnError {
			t.Fatal("明确诊断策略没有独立传入运行上下文")
		}
		raw, _ := json.Marshal(spec)
		if strings.Contains(string(raw), "request_diagnostics") || spec.Mode != tools.ModeReadOnly {
			t.Fatalf("诊断策略进入任务语义或扩大权限: %s", raw)
		}
		return engine.RunResult{Status: engine.Answered}, nil
	}, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	actual := bot.config.Chats["群"]
	if actual.RequestDiagnostics != "on_error" || actual.RequestDiagnosticsDir != dir {
		t.Fatalf("绑定诊断丢失: %+v", actual)
	}
	bot.execute(context.Background(), ctxpkg.NewSession("群", actual.WorkDir), actual, incomingMessage{"事件", "群", "用户", "检查资料"})
	if calls != 1 {
		t.Fatalf("调用次数错误: %d", calls)
	}
}

func TestFeishuInvalidDiagnosticsRejectBeforeRun(t *testing.T) {
	calls := 0
	bot, err := newFeishuBot(context.Background(), Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": {WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, RequestDiagnostics: "all"}}}, func(context.Context, *ctxpkg.Session, engine.TaskSpec, engine.Reporter, tools.MiddlewareFunc) (engine.RunResult, error) {
		calls++
		return engine.RunResult{}, nil
	}, func(context.Context, string, string) error { return nil })
	if err == nil {
		bot.Close()
		t.Fatal("非法诊断配置被接受")
	}
	if calls != 0 {
		t.Fatal("非法诊断启动任务")
	}
}
