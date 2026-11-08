package feishu

import (
	"context"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadFeaturesBindingPropagationAndIsolation(t *testing.T) {
	features := &engine.ReadFeatureSpec{Version: 1, LocalChecks: true}
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": {WorkDir: t.TempDir(), Mode: tools.ModeReadOnly, AllowedUsers: []string{"用户"}, ReadFeatures: features}}}
	calls := 0
	bot, err := newFeishuBot(context.Background(), config, func(_ context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
		calls++
		if spec.ReadFeatures == nil || !spec.ReadFeatures.LocalChecks || spec.ReadFeatures.SourceViews || spec.ReadFeatures.ClaimFocus || spec.Mode != tools.ModeReadOnly {
			t.Fatalf("飞书能力或权限传播错误: %+v", spec)
		}
		return engine.RunResult{Status: engine.Answered}, nil
	}, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	features.SourceViews = true
	binding := bot.config.Chats["群"]
	bot.execute(context.Background(), ctxpkg.NewSession("群", binding.WorkDir), binding, incomingMessage{"事件", "群", "用户", "分析资料"})
	if calls != 1 {
		t.Fatal("调用次数错误")
	}
}

func TestReadFeaturesBindingRejectsStrictAndUnknownVersions(t *testing.T) {
	for _, binding := range []ChatBinding{{Analysis: true, ReadFeatures: &engine.ReadFeatureSpec{Version: 1, ClaimFocus: true}}, {ReadFeatures: &engine.ReadFeatureSpec{Version: 8, LocalChecks: true}}} {
		binding.WorkDir, binding.Mode, binding.AllowedUsers = t.TempDir(), tools.ModeReadOnly, []string{"用户"}
		_, err := newFeishuBot(context.Background(), Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}, func(context.Context, *ctxpkg.Session, engine.TaskSpec, engine.Reporter, tools.MiddlewareFunc) (engine.RunResult, error) {
			t.Fatal("非法绑定启动任务")
			return engine.RunResult{}, nil
		}, func(context.Context, string, string) error { return nil })
		if err == nil {
			t.Fatal("飞书接受非法读取配置")
		}
	}
}
