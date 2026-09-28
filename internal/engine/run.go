package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// Run 执行一次有轮数与总时限的任务，并返回可判别的终止原因。
func (e *AgentEngine) runQueued(ctx context.Context, session *ctxpkg.Session, reporter Reporter, opts runpkg.Options, prompt *string, ticket *ctxpkg.RunTicket) (result runpkg.Result, runErr error) {
	defer ticket.Release()
	if ctx == nil || session == nil || opts.MaxTurns <= 0 || opts.Timeout <= 0 || opts.ToolTimeout < 0 || opts.MaxConcurrentTools < 0 || opts.MaxTotalTokens < 0 || opts.MaxCostMicros < 0 || opts.MaxOutputTokens < 0 {
		return result, runpkg.ErrInvalidOptions
	}
	if opts.MaxTotalTokens > 0 || opts.MaxCostMicros > 0 {
		metered, ok := e.provider.(interface{ EnforcesBudget() bool })
		if !ok || !metered.EnforcesBudget() {
			return result, fmt.Errorf("%w: 当前 Provider 未装配预算准入包装器", runpkg.ErrInvalidOptions)
		}
	}
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = 4096
	}
	if opts.ConfigVersion == "" {
		opts.ConfigVersion = runpkg.ConfigSchemaVersion
	}
	ledger, err := budget.NewLedger(budget.Limits{MaxTokens: opts.MaxTotalTokens, MaxCostMicros: opts.MaxCostMicros, MaxOutputTokens: opts.MaxOutputTokens, Price: opts.Price, EstimateMethod: provider.InputEstimateMethod, ConfigVersion: opts.ConfigVersion})
	if err != nil {
		return result, fmt.Errorf("%w: %v", runpkg.ErrInvalidOptions, err)
	}
	id, err := runpkg.NewID()
	if err != nil {
		return result, fmt.Errorf("生成运行 ID 失败: %w", err)
	}
	result = runpkg.Result{RunID: id, SessionID: session.ID, StartedAt: time.Now()}
	ledger.SetIdentity(id, opts.Provider, opts.Model)
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	runCtx = tools.WithExecutionOptions(runCtx, tools.ExecutionOptions{Timeout: opts.ToolTimeout, MaxConcurrent: opts.MaxConcurrentTools})
	runCtx = budget.WithLedger(runCtx, ledger)
	runCtx = provider.WithMaxOutputTokens(runCtx, opts.MaxOutputTokens)
	runCtx, rootSpan := observability.StartSpan(runCtx, "Agent.Run")
	rootSpan.AddAttribute("RunID", id)
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("Provider", opts.Provider)
	rootSpan.AddAttribute("Model", opts.Model)
	defer func() {
		result.EndedAt = time.Now()
		result.Budget = ledger.Snapshot()
		rootSpan.AddAttribute("Status", result.Status)
		rootSpan.AddAttribute("Reason", result.Reason)
		rootSpan.AddAttribute("Budget", result.Budget)
		rootSpan.EndSpan()
		result.TracePath, err = observability.ExportTraceToFile(rootSpan, session.WorkDir, id)
		if err != nil {
			result.TraceError = err.Error()
		}
	}()

	stop := func(status runpkg.Status, reason runpkg.Reason, cause error) (runpkg.Result, error) {
		result.Status = status
		result.Reason = reason
		return result, cause
	}
	stopContext := func() (runpkg.Result, error) {
		if ctx.Err() != nil {
			return stop(runpkg.StatusCancelled, runpkg.ReasonCallerCancelled,
				fmt.Errorf("%w: %v", runpkg.ErrCallerCancelled, ctx.Err()))
		}
		return stop(runpkg.StatusBudgetExhausted, runpkg.ReasonTimeLimit, runpkg.ErrTimeLimit)
	}
	stopBudget := func(cause error) (runpkg.Result, error) {
		switch {
		case errors.Is(cause, budget.ErrTokenLimit):
			return stop(runpkg.StatusBudgetExhausted, runpkg.ReasonTokenBudget, budget.ErrTokenLimit)
		case errors.Is(cause, budget.ErrCostLimit):
			return stop(runpkg.StatusBudgetExhausted, runpkg.ReasonCostBudget, budget.ErrCostLimit)
		case errors.Is(cause, budget.ErrUsageUnknown):
			return stop(runpkg.StatusFailed, runpkg.ReasonUsageUnknown, budget.ErrUsageUnknown)
		default:
			return stop(runpkg.StatusFailed, runpkg.ReasonProviderError, fmt.Errorf("%w: %w", runpkg.ErrProvider, cause))
		}
	}

	if err := ticket.Wait(runCtx); err != nil {
		return stopContext()
	}
	if prompt != nil {
		session.Append(schema.Message{Role: schema.RoleUser, Content: *prompt})
	}
	injector := NewReminderInjector()
	systemMsg := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode).Build()
	for {
		if runCtx.Err() != nil {
			return stopContext()
		}
		if err := ledger.StopReason(); err != nil {
			return stopBudget(err)
		}
		if result.Turns >= opts.MaxTurns {
			return stop(runpkg.StatusBudgetExhausted, runpkg.ReasonMaxTurns, runpkg.ErrMaxTurns)
		}
		result.Turns++
		turnCtx, turnSpan := observability.StartSpan(runCtx, fmt.Sprintf("Turn-%d", result.Turns))
		availableTools := e.registry.GetAvailableTools()
		contextHistory := append([]schema.Message{systemMsg}, session.GetWorkingMemory(20)...)
		compactedContext := e.compactor.Compact(contextHistory)
		turnSpan.AddAttribute("context_message_count", len(compactedContext))

		var thinkingContent string
		if e.EnableThinking {
			if runCtx.Err() != nil {
				turnSpan.EndSpan()
				return stopContext()
			}
			if reporter != nil {
				reporter.OnThinking(turnCtx)
			}
			thinkCtx, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
			thinkResp, thinkErr := e.provider.Generate(thinkCtx, compactedContext, nil)
			thinkSpan.EndSpan()
			if runCtx.Err() != nil {
				turnSpan.EndSpan()
				return stopContext()
			}
			if thinkErr != nil {
				turnSpan.EndSpan()
				return stopBudget(thinkErr)
			}
			if err := ledger.StopReason(); err != nil {
				turnSpan.EndSpan()
				return stopBudget(err)
			}
			if thinkResp == nil || thinkResp.Role != schema.RoleAssistant || len(thinkResp.ToolCalls) != 0 {
				turnSpan.EndSpan()
				return stop(runpkg.StatusFailed, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse)
			}
			if thinkResp.Content != "" {
				thinkingContent = thinkResp.Content
				session.Append(*thinkResp)
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		if runCtx.Err() != nil {
			turnSpan.EndSpan()
			return stopContext()
		}
		actCtx, actSpan := observability.StartSpan(turnCtx, "LLM.Action")
		actionResp, actErr := e.provider.Generate(actCtx, compactedContext, availableTools)
		actSpan.EndSpan()
		if runCtx.Err() != nil {
			turnSpan.EndSpan()
			return stopContext()
		}
		if actErr != nil {
			turnSpan.EndSpan()
			return stopBudget(actErr)
		}
		if actionResp == nil || actionResp.Role != schema.RoleAssistant {
			turnSpan.EndSpan()
			return stop(runpkg.StatusFailed, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse)
		}
		callIDs := make(map[string]struct{}, len(actionResp.ToolCalls))
		for _, call := range actionResp.ToolCalls {
			if call.ID == "" || call.Name == "" {
				turnSpan.EndSpan()
				return stop(runpkg.StatusFailed, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse)
			}
			if _, duplicate := callIDs[call.ID]; duplicate {
				turnSpan.EndSpan()
				return stop(runpkg.StatusFailed, runpkg.ReasonInvalidResponse, runpkg.ErrInvalidResponse)
			}
			callIDs[call.ID] = struct{}{}
		}

		session.Append(schema.Message{
			Role:      schema.RoleAssistant,
			Content:   strings.TrimSpace(thinkingContent + "\n" + actionResp.Content),
			ToolCalls: actionResp.ToolCalls,
			Usage:     actionResp.Usage,
		})
		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(turnCtx, actionResp.Content)
		}
		if len(actionResp.ToolCalls) == 0 {
			turnSpan.EndSpan()
			if runCtx.Err() != nil {
				return stopContext()
			}
			return stop(runpkg.StatusSucceeded, runpkg.ReasonCompleted, nil)
		}

		for _, call := range actionResp.ToolCalls {
			if reporter != nil {
				reporter.OnToolCall(turnCtx, call.Name, string(call.Arguments))
			}
		}
		results := tools.ExecuteBatch(turnCtx, e.registry, actionResp.ToolCalls)
		observations := make([]schema.Message, len(results))
		for i := range results {
			if results[i].IsError {
				results[i].Output = e.recovery.AnalyzeAndInject(actionResp.ToolCalls[i].Name, results[i].Output)
			}
			meta := results[i].ToolMetadata
			observations[i] = schema.Message{Role: schema.RoleUser, Content: results[i].Output, ToolCallID: actionResp.ToolCalls[i].ID, ToolMetadata: &meta}
		}
		for i, call := range actionResp.ToolCalls {
			if reporter != nil {
				reporter.OnToolResult(turnCtx, call.Name, toolPreview(results[i].Output), results[i].IsError)
			}
		}
		session.Append(observations...)
		for i, call := range actionResp.ToolCalls {
			if reminder := injector.CheckAndInject(call, results[i]); reminder != nil {
				session.Append(*reminder)
			}
		}
		turnSpan.EndSpan()
		if runCtx.Err() != nil {
			return stopContext()
		}
		if err := ledger.StopReason(); err != nil {
			return stopBudget(err)
		}
	}
}

func toolPreview(output string) string {
	if len(output) > 200 {
		return strings.ToValidUTF8(output[:200], "") + "... (已截断)"
	}
	return output
}
