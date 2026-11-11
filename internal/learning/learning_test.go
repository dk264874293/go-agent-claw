package learning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestLessonsExerciseActualEngineBoundaries(t *testing.T) {
	// 回放必须独立于真实密钥、端点及CLI观察配置。
	t.Setenv("OPENAI_API_KEY", "unused-test-key")
	t.Setenv("DEEPSEEK_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("CLAW_OBSERVE_DIR", t.TempDir())
	cases := []struct {
		name       string
		models     []int
		dispatches []int
		statuses   []engine.TaskStatus
	}{
		{"react", []int{2}, []int{1}, []engine.TaskStatus{engine.Answered}},
		{"budget", []int{1, 3, 2}, []int{1, 3, 1}, []engine.TaskStatus{engine.BudgetExhausted, engine.BudgetExhausted, engine.BudgetExhausted}},
		{"permission", []int{3, 3}, []int{2, 2}, []engine.TaskStatus{engine.Answered, engine.NeedsVerification}},
		{"context", []int{3, 3, 0}, []int{24, 24, 0}, []engine.TaskStatus{engine.Answered, engine.Answered, engine.Blocked}},
		{"recovery", []int{2, 0}, []int{0, 0}, []engine.TaskStatus{engine.NeedsVerification, engine.Blocked}},
		{"plan", []int{5, 2, 1}, []int{2, 1, 0}, []engine.TaskStatus{engine.NeedsVerification, engine.NeedsVerification, engine.Failed}},
		{"extension", []int{2, 2}, []int{1, 1}, []engine.TaskStatus{engine.Answered, engine.Answered}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			trials, err := Run(context.Background(), tc.name, &output)
			if err != nil {
				t.Fatalf("课程失败：%v\n%s", err, output.String())
			}
			if len(trials) != len(tc.models) {
				t.Fatalf("对照数量错误：%d", len(trials))
			}
			for i, trial := range trials {
				if trial.ModelRequests != tc.models[i] || len(trial.Dispatched) != tc.dispatches[i] || trial.Result.Status != tc.statuses[i] {
					t.Fatalf("对照%d偏离预期：%+v", i, trial)
				}
				for _, result := range trial.Dispatched {
					if result.Read != nil && (result.Read.Version == "" || len(result.ReadContent) == 0) {
						t.Fatal("课程用打印文本代替了真实来源回执")
					}
				}
			}
			if tc.name == "context" && (trials[0].Facts["source_bodies_in_final_request"] != 12 || trials[1].Facts["source_bodies_in_final_request"] != 24) {
				t.Fatal("上下文对照没有揭示窗口丢失与原文保留的差别")
			}
		})
	}
}

func TestScriptRejectsBrokenPairingAndUnexpectedObservation(t *testing.T) {
	s := &script{p: &printer{out: io.Discard}, next: func(_ int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if err := observation(messages, "read", schema.ToolSucceeded, "真实原文"); err != nil {
			return nil, err
		}
		return answer("已核对")
	}}
	if _, err := s.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, ToolCallID: "orphan"}}, nil); err == nil || s.calls != 0 {
		t.Fatal("孤立工具观察未被阻断")
	}
	pairedButWrong := []schema.Message{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "read", Name: "read_file"}}},
		{Role: schema.RoleUser, ToolCallID: "read", ToolStatus: schema.ToolSucceeded, Content: "错误原文"},
	}
	if msg, err := s.Generate(context.Background(), pairedButWrong, nil); err == nil || msg != nil {
		t.Fatal("脚本忽略错误观察并交付固定终答")
	}
}

func TestCancellationAndInvalidScenarioNeverStartLesson(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if trials, err := Run(ctx, "react", io.Discard); !errors.Is(err, context.Canceled) || trials != nil {
		t.Fatalf("取消后继续运行：%v", err)
	}
	var output bytes.Buffer
	if _, err := Run(context.Background(), "unknown", &output); err == nil || output.Len() != 0 {
		t.Fatal("未知课程不应创建工作区或显示成功")
	}
}

func TestCustomToolRejectsMalformedArgumentsBeforeItsWork(t *testing.T) {
	tool := &countWords{}
	for _, raw := range []string{`{}`, `null`, `{"text":null}`, `{"text":3}`, `{"text":"a","extra":true}`, `{"text":"a"} {}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("接受无效参数：%s", raw)
		}
	}
	if tool.executions != 0 {
		t.Fatal("参数错误已经执行计算")
	}
	if value, err := tool.Execute(context.Background(), json.RawMessage(`{"text":"模型  候选\n程序"}`)); err != nil || value != "3" {
		t.Fatalf("有效参数结果：%q %v", value, err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOutputFailureDoesNotReportLessonSuccess(t *testing.T) {
	if _, err := Run(context.Background(), "react", brokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("输出失败被忽略：%v", err)
	}
	if err := paired([]schema.Message{{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "pending"}}}}); err == nil || !strings.Contains(err.Error(), "缺少") {
		t.Fatal("不完整批次未被发现")
	}
}
