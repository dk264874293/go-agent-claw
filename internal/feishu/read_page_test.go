package feishu

import (
	"context"
	"reflect"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadPageBindingAndTaskPropagation(t *testing.T) {
	for _, size := range []int{0, 8000, 16000, 8192} {
		binding := ChatBinding{WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly, ReadPageBytes: size}
		config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}
		calls := 0
		run := func(_ context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
			calls++
			if spec.ReadPageBytes != size || spec.Report != nil || spec.Mode != tools.ModeReadOnly {
				t.Fatalf("读取档位改变了权限或未准确传入: %+v", spec)
			}
			return engine.RunResult{Status: engine.Answered}, nil
		}
		bot, err := newFeishuBot(context.Background(), config, run, func(context.Context, string, string) error { return nil })
		if size == 8192 {
			if err == nil {
				bot.Close()
				t.Fatal("飞书接受了无效读取档位")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		binding = bot.config.Chats["群"]
		bot.execute(context.Background(), ctxpkg.NewSession("群", binding.WorkDir), binding, incomingMessage{"事件", "群", "用户", "分析资料"})
		bot.Close()
		if calls != 1 {
			t.Fatalf("调用次数错误: %d", calls)
		}
	}
}

func TestReadScopeBindingPropagationAndIsolation(t *testing.T) {
	paths := []string{"资料,正文.md", "docs"}
	config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": {
		WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly,
		ReadScope: &engine.ReadScopeSpec{Paths: paths, NameGlob: "*.md", RequireComplete: true}, OutputStyle: "detailed",
	}}}
	calls := 0
	bot, err := newFeishuBot(context.Background(), config, func(_ context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
		calls++
		if spec.Mode != tools.ModeReadOnly || spec.Kind != engine.TaskAnswer || spec.Report != nil || spec.OutputStyle != "detailed" || spec.ReadScope == nil || spec.ReadScope.Version != 1 || !spec.ReadScope.RequireComplete || !reflect.DeepEqual(spec.ReadScope.Paths, []string{"docs", "资料,正文.md"}) {
			t.Fatalf("资料契约扩大权限或传播错误: %+v", spec)
		}
		return engine.RunResult{Status: engine.Answered}, nil
	}, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	paths[0] = "外部修改.md"
	binding := bot.config.Chats["群"]
	bot.execute(context.Background(), ctxpkg.NewSession("群", binding.WorkDir), binding, incomingMessage{"事件", "群", "用户", "阅读材料"})
	if calls != 1 {
		t.Fatalf("调用次数错误: %d", calls)
	}
}

func TestReadScopeBindingRejectsInvalidConfigBeforeRun(t *testing.T) {
	for _, invalid := range []ChatBinding{
		{ReadScope: &engine.ReadScopeSpec{Paths: []string{"../secret"}}},
		{ReadScope: &engine.ReadScopeSpec{Paths: []string{"docs"}, NameGlob: "**"}},
		{ReadScope: &engine.ReadScopeSpec{Version: 9, Paths: []string{"docs"}}},
		{OutputStyle: "short"},
	} {
		invalid.WorkDir, invalid.AllowedUsers = t.TempDir(), []string{"用户"}
		calls := 0
		bot, err := newFeishuBot(context.Background(), Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": invalid}}, func(context.Context, *ctxpkg.Session, engine.TaskSpec, engine.Reporter, tools.MiddlewareFunc) (engine.RunResult, error) {
			calls++
			return engine.RunResult{}, nil
		}, func(context.Context, string, string) error { return nil })
		if err == nil {
			bot.Close()
			t.Fatal("飞书接受了非法读取配置")
		}
		if calls != 0 {
			t.Fatal("非法配置启动了任务")
		}
	}
}
