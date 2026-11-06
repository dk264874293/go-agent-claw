package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type scriptedProvider func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f scriptedProvider) Generate(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, messages, defs)
}

func reply(text string) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: text}, nil
}

func modelCalls(calls ...schema.ToolCall) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, ToolCalls: calls}, nil
}

func readCall(id, path string) schema.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path})
	return schema.ToolCall{ID: id, Name: "read_file", Arguments: args}
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRunForwardsRecoveryHintToModel(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	reasoningItem := json.RawMessage(`{"type":"reasoning","id":"rs_test","summary":[],"encrypted_content":"fixture"}`)
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			msg, err := modelCalls(readCall("missing", "missing.txt"))
			msg.ResponseItems = []json.RawMessage{reasoningItem}
			return msg, err
		}
		previous := messages[len(messages)-2]
		if len(previous.ResponseItems) != 1 || string(previous.ResponseItems[0]) != string(reasoningItem) {
			t.Fatalf("工具观察回传时丢失了 Provider 输出项: %#v", previous)
		}
		last := messages[len(messages)-1]
		if last.Role != schema.RoleUser || last.ToolCallID != "missing" || !strings.Contains(last.Content, "[系统救援指南]") {
			t.Fatalf("模型未收到带救援提示的配对观察: %#v", last)
		}
		return reply("指定文件不存在。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("救援", dir), TaskSpec{Goal: "读取指定文件"}, nil)
	if err != nil || result.Status != Answered || calls != 2 {
		t.Fatalf("结果 = %+v，调用次数 = %d，错误 = %v", result, calls, err)
	}
}

func TestRunStopsAtModelAndToolBudgets(t *testing.T) {
	for _, tc := range []struct {
		name               string
		models, tools      int
		perTurn            int
		wantModels, wantIO int
	}{
		{name: "模型额度", models: 3, tools: 10, perTurn: 1, wantModels: 3, wantIO: 3},
		{name: "并行工具额度", models: 10, tools: 2, perTurn: 4, wantModels: 2, wantIO: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "内容")
			calls := 0
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				var batch []schema.ToolCall
				for i := 0; i < tc.perTurn; i++ {
					batch = append(batch, readCall(strconv.Itoa(calls*10+i), "source.txt"))
				}
				return modelCalls(batch...)
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{
				Budget: Budget{MaxModelCalls: tc.models, MaxToolCalls: tc.tools},
			})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("预算", dir), TaskSpec{Goal: "持续读取文件"}, nil)
			if err != nil || result.Status != BudgetExhausted ||
				result.Usage.ModelCalls != tc.wantModels || result.Usage.ToolCalls != tc.wantIO || calls != tc.wantModels {
				t.Fatalf("预算结果 = %+v，调用次数 = %d，错误 = %v", result, calls, err)
			}
		})
	}
}

func TestRunRetainsGoalAfterWorkingMemorySlides(t *testing.T) {
	dir := t.TempDir()
	// 用不同文件形成真实的长读取历史，避免通过无目的重复读取触发效率策略。
	for i := 1; i < 13; i++ {
		writeFixture(t, dir, fmt.Sprintf("source-%d.txt", i), "内容")
	}
	const goal = "原始任务锚点 42：只读并核对指定文件"
	calls := 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls < 13 {
			return modelCalls(readCall(strconv.Itoa(calls), fmt.Sprintf("source-%d.txt", calls)))
		}
		if !strings.Contains(messages[0].Content, goal) {
			t.Fatalf("滑动窗口后系统上下文丢失目标: %q", messages[0].Content)
		}
		if messages[1].Role != schema.RoleUser || messages[1].ToolCallID != "" {
			t.Fatalf("滑动窗口未以普通用户消息开场: %#v", messages[1])
		}
		pending := make(map[string]bool)
		for _, msg := range messages[1:] {
			if msg.Role == schema.RoleUser && msg.ToolCallID == "" && msg.Content == goal {
				t.Fatal("原始目标仍在滚动窗口内，未触发目标独立保留场景")
			}
			for _, call := range msg.ToolCalls {
				pending[call.ID] = true
			}
			if msg.ToolCallID != "" {
				if !pending[msg.ToolCallID] {
					t.Fatalf("出现孤立的工具观察: %s", msg.ToolCallID)
				}
				delete(pending, msg.ToolCallID)
			}
		}
		if len(pending) != 0 {
			t.Fatalf("缺失工具观察: %v", pending)
		}
		return reply("核对完成。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("长任务", dir), TaskSpec{Goal: goal}, nil)
	if err != nil || result.Status != Answered || calls != 13 {
		t.Fatalf("结果 = %+v，调用次数 = %d，错误 = %v", result, calls, err)
	}
}

func TestChangeCompletionRequiresCurrentVerification(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		status        TaskStatus
		wantEvidence  bool
	}{
		{name: "模型声称完成", status: NeedsVerification},
		{name: "实际通过", command: "test -f source.txt", status: Verified, wantEvidence: true},
		{name: "非零退出", command: "exit 7", status: NeedsVerification, wantEvidence: true},
		{name: "验收期间修改代码", command: "printf changed > source.txt", status: NeedsVerification, wantEvidence: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "原始内容")
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return reply("任务完成。")
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("验收", dir), TaskSpec{
				Goal: "修改并检查文件", Kind: TaskChange, VerifyCommand: tc.command,
			}, nil)
			if err != nil || result.Status != tc.status || (result.Verification != nil) != tc.wantEvidence {
				t.Fatalf("结果 = %+v，错误 = %v", result, err)
			}
			if result.Verification != nil && (result.Verification.Passed != (tc.status == Verified) || result.Verification.Version == "") {
				t.Fatalf("验收证据不匹配: %+v", result.Verification)
			}
			if tc.name == "验收期间修改代码" && !strings.Contains(result.Verification.Output, "版本不同") {
				t.Fatalf("验收缺少版本变化原因: %+v", result.Verification)
			}
		})
	}
}

func TestResumeCannotReuseStaleVerification(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "expected")
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return reply("已检查。")
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
	sess := ctxpkg.NewSession("恢复验收", dir)
	initial, err := eng.Run(context.Background(), sess, TaskSpec{
		Goal: "验收指定文件", Kind: TaskChange, VerifyCommand: "grep -qx expected source.txt",
	}, nil)
	if err != nil || initial.Status != Verified {
		t.Fatalf("首次验收结果 = %+v，错误 = %v", initial, err)
	}
	writeFixture(t, dir, "source.txt", "changed")
	resumed, err := eng.Run(context.Background(), sess, TaskSpec{ID: initial.TaskID, Resume: true}, nil)
	if err != nil || resumed.Status != NeedsVerification || resumed.Verification == nil || resumed.Verification.Passed {
		t.Fatalf("恢复沿用了过期验收: %+v，错误 = %v", resumed, err)
	}
}

func TestResumePreservesConsumedModelBudget(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "内容")
	sess := ctxpkg.NewSession("恢复预算", dir)
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("读取", "source.txt"))
		}
		return reply("已读取文件。")
	})
	limited := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 1}})
	first, err := limited.Run(context.Background(), sess, TaskSpec{Goal: "读取文件"}, nil)
	if err != nil || first.Status != BudgetExhausted || first.Usage.ModelCalls != 1 {
		t.Fatalf("首次运行 = %+v，错误 = %v", first, err)
	}
	resume := TaskSpec{ID: first.TaskID, Resume: true}
	second, err := limited.Run(context.Background(), sess, resume, nil)
	if err != nil || second.Status != BudgetExhausted || second.Usage.ModelCalls != 1 || calls != 1 {
		t.Fatalf("恢复后的原额度 = %+v，调用次数 = %d，错误 = %v", second, calls, err)
	}
	extended := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 2}})
	third, err := extended.Run(context.Background(), sess, resume, nil)
	if err != nil || third.Status != Answered || third.Usage.ModelCalls != 2 || calls != 2 {
		t.Fatalf("显式调高额度后的恢复 = %+v，调用次数 = %d，错误 = %v", third, calls, err)
	}
}

func TestResumeDoesNotReplayRecordedShellEffect(t *testing.T) {
	dir := t.TempDir()
	sess := ctxpkg.NewSession("恢复已结束命令", dir)
	call := schema.ToolCall{
		ID: "首次命令", Name: "bash",
		Arguments: json.RawMessage(`{"command":"printf x >> proof.txt"}`),
	}
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(call)
		case 2:
			return nil, errors.New("模拟任务在命令成功后被中断")
		case 3:
			repeated := call
			repeated.ID = "恢复后再次提议"
			return modelCalls(repeated)
		default:
			return reply("操作已结束。")
		}
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
	first, err := eng.Run(context.Background(), sess, TaskSpec{Goal: "追加标记一次"}, nil)
	if err == nil || first.Status != Failed {
		t.Fatalf("模拟中断结果 = %+v，错误 = %v", first, err)
	}
	resumed, err := eng.Run(context.Background(), sess, TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || resumed.Status != NeedsVerification {
		t.Fatalf("恢复后的结果 = %+v，错误 = %v", resumed, err)
	}
	marker, err := os.ReadFile(filepath.Join(dir, "proof.txt"))
	if err != nil || string(marker) != "x" {
		t.Fatalf("恢复后命令效果重复，内容 = %q，错误 = %v", marker, err)
	}
}

type recordingRegistry struct {
	tools.Registry
	executed []schema.ToolCall
}

func (r *recordingRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	r.executed = append(r.executed, call)
	return r.Registry.Execute(ctx, call)
}

func saveInterruptedTask(t *testing.T, dir string, sess *ctxpkg.Session, pending pendingAction) string {
	t.Helper()
	workDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.Open(workDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := taskstore.NewID()
	task, err := store.CreateTask(id)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	state := taskState{
		Version:   1,
		Spec:      TaskSpec{ID: id, Goal: "恢复被中断的文件修改", Kind: TaskChange, Mode: tools.ModeTrusted},
		SessionID: sess.ID,
		WorkDir:   workDir,
		Limits:    (Budget{}).defaults(),
		Pending:   map[string]pendingAction{pending.ID: pending},
	}
	if _, err := task.Append("action_started", state); err != nil {
		t.Fatal(err)
	}
	if err := task.SaveSnapshot(state); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResumeReconcilesConfirmedFileWriteWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	sess := ctxpkg.NewSession("恢复文件", dir)
	call := schema.ToolCall{
		ID: "原调用", Name: "write_file",
		Arguments: json.RawMessage(`{"path":"source.txt","content":"已生效","expected_version":"missing"}`),
	}
	scope := tools.WithScope(context.Background(), tools.ExecutionScope{Mode: tools.ModeTrusted, WorkDir: dir})
	preview, err := tools.PreviewWrite(scope, call)
	if err != nil {
		t.Fatal(err)
	}
	id := saveInterruptedTask(t, dir, sess, pendingAction{
		ID: "动作", Call: call,
		Before:   &fileVersion{Path: preview.Path, Version: preview.Before},
		Expected: &fileVersion{Path: preview.Path, Version: preview.After},
	})
	// 模拟动作已写出文件，但还没记录动作结果就中断。
	writeFixture(t, dir, "source.txt", "已生效")
	reg := &recordingRegistry{Registry: NewWorkspaceRegistry(dir)}
	calls := 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			repeated := call
			repeated.ID = "恢复后调用"
			return modelCalls(repeated)
		}
		last := messages[len(messages)-1]
		if last.ToolCallID != "恢复后调用" || !strings.Contains(last.Content, "恢复时已核实文件效果") {
			t.Fatalf("模型未得到已确认的效果: %#v", last)
		}
		return reply("文件已存在。")
	})
	eng := NewAgentEngine(provider, reg, EngineOptions{Mode: tools.ModeTrusted})
	result, err := eng.Run(context.Background(), sess, TaskSpec{ID: id, Resume: true}, nil)
	if err != nil || result.Status != NeedsVerification || calls != 2 || len(reg.executed) != 0 {
		t.Fatalf("恢复结果 = %+v，工具 = %+v，模型调用 = %d，错误 = %v", result, reg.executed, calls, err)
	}
}

func TestResumeBlocksUnknownShellBeforeCallingModel(t *testing.T) {
	dir := t.TempDir()
	sess := ctxpkg.NewSession("恢复命令", dir)
	id := saveInterruptedTask(t, dir, sess, pendingAction{
		ID: "未知动作", Call: schema.ToolCall{
			ID: "原调用", Name: "bash", Arguments: json.RawMessage(`{"command":"printf replay > replay.txt"}`),
		},
	})
	reg := &recordingRegistry{Registry: NewWorkspaceRegistry(dir)}
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("效果未知时不应调用模型")
		return nil, nil
	})
	eng := NewAgentEngine(provider, reg, EngineOptions{Mode: tools.ModeTrusted})
	result, err := eng.Run(context.Background(), sess, TaskSpec{ID: id, Resume: true}, nil)
	if err != nil || result.Status != Blocked || !strings.Contains(result.Reason, "效果未知") || len(reg.executed) != 0 {
		t.Fatalf("效果未知的恢复结果 = %+v，工具 = %+v，错误 = %v", result, reg.executed, err)
	}
}

func readPlanSnapshot(t *testing.T, dir, id string) taskState {
	t.Helper()
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.OpenTask(id)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	snapshot, err := task.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	events, err := task.Events()
	if err != nil || len(events) == 0 || snapshot.Sequence != events[len(events)-1].Sequence {
		t.Fatalf("检查点没有对应任务终态：%+v，事件数 %d，错误 %v", snapshot, len(events), err)
	}
	var state taskState
	if err := json.Unmarshal(snapshot.State, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func taskPlanView(t *testing.T, dir, id, name string) string {
	t.Helper()
	view, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", id, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(view)
}

func requireStepPrompt(t *testing.T, messages []schema.Message, goal string) {
	t.Helper()
	step, _ := json.Marshal(goal)
	if !strings.Contains(messages[0].Content, `"step":`+string(step)) {
		t.Fatalf("未调度预期步骤 %q，系统提示 %q", goal, messages[0].Content)
	}
}

func TestPlanRunsReadyStepsAndWritesTaskViews(t *testing.T) {
	dir := t.TempDir()
	id := taskstore.NewID()
	const goal = "先创建文件再核对"
	const first, second = "创建测试文件", "核对文件内容"
	for _, name := range []string{"PLAN.md", "TODO.md"} {
		writeFixture(t, dir, name, "工作区已有文件")
	}
	calls := 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			if len(defs) != 0 {
				t.Fatalf("计划提议阶段不应暴露工具：%v", defs)
			}
			// 列表顺序与依赖顺序相反。
			return reply(`[{"id":"check","goal":"核对文件内容","depends_on":["create"]},{"id":"create","goal":"创建测试文件","depends_on":[]}]`)
		case 2:
			requireStepPrompt(t, messages, first)
			return modelCalls(schema.ToolCall{ID: "创建", Name: "write_file", Arguments: json.RawMessage(`{"path":"source.txt","content":"计划步骤的产物","expected_version":"missing"}`)})
		case 3:
			requireStepPrompt(t, messages, first)
			return reply("文件已创建。")
		case 4:
			requireStepPrompt(t, messages, second)
			view := taskPlanView(t, dir, id, "TODO.md")
			if !strings.Contains(view, "- [x] create — "+first) || !strings.Contains(view, "- [ ] check — "+second) {
				t.Fatalf("后续步骤开始时视图未反映就绪进度：%q", view)
			}
			return modelCalls(readCall("核对", "source.txt"))
		case 5:
			last := messages[len(messages)-1]
			if last.ToolCallID != "核对" || !strings.Contains(last.Content, "计划步骤的产物") {
				t.Fatalf("后续步骤没有读取前置步骤产物：%+v", last)
			}
			return reply("已核对内容。")
		default:
			t.Fatalf("非预期的模型调用 %d", calls)
			return nil, nil
		}
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("计划", dir), TaskSpec{ID: id, Goal: goal, Plan: true}, nil)
	if err != nil || result.Status != NeedsVerification || calls != 5 {
		t.Fatalf("计划运行结果 = %+v，调用次数 = %d，错误 = %v", result, calls, err)
	}
	state := readPlanSnapshot(t, dir, id)
	if len(state.Plan) != 2 || state.Plan[0].ID != "check" || !state.Plan[0].Done || state.Plan[1].ID != "create" || !state.Plan[1].Done {
		t.Fatalf("持久计划状态不符：%+v", state.Plan)
	}
	plan, todo := taskPlanView(t, dir, id, "PLAN.md"), taskPlanView(t, dir, id, "TODO.md")
	if !strings.Contains(plan, goal) || !strings.Contains(plan, "依赖 create") ||
		!strings.Contains(todo, "- [x] check — "+state.Plan[0].Goal) || !strings.Contains(todo, "- [x] create — "+state.Plan[1].Goal) {
		t.Fatalf("计划视图与快照不一致：计划 %q，待办 %q", plan, todo)
	}
	for _, name := range []string{"PLAN.md", "TODO.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "工作区已有文件" {
			t.Fatalf("工作区已有 %s 被覆盖：%q，错误 %v", name, data, err)
		}
	}
}

func TestPlanRejectsInvalidDependenciesBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name, plan, reason string
	}{
		{name: "循环", plan: `[{"id":"a","goal":"A","depends_on":["b"]},{"id":"b","goal":"B","depends_on":["a"]}]`, reason: "循环依赖"},
		{name: "未知", plan: `[{"id":"a","goal":"A","depends_on":["missing"]}]`, reason: "未知依赖"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls != 1 {
					t.Fatal("无效计划被调度")
				}
				return reply(tc.plan)
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("无效计划", dir), TaskSpec{Goal: "执行计划", Plan: true}, nil)
			if result.ExitCode() == 0 || calls != 1 || result.Usage.ToolCalls != 0 || !strings.Contains(result.Reason, tc.reason) {
				t.Fatalf("无效计划未被拒绝：结果 %+v，调用次数 %d，错误 %v", result, calls, err)
			}
		})
	}
}

func TestPlanPatchPreservesCompletedSteps(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		valid       bool
	}{
		{name: "替换待执行步骤", patch: `[{"id":"replacement","goal":"改用读取核实","depends_on":["anchor"],"done":true}]`, valid: true},
		{name: "企图重写完成步骤", patch: `[{"id":"anchor","goal":"错误地改写已完成步骤","depends_on":[]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			provider := scriptedProvider(func(_ context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				switch calls {
				case 1:
					return reply(`[{"id":"anchor","goal":"完成基础记录","depends_on":[]},{"id":"blocked","goal":"尝试受阻步骤","depends_on":["anchor"]}]`)
				case 2:
					requireStepPrompt(t, messages, "完成基础记录")
					return reply("基础记录已完成。")
				case 3:
					requireStepPrompt(t, messages, "尝试受阻步骤")
					return reply("")
				case 4:
					last := messages[len(messages)-1]
					if len(defs) != 0 || !strings.Contains(last.Content, "阻塞原因") || !strings.Contains(last.Content, `"done":true`) {
						t.Fatalf("补丁模型未得到阻塞与已完成状态：%+v", last)
					}
					return reply(tc.patch)
				case 5:
					if !tc.valid {
						t.Fatal("重写完成步骤的补丁被调度")
					}
					requireStepPrompt(t, messages, "改用读取核实")
					return reply("已执行替代步骤。")
				default:
					t.Fatalf("非预期的模型调用 %d", calls)
					return nil, nil
				}
			})
			eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("计划补丁", dir), TaskSpec{Goal: "规划并处理阻塞", Plan: true}, nil)
			state := readPlanSnapshot(t, dir, result.TaskID)
			if len(state.Plan) != 2 || state.Plan[0].ID != "anchor" || state.Plan[0].Goal != "完成基础记录" || !state.Plan[0].Done {
				t.Fatalf("完成步骤未被冻结：%+v", state.Plan)
			}
			if tc.valid {
				if err != nil || result.Status != Answered || calls != 5 || state.Plan[1].ID != "replacement" || !state.Plan[1].Done {
					t.Fatalf("有效补丁未被执行：结果 %+v，计划 %+v，调用 %d，错误 %v", result, state.Plan, calls, err)
				}
			} else if result.ExitCode() == 0 || calls != 4 || state.Plan[1].ID != "blocked" || state.Plan[1].Done {
				t.Fatalf("无效补丁改变了计划：结果 %+v，计划 %+v，调用 %d，错误 %v", result, state.Plan, calls, err)
			}
		})
	}
}

func TestReadOnlyModeRejectsVerificationOnRunAndResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "直接执行"
		if resume {
			name = "恢复时降权"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sess := ctxpkg.NewSession("验收权限", dir)
			spec := TaskSpec{Goal: "检查任务", Kind: TaskChange, VerifyCommand: "printf unauthorized > proof.txt"}
			if resume {
				interrupted := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
					return nil, errors.New("模拟进入验收之前中断")
				})
				trusted := NewAgentEngine(interrupted, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
				initial, err := trusted.Run(context.Background(), sess, spec, nil)
				if err == nil || initial.Status != Failed {
					t.Fatalf("前置运行未在验收之前中断：%+v，错误 %v", initial, err)
				}
				spec = TaskSpec{ID: initial.TaskID, Resume: true}
			}
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				t.Fatal("权限不足的任务不应调用模型")
				return nil, nil
			})
			reg := &recordingRegistry{Registry: NewWorkspaceRegistry(dir)}
			readonly := NewAgentEngine(provider, reg, EngineOptions{Mode: tools.ModeReadOnly})
			result, err := readonly.Run(context.Background(), sess, spec, nil)
			if err != nil || result.Status != Blocked || !strings.Contains(result.Reason, "权限不足") || len(reg.executed) != 0 {
				t.Fatalf("只读验收未被阻塞：%+v，工具 %+v，错误 %v", result, reg.executed, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "proof.txt")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("验收命令可能已执行：%v", err)
			}
		})
	}
}
