package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type AgentEngine struct {
	provider  provider.LLMProvider
	registry  tools.Registry
	options   EngineOptions
	compactor *ctxpkg.Compactor
	recovery  *ctxpkg.RecoveryManager
}

func NewAgentEngine(p provider.LLMProvider, registry tools.Registry, options EngineOptions) *AgentEngine {
	if options.Mode == "" {
		options.Mode = tools.ModeReadOnly
	}
	if options.MaxContextBytes == 0 {
		options.MaxContextBytes = 300000
	}
	return &AgentEngine{
		provider: p, registry: registry, options: options,
		compactor: ctxpkg.NewCompactor(options.MaxContextBytes, 10),
		recovery:  ctxpkg.NewRecoveryManager(),
	}
}

func newID() string { return taskstore.NewID() }

func (e *AgentEngine) Run(parent context.Context, session *ctxpkg.Session, spec TaskSpec, reporter Reporter) (result RunResult, err error) {
	result.RunID = newID()
	if spec.ID == "" && !spec.Resume {
		spec.ID = newID()
	}
	result.TaskID = spec.ID
	result.Status = Failed
	if !e.options.Budget.valid() || e.options.MaxContextBytes < 1 {
		result.Reason = "运行预算配置无效"
		return result, errors.New(result.Reason)
	}
	if parent.Err() != nil {
		result.Status, result.Reason = Cancelled, parent.Err().Error()
		return result, nil
	}
	workDir, err := filepath.EvalSymlinks(session.WorkDir)
	if err == nil {
		workDir, err = filepath.Abs(workDir)
	}
	if err != nil {
		result.Reason = "无法定位工作区"
		return result, err
	}
	store, err := taskstore.Open(workDir)
	if errors.Is(err, taskstore.ErrBusy) {
		result.Status, result.Reason = Blocked, "工作区已有运行中的主任务"
		return result, nil
	}
	if err != nil {
		return result.stop(parent, fmt.Errorf("%w：无法打开工作区任务存储: %w", errStorage, err))
	}
	defer store.Close()
	task, state, err := e.openTask(store, workDir, session.ID, spec)
	if err != nil {
		return result.stop(parent, err)
	}
	defer task.Close()
	if state.LastResult != nil {
		previous := *state.LastResult
		previous.Delivery = deliveryFromState(state)
		if previous.Status == Answered {
			return previous, nil
		}
		if previous.Status == Verified {
			version, checkErr := WorkspaceVersion(parent, workDir)
			if checkErr != nil {
				return result.stop(parent, checkErr)
			}
			if previous.Verification != nil && previous.Verification.Version == version {
				return previous, nil
			}
			previous.Status = NeedsVerification
			previous.Reason = "工作区已变化，原验收证据已失效"
			previous.StopCode = "verification_stale"
			previous.FinalText = ""
			if previous.Verification != nil {
				evidence := *previous.Verification
				evidence.Passed = false
				previous.Verification = &evidence
			}
			return previous, nil
		}
	}
	ctx, cancel := context.WithTimeout(parent, state.Limits.Timeout-state.Usage.Elapsed)
	defer cancel()
	mode := state.Spec.Mode
	requested := spec.Mode
	if requested == "" {
		requested = e.options.Mode
	}
	if modeLevel(requested) < modeLevel(mode) {
		mode = requested
	}
	ctx = tools.WithScope(ctx, tools.ExecutionScope{Mode: mode, WorkDir: workDir})
	ctx, span := observability.StartSpan(ctx, "Agent.Run")
	span.AddAttribute("run_id", result.RunID)
	span.AddAttribute("task_id", result.TaskID)
	r := &runState{
		state: state, started: time.Now(), elapsed: state.Usage.Elapsed,
		persist: func(kind string, state taskState) error {
			if kind == "elapsed" {
				_, err := task.Append(kind, elapsedEvent{Version: state.Version, Elapsed: state.Usage.Elapsed})
				return err
			}
			if _, err := task.Append(kind, state); err != nil {
				return err
			}
			if len(state.Plan) > 0 && (kind == "plan" || kind == "plan_patch" || kind == "step_done" || kind == "finished") {
				if err := task.SavePlanViews(planViews(state)); err != nil {
					log.Printf("无法更新任务计划视图: %v", err)
				}
			}
			return nil
		},
		artifact: task.SaveArtifact, readArtifact: task.ReadArtifact, subSlots: make(chan struct{}, 2),
	}
	ctx = context.WithValue(ctx, runContextKey{}, r)
	clockDone, clockStop := make(chan error, 1), make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-clockStop:
				clockDone <- nil
				return
			case <-ctx.Done():
				clockDone <- nil
				return
			case <-ticker.C:
				if clockErr := r.record("elapsed", func(*taskState) error { return nil }); clockErr != nil {
					clockDone <- clockErr
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		close(clockStop)
		if clockErr := <-clockDone; clockErr != nil {
			err = errors.Join(err, clockErr)
			result.Status, result.Reason, result.StopCode = Failed, "无法保存任务计时", "storage_error"
		}
		if errors.Is(err, errStorage) {
			result.Status, result.Reason, result.StopCode = Failed, err.Error(), "storage_error"
		}
		if result.Status != Answered && result.Status != Verified && result.Status != NeedsVerification {
			result.FinalText = ""
		}
		if result.StopCode == "" {
			result.StopCode = strings.ToLower(string(result.Status))
		}
		if err != nil && result.Reason == "" {
			result.Reason = err.Error()
		}
		if saveErr := r.record("finished", func(s *taskState) error {
			result.Usage = s.Usage
			result.Usage.Elapsed = r.elapsed + time.Since(r.started)
			result.Verification = s.Verification
			result.Delivery = deliveryFromState(*s)
			s.LastResult = &result
			return nil
		}); saveErr != nil {
			err = errors.Join(err, saveErr)
			result.Status, result.Reason, result.StopCode = Failed, "无法保存任务终态", "storage_error"
			result.FinalText = ""
		} else if saveErr := task.SaveSnapshot(r.state); saveErr != nil {
			log.Printf("无法更新任务检查点，任务日志仍可用于恢复: %v", saveErr)
		}
		span.AddAttribute("status", result.Status)
		span.AddAttribute("stop_code", result.StopCode)
		span.AddAttribute("model_calls", result.Usage.ModelCalls)
		span.AddAttribute("tool_calls", result.Usage.ToolCalls)
		span.EndSpan()
		if traceErr := observability.ExportTraceToFile(span, workDir, result.RunID); traceErr != nil {
			log.Printf("无法导出任务追踪: %v", traceErr)
		}
	}()
	if err := r.record("started", func(s *taskState) error { return nil }); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result.stop(parent, err)
	}
	if reason, err := e.reconcile(ctx, r); err != nil {
		return result.stop(parent, err)
	} else if reason != "" {
		result.Status, result.Reason = Blocked, reason
		return result, nil
	}
	if spec.Resume {
		r.resumeReceipts = make(map[string]schema.ToolResult, len(r.state.Completed))
		for key, receipt := range r.state.Completed {
			r.resumeReceipts[key] = receipt
		}
	}
	if spec.Resume && state.Verification != nil && state.Verification.Passed {
		version, checkErr := WorkspaceVersion(ctx, workDir)
		if checkErr != nil {
			return result.stop(parent, checkErr)
		}
		if version == state.Verification.Version {
			result.Status, result.Reason = Verified, "已核实中断前保存的验收证据"
			return result, nil
		}
		if err := r.record("verification_stale", func(s *taskState) error {
			s.Verification.Passed = false
			return nil
		}); err != nil {
			return result, err
		}
	}
	if state.Spec.VerifyCommand != "" && mode != tools.ModeTrusted {
		result.Status, result.Reason = Blocked, "当前任务权限不足以执行已指定的验收命令"
		return result, nil
	}

	session.Append(schema.Message{Role: schema.RoleUser, Content: state.Spec.Goal})
	system := ctxpkg.NewPromptComposer(workDir, false).Build()
	system.Content += "\n\n执行模式：" + string(mode) + "。工具准入由程序控制。"
	if state.Spec.Plan && len(state.Plan) == 0 {
		steps, err := e.proposePlan(ctx, r, system, "")
		if err != nil {
			return result.stop(parent, err)
		}
		if err := r.record("plan", func(s *taskState) error { s.Plan = steps; return nil }); err != nil {
			return result, err
		}
	}
	text, err := e.runTask(ctx, r, session, system, reporter)
	if err != nil {
		return result.stop(parent, err)
	}
	result.FinalText = text
	switch {
	case r.state.Verification != nil && r.state.Verification.Passed:
		result.Status, result.Reason = Verified, "指定验收已通过，工作区版本已核对"
	case r.state.Spec.Kind == TaskChange || r.state.Spec.VerifyCommand != "":
		result.Status, result.Reason = NeedsVerification, "修改任务尚未取得有效验收证据"
	default:
		result.Status, result.Reason = Answered, "已交付回复"
	}
	return result, nil
}

func (r RunResult) stop(parent context.Context, err error) (RunResult, error) {
	r.FinalText = ""
	r.Reason = err.Error()
	var limit budgetLimit
	switch {
	case errors.Is(err, errStorage):
		r.Status, r.StopCode = Failed, "storage_error"
		return r, err
	case errors.Is(parent.Err(), context.Canceled), errors.Is(err, context.Canceled):
		r.Status, r.StopCode = Cancelled, "cancelled"
	case errors.As(err, &limit):
		r.Status, r.StopCode = BudgetExhausted, limit.code
	case errors.Is(err, errBudget):
		r.Status, r.StopCode = BudgetExhausted, "budget_exhausted"
	case errors.Is(err, context.DeadlineExceeded):
		r.Status, r.StopCode = BudgetExhausted, "deadline"
	case errors.Is(err, errContextBudget), errors.Is(err, errBlocked):
		r.Status, r.StopCode = Blocked, "blocked"
	default:
		r.Status = Failed
		return r, err
	}
	return r, nil
}

var errBlocked = errors.New("任务已阻塞")

func (e *AgentEngine) runTask(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, reporter Reporter) (string, error) {
	var text string
	patchUsed := false
	for {
		idx := readyStep(r.state.Plan)
		if idx < 0 {
			break
		}
		step := r.state.Plan[idx]
		var err error
		text, err = e.converse(ctx, r, session, system, e.registry, reporter, step.ID, step.Goal, 0)
		if err != nil {
			if !errors.Is(err, errBlocked) || len(r.state.Pending) != 0 || patchUsed {
				return text, err
			}
			patchUsed = true
			steps, patchErr := e.proposePlan(ctx, r, system, err.Error())
			if patchErr != nil {
				return text, patchErr
			}
			if err := r.record("plan_patch", func(s *taskState) error { s.Plan = steps; return nil }); err != nil {
				return text, err
			}
			continue
		}
		if err := r.record("step_done", func(s *taskState) error {
			s.Plan[idx].Done = true
			r.note(s, "步骤 "+step.ID+"： "+text)
			return nil
		}); err != nil {
			return text, err
		}
	}
	if len(r.state.Plan) == 0 {
		var err error
		text, err = e.converse(ctx, r, session, system, e.registry, reporter, "", "", 0)
		if err != nil {
			return text, err
		}
	}
	if r.state.Spec.VerifyCommand == "" {
		return text, nil
	}
	for {
		evidence, err := e.verify(ctx, r, reporter)
		if err != nil {
			return text, err
		}
		if err := r.record("verification", func(s *taskState) error { s.Verification = evidence; return nil }); err != nil {
			return text, err
		}
		if evidence.Passed {
			return text, nil
		}
		// 仅在修复改变工作区版本后重新执行验收。
		session.Append(schema.Message{Role: schema.RoleUser, Content: "指定验收未通过：\n" + evidence.Output})
		text, err = e.converse(ctx, r, session, system, e.registry, reporter, "verify-fix", "根据验收证据修复问题", 0)
		if err != nil {
			return text, err
		}
		current, err := WorkspaceVersion(ctx, r.state.WorkDir)
		if err != nil || current == evidence.Version {
			return text, err
		}
	}
}

func (e *AgentEngine) generate(ctx context.Context, r *runState, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	snapshot := r.budgetSnapshot()
	if deadline, ok := ctx.Deadline(); ok {
		snapshot.RemainingMS = min(snapshot.RemainingMS, max(int64(0), time.Until(deadline).Milliseconds()))
	}
	prepared, err := e.compactor.CompactWithin(withBudgetContext(msgs, snapshot), defs)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", errContextBudget, err)
	}
	if err := r.reserve(ctx, "model"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, span := observability.StartSpan(ctx, "LLM.Generate")
	defer span.EndSpan()
	msg, err := e.provider.Generate(callCtx, prepared, defs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("模型调用失败: %w", err)
	}
	if msg == nil {
		return nil, errors.New("模型没有返回消息")
	}
	return msg, nil
}

func (e *AgentEngine) converse(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, registry tools.Registry, reporter Reporter, stepID, stepGoal string, subTurns int) (string, error) {
	failures := make(map[string]int)
	for turn := 0; ; turn++ {
		if subTurns > 0 {
			if turn >= subTurns {
				return "", fmt.Errorf("%w：子任务达到 %d 轮", errBlocked, subTurns)
			}
		} else if err := r.reserve(ctx, "turn"); err != nil {
			return "", err
		}
		prompt := system
		if subTurns == 0 {
			anchor, _ := json.Marshal(struct {
				Goal     string   `json:"goal"`
				Step     string   `json:"step,omitempty"`
				Progress []string `json:"progress,omitempty"`
			}{r.state.Spec.Goal, stepGoal, r.state.Progress})
			prompt.Content += "\n\n当前任务（结构化数据）：" + string(anchor)
		}
		history := append([]schema.Message{prompt}, session.GetWorkingMemory(20)...)
		if subTurns == 0 && e.options.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}
			thinking, err := e.generate(ctx, r, history, nil)
			if err != nil {
				return "", err
			}
			if thinking.Content != "" {
				history = append(history, schema.Message{Role: schema.RoleAssistant, Content: thinking.Content, ResponseItems: thinking.ResponseItems})
			}
		}
		msg, err := e.generate(ctx, r, history, registry.VisibleTools(ctx))
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(msg.Content)
		if len(msg.ToolCalls) == 0 {
			if text == "" {
				return "", fmt.Errorf("%w：模型没有交付内容", errBlocked)
			}
			session.Append(schema.Message{Role: schema.RoleAssistant, Content: text, ResponseItems: msg.ResponseItems})
			return text, nil
		}
		seen := make(map[string]bool, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				return "", fmt.Errorf("%w：模型返回的工具调用 ID 缺失或重复", errBlocked)
			}
			seen[call.ID] = true
		}
		session.Append(schema.Message{Role: schema.RoleAssistant, Content: text, ToolCalls: msg.ToolCalls, ResponseItems: msg.ResponseItems})
		results, batchErr := e.executeBatch(ctx, r, registry, reporter, msg.ToolCalls, stepID)
		var observations []schema.Message
		var stopped string
		for i, result := range results {
			call := msg.ToolCalls[i]
			content := observation(result)
			if result.IsError {
				content = e.recovery.AnalyzeAndInject(call.Name, content)
			}
			observations = append(observations, schema.Message{Role: schema.RoleUser, ToolCallID: call.ID, ToolStatus: result.Status, Content: content})
			key := actionKey(stepID, call)
			if !result.IsError {
				delete(failures, key)
			} else {
				failures[key]++
				if failures[key] >= 3 {
					stopped = fmt.Sprintf("工具 %s 以相同参数连续失败三次", call.Name)
				}
			}
		}
		session.Append(observations...)
		if batchErr != nil {
			return "", batchErr
		}
		if stopped != "" {
			return "", fmt.Errorf("%w：%s", errBlocked, stopped)
		}
	}
}

func observation(result schema.ToolResult) string {
	header := fmt.Sprintf("执行状态：%s，效果：%s", result.Status, result.Effect)
	if result.ExitCode != nil {
		header += fmt.Sprintf("，退出码：%d", *result.ExitCode)
	}
	return header + "\n" + result.Output
}

func actionKey(stepID string, call schema.ToolCall) string {
	raw := []byte(call.Arguments)
	var data any
	if json.Unmarshal(raw, &data) == nil {
		raw, _ = json.Marshal(data)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(stepID+"\x00"+call.Name+"\x00"+string(raw))))
}

func (e *AgentEngine) executeBatch(ctx context.Context, r *runState, registry tools.Registry, reporter Reporter, calls []schema.ToolCall, stepID string) ([]schema.ToolResult, error) {
	results := make([]schema.ToolResult, len(calls))
	errs := make([]error, len(calls))
	next := 0
	var batchErr error
	for next < len(calls) && batchErr == nil {
		end := next + 1
		name := calls[next].Name
		if tools.IsReadOnly(name) {
			for end < len(calls) && end-next < 4 && tools.IsReadOnly(calls[end].Name) {
				end++
			}
		} else if name == "spawn_subagent" && end < len(calls) && calls[end].Name == name {
			end++
		}
		if end-next == 1 {
			results[next], errs[next] = e.execute(ctx, r, registry, reporter, calls[next], stepID, false)
		} else {
			var wg sync.WaitGroup
			for i := next; i < end; i++ {
				wg.Go(func() { results[i], errs[i] = e.execute(ctx, r, registry, reporter, calls[i], stepID, false) })
			}
			wg.Wait()
		}
		for i := next; i < end; i++ {
			if errs[i] != nil {
				batchErr = errs[i]
				break
			}
		}
		next = end
	}
	for i := range results {
		if results[i].Status == "" {
			results[i] = schema.ToolResult{ToolCallID: calls[i].ID, Status: schema.ToolRejected, Effect: schema.EffectNone, IsError: true, Output: "任务已停止，本次调用未执行"}
		}
	}
	return results, batchErr
}
