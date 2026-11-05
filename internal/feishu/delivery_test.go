package feishu

import (
	"context"
	"os"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestFinalReportUsesServiceContextAndSendsOnce(t *testing.T) {
	for _, status := range []engine.TaskStatus{engine.Answered, engine.Cancelled, engine.BudgetExhausted, engine.Failed} {
		t.Run(string(status), func(t *testing.T) {
			dir := t.TempDir()
			binding := ChatBinding{WorkDir: dir, AllowedUsers: []string{"用户"}, Mode: tools.ModeReadOnly}
			config := Config{EventLedger: t.TempDir(), Chats: map[string]ChatBinding{"群": binding}}
			taskCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := false
			var sent []string
			run := func(ctx context.Context, _ *ctxpkg.Session, _ engine.TaskSpec, reporter engine.Reporter, _ tools.MiddlewareFunc) (engine.RunResult, error) {
				reporter.OnMessage(ctx, "模型临时过程说明")
				result := engine.RunResult{TaskID: "统一终报", Status: status, Reason: "终态已确定"}
				if status == engine.Answered {
					result.FinalText = "实际最终答复"
				}
				if status == engine.Failed {
					result.StopCode = "storage_error"
				}
				cancel()
				finished = true
				return result, nil
			}
			bot, err := newFeishuBot(context.Background(), config, run, func(ctx context.Context, _, text string) error {
				if !finished || ctx.Err() != nil {
					t.Error("终报提前发布或沿用了已取消的任务上下文")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("终报发送没有截止时间")
				}
				sent = append(sent, text)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer bot.Close()
			binding = bot.config.Chats["群"]
			bot.execute(taskCtx, ctxpkg.NewSession("群", dir), binding, incomingMessage{"事件", "群", "用户", "执行"})
			if len(sent) != 1 || strings.Contains(sent[0], "模型临时过程说明") || !strings.Contains(sent[0], string(status)) {
				t.Fatalf("终报不符合一次发布要求：%v", sent)
			}
			if status == engine.Answered && strings.Count(sent[0], "实际最终答复") != 1 {
				t.Fatalf("最终答复丢失：%v", sent)
			}
		})
	}
}

// 默认测试不向用户的全局观测目录写入；接线测试单独启用临时目录。
func TestMain(m *testing.M) {
	os.Setenv("CLAW_OBSERVE", "off")
	os.Exit(m.Run())
}
