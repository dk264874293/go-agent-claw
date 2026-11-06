package feishu

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestAnalysisBindingMustBeReadOnly(t *testing.T) {
	for _, mode := range []tools.Mode{"", tools.ModeReadOnly, tools.ModeWorkspace, tools.ModeTrusted} {
		t.Run(string(mode), func(t *testing.T) {
			binding := ChatBinding{WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Approvers: []string{"审批人"}, Mode: mode, Analysis: true}
			config := Config{EventLedger: filepath.Join(t.TempDir(), "ledger"), Chats: map[string]ChatBinding{"群": binding}}
			err := config.validate()
			wantErr := mode == tools.ModeWorkspace || mode == tools.ModeTrusted
			if (err != nil) != wantErr {
				t.Fatalf("模式 %s 的分析配置判断错误: %v", mode, err)
			}
			if wantErr && !strings.Contains(err.Error(), "分析模式") {
				t.Fatalf("未给出具体分析模式提示: %v", err)
			}
		})
	}
}

func TestFeishuPassesExplicitAnalysisWithoutGoalInference(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通问答", true: "显式分析"}[enabled], func(t *testing.T) {
			binding := ChatBinding{WorkDir: t.TempDir(), AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly, Analysis: enabled}
			config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}
			calls := 0
			run := func(_ context.Context, _ *ctxpkg.Session, spec engine.TaskSpec, _ engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
				calls++
				if spec.Analysis != enabled || spec.Kind != engine.TaskAnswer || spec.Mode != tools.ModeReadOnly {
					t.Fatalf("分析配置没有准确传入: %+v", spec)
				}
				return engine.RunResult{Status: engine.Answered, FinalText: "已答复，未执行语义验收"}, nil
			}
			bot, err := newFeishuBot(context.Background(), config, run, func(context.Context, string, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer bot.Close()
			binding = bot.config.Chats["群"]
			bot.execute(context.Background(), ctxpkg.NewSession("群", binding.WorkDir), binding, incomingMessage{"事件", "群", "用户", "分析文档并列出证据"})
			if calls != 1 {
				t.Fatalf("入口重复调用: %d", calls)
			}
		})
	}
}
