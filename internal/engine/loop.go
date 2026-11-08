package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
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
	parent = observe.WithTask(parent, spec.ID)
	// 最先注册，最后执行：此时任务关闭、快照与 Trace 收尾均已结束。
	defer func() { observe.Emit(parent, "run_finalized", ObservationResult(result, err)) }()
	result.Status = Failed
	if !e.options.Budget.valid() || e.options.MaxContextBytes < 1 || e.options.AnalysisContextBytes < 0 {
		result.Reason = "运行预算配置无效"
		return result, errors.New(result.Reason)
	}
	if err := parent.Err(); err != nil {
		return result.stop(parent, err)
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
	// 直接调用引擎也需要内部根身份；不创建 Recorder、不强制新增观测落盘。
	if observe.Identity(parent).AgentID == "" {
		parent = observe.WithAgent(parent, newID(), "")
	}
	observedMode := state.Spec.Mode
	requestedMode := spec.Mode
	if requestedMode == "" {
		requestedMode = e.options.Mode
	}
	if modeLevel(requestedMode) < modeLevel(observedMode) {
		observedMode = requestedMode
	}
	observe.Emit(parent, "run_configured", observe.Payload{
		DeliveryReview:     deliveryReviewObservation(recordedDeliveryReviewSummary(state), state.Spec.DeliveryReview != nil),
		ReadFeatures:       observationReadFeatures(state.Spec),
		ReadContextVersion: int64(state.Spec.ReadContextVersion),
		ReadPageBytes:      int64(effectivePageBytes(state.Spec.ReadPageBytes)), Analysis: state.Spec.Analysis, Delegation: state.Spec.Delegation,
		Kind: string(state.Spec.Kind), Mode: string(observedMode), Goal: state.Spec.Goal,
		MaxModelCalls: int64(state.Limits.MaxModelCalls), MaxToolCalls: int64(state.Limits.MaxToolCalls),
		MaxTurns: int64(state.Limits.MaxTurns), TimeoutMS: state.Limits.Timeout.Milliseconds(),
		ModelCalls: int64(state.Usage.ModelCalls), ToolCalls: int64(state.Usage.ToolCalls), Turns: int64(state.Usage.Turns), ElapsedMS: state.Usage.Elapsed.Milliseconds(),
	})
	if state.LastResult != nil && state.Spec.Quality == nil {
		previous := *state.LastResult
		previous.Delivery = deliveryFromState(state)
		if previous.DeliveryReview != nil {
			copied := *previous.DeliveryReview
			copied.RecordedOnly = true
			previous.DeliveryReview = &copied
		}
		if previous.Status == Answered {
			if previous.Reading != nil {
				reading := *previous.Reading
				reading.RecordedOnly = true
				previous.Reading = &reading
				previous.Reason = "复用历史答复；读取覆盖仅代表原运行，未重新核对当前文件"
			}
			if state.Spec.Analysis {
				previous.Reason = "复用历史分析答复；引用仅说明原运行当时的读取版本，未重新核对当前文件"
			}
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
	ctx = tools.WithScope(ctx, tools.ExecutionScope{Mode: mode, WorkDir: workDir, ReadPageBytes: effectivePageBytes(state.Spec.ReadPageBytes), ReportOnly: state.Spec.Report != nil, DisableSubagent: state.Spec.Report != nil || state.Spec.Quality != nil})
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
			event, err := task.Append(kind, state)
			if err != nil {
				return err
			}
			observe.Emit(observe.WithSourceSequence(ctx, event.Sequence), "task_event_committed", observe.Payload{SourceKind: kind, ModelCalls: int64(state.Usage.ModelCalls), ToolCalls: int64(state.Usage.ToolCalls), Turns: int64(state.Usage.Turns), ElapsedMS: state.Usage.Elapsed.Milliseconds()})
			if len(state.Plan) > 0 && (kind == "plan" || kind == "plan_patch" || kind == "step_done" || kind == "finished") {
				if err := task.SavePlanViews(planViews(state)); err != nil {
					log.Printf("无法更新任务计划视图: %v", err)
				}
			}
			return nil
		},
		artifact: task.SaveArtifact, readArtifact: task.ReadArtifact, readArtifactPage: task.ReadArtifactPage,
		runID: result.RunID, subSlots: make(chan struct{}, 2),
	}
	ctx = context.WithValue(ctx, runContextKey{}, r)
	var system schema.Message
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
		if r.readonlyAnswer() || r.readMemoryEnabled() {
			result.Reading = r.readingCoverage(ctx)
		}
		if r.state.Spec.Report != nil {
			var reportErr error
			result.Report, reportErr = r.reportDelivery()
			if reportErr != nil {
				err = errors.Join(err, reportErr)
			}
		}
		if r.analysisEnabled() {
			result.Analysis = r.analysisReport(ctx)
		}
		result.Quality = r.qualityDelivery()
		r.mu.Lock()
		result.LocalChecks = append([]LocalCheckSummary(nil), r.state.LocalChecks...)
		r.mu.Unlock()
		if storageErr := r.storageError(); storageErr != nil {
			err = errors.Join(err, storageErr)
		}
		if errors.Is(err, errStorage) {
			result.Status, result.Reason, result.StopCode = Failed, err.Error(), "storage_error"
		}
		if result.Status != Answered && result.Status != Verified && result.Status != NeedsVerification {
			partial, partialErr := e.partialForStop(ctx, r, session, system, result)
			result.PartialReport = partial
			if partialErr != nil {
				err = errors.Join(err, partialErr)
				result.Status, result.Reason, result.StopCode = Failed, partialErr.Error(), "storage_error"
			} else if (result.Status == BudgetExhausted || result.StopCode == "readonly_repeat") && errors.Is(ctx.Err(), context.Canceled) {
				result.Status, result.Reason, result.StopCode = Cancelled, "任务已取消", "cancelled"
			}
		}
		if reviewErr := r.finalizeDeliveryReview(ctx); reviewErr != nil {
			err = errors.Join(err, reviewErr)
			result.Status, result.Reason, result.StopCode = Failed, reviewErr.Error(), "storage_error"
		}
		result.DeliveryReview = r.deliveryReviewSummary(context.WithoutCancel(ctx))
		var finalContentIssues []DeliveryReviewIssue
		if result.FinalText != "" && (result.Status == Answered || result.Status == Verified || result.Status == NeedsVerification) {
			finalContentIssues = contentReviewIssues(r.reviewDeliveryContent(context.WithoutCancel(ctx), "", result.FinalText))
		}
		// 收尾仍属于执行时间。计时落盘持续覆盖最后一次模型请求，之后才汇总故障。
		close(clockStop)
		if clockErr := <-clockDone; clockErr != nil {
			err = errors.Join(err, clockErr)
			result.Status, result.Reason, result.StopCode = Failed, "无法保存任务计时", "storage_error"
		}
		if storageErr := r.storageError(); storageErr != nil {
			err = errors.Join(err, storageErr)
			result.Status, result.Reason, result.StopCode = Failed, storageErr.Error(), "storage_error"
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
			recordDeliveryReviewAnswer(s, result)
			if result.DeliveryReview != nil && s.DeliveryReview != nil {
				if result.FinalText != "" {
					result.DeliveryReview.Unresolved = append(result.DeliveryReview.Unresolved, finalContentIssues...)
				}
				result.DeliveryReview.ContentComparisons = append([]DeliveryReviewContentComparison(nil), s.DeliveryReview.ContentComparisons...)
				s.DeliveryReview.Summary = result.DeliveryReview
			}
			result.Usage = s.Usage
			result.Usage.Elapsed = r.elapsed + time.Since(r.started)
			result.Verification = s.Verification
			result.Delivery = deliveryFromState(*s)
			result.Artifacts = artifactsFromDelivery(result.Delivery)
			if s.Spec.Analysis && analysisStateOf(*s) != nil {
				if report, ok := analysisStateOf(*s).Reports[observe.Identity(ctx).AgentID]; ok {
					result.Analysis = &report
				}
			}
			s.LastResult = &result
			s.Reading = result.Reading
			return nil
		}); saveErr != nil {
			err = errors.Join(err, saveErr)
			result.Status, result.Reason, result.StopCode = Failed, "无法保存任务终态", "storage_error"
			result.FinalText = ""
			result.PartialReport = r.recordedPartial(result.Reason, "任务记录未可靠保存，以下仅为现场执行观察。")
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
	if err := r.markInterruptedDeliveryReview(); err != nil {
		return result, err
	}
	if err := r.record("started", func(s *taskState) error { s.Reading = nil; return nil }); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result.stop(ctx, err)
	}
	if reason, err := e.reconcile(ctx, r); err != nil {
		return result.stop(ctx, err)
	} else if reason != "" {
		result.Status, result.Reason = Blocked, reason
		return result, nil
	}
	if state.Spec.Report != nil {
		if done, recoverErr := r.recoverReportPublication(ctx); recoverErr != nil {
			return result.stop(ctx, recoverErr)
		} else if done {
			if err := ctx.Err(); err != nil {
				return result.stop(ctx, err)
			}
			out, reportErr := r.reportSummaryResult(result)
			if reportErr != nil {
				return out.stop(ctx, reportErr)
			}
			return out, nil
		}
	}
	if r.state.Spec.Report != nil {
		if targetErr := r.checkReportTarget(ctx); targetErr != nil {
			return result.stop(ctx, targetErr)
		}
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
			return result.stop(ctx, checkErr)
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

	if r.qualityEnabled() && r.state.Quality.DraftID != "" {
		text, qualityErr := e.resumeQuality(ctx, r, session, reporter)
		if qualityErr != nil {
			return result.stop(ctx, qualityErr)
		}
		if r.state.Spec.Report != nil {
			return r.reportSummaryResult(result)
		}
		result.Status, result.Reason, result.FinalText = Answered, "已交付质量分析；语义验收未运行", text
		return result, nil
	}
	r.bindTaskHistory(session)
	session.Append(schema.Message{Role: schema.RoleUser, Content: state.Spec.Goal})
	system = ctxpkg.NewPromptComposer(workDir, false).Build()
	system.Content += "\n\n执行模式：" + string(mode) + "。工具准入由程序控制。"
	system.Content += runtimeWorkspaceContext(workDir)
	if spec.Resume {
		if err := r.resumedReadonlyStop(ctx); err != nil {
			return result.stop(ctx, err)
		}
	}
	if r.toolsExhausted() {
		return result.stop(ctx, toolLimit())
	}
	if state.Spec.Plan && len(state.Plan) == 0 {
		steps, err := e.proposePlan(ctx, r, system, "")
		if err != nil {
			return result.stop(ctx, err)
		}
		if err := r.record("plan", func(s *taskState) error { s.Plan = steps; return nil }); err != nil {
			return result, err
		}
	}
	text, err := e.runTask(ctx, r, session, system, reporter)
	if err != nil {
		return result.stop(ctx, err)
	}
	if r.state.Spec.Report != nil {
		if err := ctx.Err(); err != nil {
			return result.stop(ctx, err)
		}
		out, reportErr := r.reportSummaryResult(result)
		if reportErr != nil {
			return out.stop(ctx, reportErr)
		}
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return result.stop(ctx, err)
	}
	if err := r.checkReadingComplete(ctx); err != nil {
		return result.stop(ctx, err)
	}
	result.FinalText = text
	if r.analysisEnabled() {
		result.Analysis = r.analysisReport(ctx)
	}
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

func (r RunResult) stop(ctx context.Context, err error) (RunResult, error) {
	r.FinalText = ""
	r.Reason = err.Error()
	var limit budgetLimit
	var readonlyStop readonlyRepeatStop
	var reportStop reportError
	var networkError net.Error
	switch {
	case errors.Is(err, errStorage):
		r.Status, r.StopCode = Failed, "storage_error"
		return r, err
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		r.Status, r.StopCode = Cancelled, "cancelled"
	case errors.As(err, &limit):
		r.Status, r.StopCode = BudgetExhausted, limit.code
	case errors.Is(err, errBudget):
		r.Status, r.StopCode = BudgetExhausted, "budget_exhausted"
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Status, r.StopCode = BudgetExhausted, "deadline"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
		// 请求自身的超时不能证明任务累计时限已到达。
		r.Status, r.StopCode = Failed, "request_timeout"
		return r, err
	case errors.As(err, &readonlyStop):
		r.Status, r.StopCode = Blocked, "readonly_repeat"
	case errors.As(err, &reportStop):
		r.Status, r.StopCode = Blocked, reportStop.code
	case errors.Is(err, errAnalysisReportInvalid):
		r.Status, r.StopCode = Blocked, "analysis_report_invalid"
	case errors.Is(err, errContextBudget), errors.Is(err, errBlocked):
		r.Status, r.StopCode = Blocked, "blocked"
	default:
		if kind := provider.FailureKind(err); kind != "" {
			r.Status, r.StopCode = Failed, kind
			return r, err
		}
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
			var readonlyStop readonlyRepeatStop
			if errors.As(err, &readonlyStop) {
				return text, err
			}
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
	return e.generateRequest(ctx, r, msgs, defs, false)
}

// generateRequest 共享预算上下文、压缩、调用和观测。收尾使用原子准入而不是再次扣模型次数。
func (e *AgentEngine) generateRequest(ctx context.Context, r *runState, msgs []schema.Message, defs []schema.ToolDefinition, closing bool) (*schema.Message, error) {
	snapshot := r.requestBudgetSnapshot(ctx)
	reviewRequest, _ := ctx.Value(deliveryReviewRequestKey{}).(bool)
	if reviewRequest {
		snapshot.ModelCallsRemaining++
	}
	if deadline, ok := ctx.Deadline(); ok {
		snapshot.RemainingMS = min(snapshot.RemainingMS, max(int64(0), time.Until(deadline).Milliseconds()))
	}
	purpose, _ := ctx.Value(qualityPurposeKey{}).(string)
	request := append([]schema.Message(nil), msgs...)
	if purpose == "" {
		if block := outputStylePrompt(r.state.Spec); block != "" {
			if len(request) > 0 && request[0].Role == schema.RoleSystem {
				request[0].Content += "\n" + block
			} else {
				request = append([]schema.Message{{Role: schema.RoleSystem, Content: block}}, request...)
			}
		}
		if block := r.readingContext(ctx); block != "" {
			if len(request) > 0 && request[0].Role == schema.RoleSystem {
				request[0].Content += "\n" + block
			} else {
				request = append([]schema.Message{{Role: schema.RoleSystem, Content: block}}, request...)
			}
		}
		if block := func() string {
			if r.analysisEnabled() {
				return ""
			}
			return r.readContext(ctx)
		}(); block != "" {
			if len(request) > 0 && request[0].Role == schema.RoleSystem {
				request[0].Content += "\n\n" + block
			} else {
				request = append([]schema.Message{{Role: schema.RoleSystem, Content: block}}, request...)
			}
		}
	}
	var prepared []schema.Message
	var delivered []string
	var stats analysisRequestStats
	var err error
	if purpose != "" {
		prepared, err = ctxpkg.CompactAnalysisWithin(withBudgetContext(request, snapshot), defs, e.options.MaxContextBytes)
	} else if r.evidenceEnabled() {
		prepared, delivered, stats, err = e.prepareAnalysisRequest(ctx, r, withBudgetContext(request, snapshot), defs)
	} else {
		prepared, err = e.compactor.CompactWithin(withBudgetContext(request, snapshot), defs)
	}
	if err != nil {
		return nil, fmt.Errorf("%w：%v", errContextBudget, err)
	}
	if purpose != "" {
		if err := r.reserveQualityStage(ctx, purpose); err != nil {
			return nil, err
		}
	} else if reviewRequest {
		if err := r.takeDeliveryReviewModel(ctx); err != nil {
			return nil, err
		}
	} else if closing {
		if err := r.reserveClosing(ctx); err != nil {
			return nil, err
		}
	} else {
		if err := r.reserve(ctx, "model"); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, span := observability.StartSpan(observe.WithModelCall(ctx, observe.NewID()), "LLM.Generate")
	defer span.EndSpan()
	diagnosticIDs := diagnosticIdentity(observe.Identity(callCtx))
	if requestdiag.Mode(callCtx) != requestdiag.Off && diagnosticIDs.WorkspaceID == "" {
		diagnosticIDs.WorkspaceID = observe.WorkspaceID(r.state.WorkDir)
	}
	callCtx, diagnosticCall := requestdiag.BeginCall(callCtx, diagnosticIDs, r.requestOrigins(prepared))
	var material []byte
	if observe.Identity(callCtx).AttemptID != "" {
		material, _ = json.Marshal(struct {
			Messages []schema.Message
			Tools    []schema.ToolDefinition
		}{prepared, defs})
	}
	model := ""
	if named, ok := e.provider.(interface{ ModelName() string }); ok {
		model = named.ModelName()
	}
	started := time.Now()
	promptBytes, _ := ctx.Value(featurePromptBytesKey{}).(int64)
	deliveryPromptBytes, _ := ctx.Value(deliveryReviewPromptBytesKey{}).(int64)
	checkSchemaBytes, viewSchemaBytes := int64(0), int64(0)
	for _, def := range defs {
		raw, _ := json.Marshal(def)
		if def.Name == "check_evidence" {
			checkSchemaBytes += int64(len(raw))
		}
		if def.Name == "read_evidence" && r.sourceViewsEnabled() {
			viewSchemaBytes += int64(len(raw))
		}
	}
	observe.Emit(callCtx, "model_started", observe.Payload{DeliveryReviewPromptBytes: &deliveryPromptBytes, LocalCheckSchemaBytes: &checkSchemaBytes, SourceViewSchemaBytes: &viewSchemaBytes, FeaturePromptBytes: &promptBytes, Model: model, Stage: modelStage(callCtx), ContextBytes: int64(len(material)), ReadContextVersion: int64(r.state.Spec.ReadContextVersion), ReadPageBytes: int64(r.effectiveReadPageBytes()), ToolCount: int64(len(defs)), SourceBytes: stats.source, AnalysisBytes: stats.notes, EvidenceBytes: stats.evidence, ActionBytes: stats.actions, SchemaBytes: stats.schema, FoldedReads: stats.folded, SoftTargetBytes: stats.target, SoftTargetExceeded: stats.over, UnprocessedReadCount: stats.unprocessed, FoldedSourceBytes: stats.foldedSource, OmittedNoteEntries: stats.omittedNotes, NotesMetadataRemovedBytes: stats.notesRemoved, NotesMetadataRetainedBytes: stats.notesRetained, NotesPreviouslyDelivered: stats.notesPreviouslyDelivered, NotesThisRequest: stats.notesThisRequest})
	r.observeSourceViewLifecycle(callCtx, "source_view_folded", stats.viewFoldedIDs)
	msg, err := e.provider.Generate(callCtx, prepared, defs)
	if diagnosticCall != nil {
		diagnosticKind := observationError(err)
		if err == nil && msg == nil {
			diagnosticKind = "model_response_invalid"
		}
		if ctx.Err() != nil {
			diagnosticKind = observationError(ctx.Err())
		}
		diagnosticCall.Finish(requestdiag.Outcome{Success: err == nil && msg != nil && ctx.Err() == nil, ErrorClass: diagnosticKind})
	}
	errorClass := observationError(err)
	if ctx.Err() != nil {
		errorClass = observationError(ctx.Err())
	}
	if msg == nil && err == nil {
		errorClass = "empty_response"
	}
	observe.Emit(callCtx, "model_finished", observe.Payload{Model: model, Stage: modelStage(callCtx), DurationMS: time.Since(started).Milliseconds(), ErrorClass: errorClass})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("模型调用失败: %w", err)
	}
	if msg == nil {
		return nil, errors.New("模型没有返回消息")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if purpose == "" {
		r.markAnalysisDelivered(callCtx, delivered)
		r.markReadExposure(ctx, prepared)
	}
	// 响应身份只留在进程内，不能进入原始历史或 Provider 请求。
	msg.SourceModelCallID = observe.Identity(callCtx).ModelCallID
	msg.NotesCandidateCount = stats.unprocessed
	return msg, nil
}

func (e *AgentEngine) converse(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, registry tools.Registry, reporter Reporter, stepID, stepGoal string, subTurns int) (string, error) {
	failures := make(map[string]int)
	for turn := 0; ; turn++ {
		if r.toolsExhausted() {
			return "", toolLimit()
		}
		if subTurns > 0 {
			if turn >= subTurns {
				return "", fmt.Errorf("%w：子任务达到 %d 轮", errBlocked, subTurns)
			}
		}
		reviewRequest := false
		if subTurns == 0 {
			var turnErr error
			reviewRequest, turnErr = r.takeDeliveryReviewTurn(ctx)
			if turnErr != nil {
				return "", turnErr
			}
			if !reviewRequest {
				if err := r.reserve(ctx, "turn"); err != nil {
					return "", err
				}
			}
		}
		prompt := system
		featurePrompt := r.readFeaturePrompt(ctx)
		deliveryPrompt := ""
		if subTurns == 0 {
			deliveryPrompt = r.deliveryReviewPrompt(ctx)
		}
		prompt.Content += featurePrompt + deliveryPrompt
		requestCtx := context.WithValue(ctx, featurePromptBytesKey{}, int64(len(featurePrompt)))
		requestCtx = context.WithValue(requestCtx, deliveryReviewPromptBytesKey{}, int64(len(deliveryPrompt)))
		if reviewRequest {
			requestCtx = context.WithValue(requestCtx, deliveryReviewRequestKey{}, true)
			requestCtx = context.WithValue(requestCtx, modelStageKey{}, "delivery_review")
		}
		if r.qualityEnabled() {
			prompt.Content += "\n" + qualityPrompt
		}
		if r.state.Spec.Report != nil && !r.qualityEnabled() {
			prompt.Content += "\n" + reportPrompt
		}
		if subTurns == 0 {
			if r.analysisEnabled() || r.readTrackingEnabled() {
				prompt.Content += r.analysisAnchor(stepGoal)
			} else {
				anchor, _ := json.Marshal(struct {
					Goal     string   `json:"goal"`
					Step     string   `json:"step,omitempty"`
					Progress []string `json:"progress,omitempty"`
				}{r.state.Spec.Goal, stepGoal, r.state.Progress})
				prompt.Content += "\n\n当前任务（结构化数据）：" + string(anchor)
			}
		}
		history := append([]schema.Message{prompt}, session.GetWorkingMemory(20)...)
		if r.evidenceEnabled() {
			if r.readMemoryEnabled() {
				history = append([]schema.Message{prompt}, r.taskHistory(session)...)
			} else {
				history = append([]schema.Message{prompt}, analysisHistory(session)...)
			}
		}
		if subTurns == 0 && e.options.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}
			thinking, err := e.generate(context.WithValue(requestCtx, modelStageKey{}, "thinking"), r, history, nil)
			if err != nil {
				return "", err
			}
			if thinking.Content != "" {
				history = append(history, schema.Message{Role: schema.RoleAssistant, Content: thinking.Content, ResponseItems: thinking.ResponseItems})
			}
		}
		msg, err := e.generate(requestCtx, r, history, r.visibleTools(ctx, registry))
		if reviewRequest {
			if finishErr := r.finishDeliveryReviewRequest(ctx, err); finishErr != nil {
				return "", errors.Join(err, finishErr)
			}
			r.observeDeliveryReview(ctx, "delivery_review_completed")
		}
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(msg.Content)
		if r.qualityEnabled() {
			if handled, out, qualityErr := e.qualityToolResponse(ctx, r, session, registry, reporter, msg); handled {
				return out, qualityErr
			}
		}
		reportSubmit := false
		if r.state.Spec.Report != nil && !r.qualityEnabled() {
			if len(msg.ToolCalls) == 0 {
				return "", reportError{"report_not_delivered", "报告尚未通过write_report提交"}
			}
			for _, c := range msg.ToolCalls {
				if c.Name == "write_report" {
					reportSubmit = true
				}
			}
			if reportSubmit && len(msg.ToolCalls) != 1 {
				return "", e.rejectReportBatch(ctx, r, session, msg)
			}
		}
		var analysis *AnalysisReport
		if r.analysisEnabled() && (text != "" || len(msg.ToolCalls) == 0) {
			analysis, err = r.acceptAnalysis(ctx, text, len(msg.ToolCalls) == 0)
			if err != nil {
				return "", err
			}
		}
		if len(msg.ToolCalls) == 0 {
			r.observeWorkNotesResponse(ctx, msg, workNotesCarrierBatch{}, "")
			if text == "" {
				return "", fmt.Errorf("%w：模型没有交付内容", errBlocked)
			}
			if subTurns == 0 && r.deliveryReviewBoundary(ctx, registry, nil, stepID) {
				admitted, reviewErr := r.admitDeliveryReview(ctx, *msg)
				if reviewErr != nil {
					return "", reviewErr
				}
				if admitted {
					session.Append(schema.Message{Role: schema.RoleAssistant, Content: text, ResponseItems: msg.ResponseItems}, schema.Message{Role: schema.RoleUser, Content: "交付前处理已准入：请处理当前有来源的待办目录。可直接修订终答；没有结构化处置的事项仍列待核实，不要为登记状态调用无用工具。"})
					continue
				}
			}
			session.Append(schema.Message{Role: schema.RoleAssistant, Content: text, ResponseItems: msg.ResponseItems})
			if analysis != nil && subTurns == 0 {
				return RenderAnalysis(*analysis), nil
			}
			return text, nil
		}
		seen := make(map[string]bool, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				return "", fmt.Errorf("%w：模型返回的工具调用 ID 缺失或重复", errBlocked)
			}
			seen[call.ID] = true
		}
		notesCtx := observe.WithModelCall(ctx, msg.SourceModelCallID)
		beforeNotes := r.workNotesObservationSnapshot(notesCtx, text)
		carriers, err := r.consumeWorkNotesCarriers(notesCtx, msg.ToolCalls)
		if err != nil {
			return "", err
		}
		if !carriers.valid {
			if err := r.acceptWorkNotes(notesCtx, text); err != nil {
				return "", err
			}
			r.observeWorkNotesTextFallback(notesCtx, text, beforeNotes, &carriers)
		}
		r.observeWorkNotesResponse(ctx, msg, carriers, "")
		if subTurns == 0 {
			if err := r.refreshDeliveryReview(notesCtx); err != nil {
				return "", err
			}
			if r.deliveryReviewBoundary(notesCtx, registry, carriers.calls, stepID) {
				candidate := *msg
				candidate.ToolCalls = carriers.calls
				admitted, reviewErr := r.admitDeliveryReview(notesCtx, candidate)
				if reviewErr != nil {
					return "", reviewErr
				}
				if admitted {
					notesCtx = context.WithValue(notesCtx, deliveryReviewDeferredKey{}, true)
				}
			}
		}
		session.Append(schema.Message{Role: schema.RoleAssistant, Content: text, ToolCalls: msg.ToolCalls, ResponseItems: msg.ResponseItems})
		results, batchErr := e.executeBatch(notesCtx, r, registry, reporter, carriers.calls, stepID)
		var observations []schema.Message
		var stopped error
		var boundaryErr error
		var readonlyCandidates []ReadonlyFailureBoundary
		for i, result := range results {
			call := carriers.calls[i]
			content := observation(result)
			if result.IsError && result.ErrorCode != "delivery_review_required" {
				content = e.recovery.AnalyzeAndInject(call.Name, content)
			}
			content = carriers.appendReceipt(i, content)
			r.rememberReadObservation(ctx, result, content)
			evidenceID := ""
			if result.Read != nil {
				evidenceID = result.Read.EvidenceID
			}
			readActionID := ""
			if result.Read != nil {
				readActionID = result.Read.ActionID
			}
			observations = append(observations, schema.Message{Role: schema.RoleUser, ToolCallID: call.ID, ToolStatus: result.Status, Content: content, ReadEvidenceID: evidenceID, ReadActionID: readActionID})
			trackedReadonly := false
			if subTurns == 0 && result.ErrorCode != "delivery_review_required" {
				boundary, trackErr := r.noteReadonlyResult(ctx, stepID, call, result)
				if trackErr != nil {
					boundaryErr = errors.Join(boundaryErr, trackErr)
				} else if boundary != nil {
					trackedReadonly = true
					if boundary.Stopped {
						readonlyCandidates = append(readonlyCandidates, *boundary)
					}
				}
			}
			key := actionKey(stepID, call)
			if !result.IsError || result.ErrorCode == "delivery_review_required" {
				delete(failures, key)
			} else {
				failures[key]++
				if failures[key] >= 3 && stopped == nil && !trackedReadonly {
					stopped = fmt.Errorf("%w：工具 %s 的同一请求已失败三次", errBlocked, call.Name)
				}
			}
		}
		session.Append(observations...)
		if batchErr != nil {
			return "", batchErr
		}
		if reportSubmit {
			if len(results) != 1 || results[0].Status != schema.ToolSucceeded {
				return "", r.reportFailure("报告未发布")
			}
			return "", nil
		}
		if boundaryErr != nil {
			return "", boundaryErr
		}
		for _, candidate := range readonlyCandidates {
			if current := r.unresolvedReadonlyBoundary(candidate.ID); current != nil && stopped == nil {
				stopped = readonlyRepeatStop{Boundary: *current}
			}
		}
		if stopped != nil {
			var readonlyStop readonlyRepeatStop
			if errors.As(stopped, &readonlyStop) {
				if err := r.record("readonly_failure", func(s *taskState) error {
					if r.fatalStorage != nil {
						return r.fatalStorage
					}
					s.ReadonlyStopID = readonlyStop.Boundary.ID
					return nil
				}); err != nil {
					return "", err
				}
			}
			return "", stopped
		}
	}
}

func observation(result schema.ToolResult) string {
	header := fmt.Sprintf("执行状态：%s，效果：%s", result.Status, result.Effect)
	if result.ErrorCode != "" {
		header += "，错误码：" + result.ErrorCode
	}
	if result.Retryable != nil {
		if *result.Retryable {
			header += "，当前前提下可重试"
		} else {
			header += "，当前前提下同请求不应重试，请修正目标或跳过"
		}
	}
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
	if r.localChecksEnabled() {
		for _, call := range calls {
			if call.Name == "check_evidence" {
				ctx = context.WithValue(ctx, localCheckBatchKey{}, true)
				break
			}
		}
	}
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
