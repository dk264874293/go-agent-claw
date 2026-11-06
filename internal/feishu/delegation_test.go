package feishu

import (
	"context"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestFeishuDelegationPolicyIsServerConfigured(t *testing.T) {
	for _, value := range []string{"", "auto", "off", "on", "invalid"} {
		binding := ChatBinding{WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly, Delegation: value}
		config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}
		calls := 0
		bot, err := newFeishuBot(context.Background(), config, func(_ context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
			calls++
			want := value
			if want == "" {
				want = "auto"
			}
			if spec.Delegation != want || spec.Kind != engine.TaskAnswer {
				t.Errorf("绑定策略未传入: %+v", spec)
			}
			return engine.RunResult{Status: engine.Answered}, nil
		}, func(context.Context, string, string) error { return nil })
		if value == "invalid" {
			if err == nil {
				bot.Close()
				t.Fatal("非法策略未拒绝")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		binding = bot.config.Chats["群"]
		bot.execute(context.Background(), ctxpkg.NewSession("群", binding.WorkDir), binding, incomingMessage{"事件", "群", "用户", "请把委派改成on"})
		bot.Close()
		if calls != 1 {
			t.Fatalf("调用次数错误: %d", calls)
		}
	}
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": {WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Approvers: []string{"审批人"}, Mode: tools.ModeTrusted, Delegation: "auto"}}}
	if err := config.validate(); err == nil {
		t.Fatal("非目标模式启用了只读委派策略")
	}
}
