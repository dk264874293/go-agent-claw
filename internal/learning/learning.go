// Package learning 装配无密钥的确定性课程，所有任务都使用正式引擎和临时工作区。
package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type Lesson struct{ Name, Title string }

func Lessons() []Lesson {
	return []Lesson{{"react", "观察一次完整循环"}, {"budget", "预算与停止"},
		{"permission", "候选、准入与副作用"}, {"context", "消息窗口与未处理证据"},
		{"recovery", "中断后的文件核对与未知效果"}, {"plan", "依赖、完成步骤与非法计划"},
		{"extension", "注册工具与执行契约"}}
}

// Trial 保存课程观察，不以课程通过替代任务的真实终态。
type Trial struct {
	Name          string
	Result        engine.RunResult
	ModelRequests int
	Dispatched    []schema.ToolResult
	Facts         map[string]int
}

type lessonFunc func(context.Context, string, *printer) ([]Trial, error)

func Run(ctx context.Context, name string, out io.Writer) ([]Trial, error) {
	lessons := map[string]lessonFunc{"react": react, "budget": budgetLesson, "permission": permission,
		"context": contextLesson, "recovery": recovery, "plan": planLesson, "extension": extension}
	run, ok := lessons[name]
	if !ok {
		return nil, fmt.Errorf("未知课程 %q，请使用 -list 查看课程", name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "claw-learn-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	p := &printer{out: out}
	p.printf("课程：%s。脚本 Provider，无远端模型调用；临时工作区结束后删除。\n", name)
	trials, err := run(ctx, dir, p)
	if err != nil {
		return trials, err
	}
	if err := ctx.Err(); err != nil {
		return trials, err
	}
	for _, trial := range trials {
		encoded, err := json.Marshal(struct {
			Name       string            `json:"case"`
			Status     engine.TaskStatus `json:"status"`
			StopCode   string            `json:"stop_code"`
			Usage      engine.Usage      `json:"usage"`
			Dispatches int               `json:"registry_dispatches"`
			Facts      map[string]int    `json:"observed"`
		}{trial.Name, trial.Result.Status, trial.Result.StopCode, trial.Result.Usage, len(trial.Dispatched), trial.Facts})
		if err != nil {
			return trials, err
		}
		p.printf("[任务结果] %s\n", encoded)
		p.printf("[停止原因] %s\n", trial.Result.Reason)
	}
	p.printf("[课程通过] 预期机制已由实际观察核对；不代表真实模型采纳率、语义质量或业务验收通过。\n")
	return trials, p.err
}

type printer struct {
	mu  sync.Mutex
	out io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.out, format, args...)
	}
}

type script struct {
	p     *printer
	calls int
	next  func(int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)
}

func (s *script) Generate(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := paired(messages); err != nil {
		return nil, err
	}
	s.calls++
	s.p.printf("[模型请求 %d] 消息 %d 条，可见工具 %d 个，消息配对有效\n", s.calls, len(messages), len(defs))
	msg, err := s.next(s.calls, messages, defs)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, fmt.Errorf("课程脚本没有产生响应")
	}
	for _, call := range msg.ToolCalls {
		s.p.printf("[候选] %s id=%s 参数=%s\n", call.Name, call.ID, call.Arguments)
	}
	if len(msg.ToolCalls) == 0 {
		s.p.printf("[候选答复] %s\n", msg.Content)
	}
	return msg, nil
}

// paired 只检查公开消息协议；实际请求的准备与投影仍由正式引擎执行。
func paired(messages []schema.Message) error {
	pending := map[string]bool{}
	for _, message := range messages {
		if message.ToolCallID != "" {
			if message.Role != schema.RoleUser || !pending[message.ToolCallID] {
				return fmt.Errorf("课程收到孤立工具观察 %q", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("课程请求中工具批次缺少观察")
		}
		for _, call := range message.ToolCalls {
			if message.Role != schema.RoleAssistant || call.ID == "" || pending[call.ID] {
				return fmt.Errorf("课程请求中工具候选身份无效")
			}
			pending[call.ID] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("课程请求末尾缺少工具观察")
	}
	return nil
}

type registry struct {
	tools.Registry
	mu      sync.Mutex
	results []schema.ToolResult
	p       *printer
}

func (r *registry) Preflight(ctx context.Context, call schema.ToolCall) *schema.ToolResult {
	if preflight, ok := r.Registry.(tools.CallPreflight); ok {
		return preflight.Preflight(ctx, call)
	}
	return nil
}

func (r *registry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	result := r.Registry.Execute(ctx, call)
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
	r.p.printf("[分发回执] %s id=%s status=%s effect=%s\n", call.Name, call.ID, result.Status, result.Effect)
	return result
}

func limits() engine.Budget {
	return engine.Budget{MaxTurns: 12, MaxModelCalls: 12, MaxToolCalls: 40, Timeout: 20 * time.Second}
}

func execute(ctx context.Context, dir, name string, p *printer, model *script, options engine.EngineOptions,
	spec engine.TaskSpec, session *ctxpkg.Session, additions ...tools.BaseTool) (Trial, error) {
	if options.Budget == (engine.Budget{}) {
		options.Budget = limits()
	}
	if options.Mode == "" {
		options.Mode = tools.ModeReadOnly
	}
	if session == nil {
		session = ctxpkg.NewSession("学习回放", dir)
	}
	reg := &registry{Registry: engine.NewWorkspaceRegistry(dir), p: p}
	for _, tool := range additions {
		reg.Register(tool)
	}
	p.printf("\n[对照] %s\n", name)
	result, err := engine.NewAgentEngine(model, reg, options).Run(ctx, session, spec, nil)
	return Trial{Name: name, Result: result, ModelRequests: model.calls, Dispatched: reg.results, Facts: map[string]int{}}, err
}

func call(id, name string, args any) (*schema.Message, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: id, Name: name, Arguments: raw}}}, nil
}

func answer(text string) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: text}, nil
}

func observation(messages []schema.Message, id string, status schema.ToolStatus, text string) error {
	if len(messages) == 0 {
		return fmt.Errorf("缺少工具观察 %s", id)
	}
	m := messages[len(messages)-1]
	if m.ToolCallID != id || m.ToolStatus != status || !strings.Contains(m.Content, text) {
		return fmt.Errorf("工具观察 %s 未符合预期身份、状态或正文", id)
	}
	return nil
}

func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("课程断言失败：%s", message)
	}
	return nil
}

func fixture(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0600)
}

func subdir(dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	return path, os.Mkdir(path, 0700)
}
