package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// Run 执行一次有轮数与总时限的任务，并返回可判别的终止原因。
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter, opts runpkg.Options) (result runpkg.Result, runErr error) {
	if ctx == nil || session == nil || opts.MaxTurns <= 0 || opts.Timeout <= 0 {
		return result, runpkg.ErrInvalidOptions
	}
	id, err := runpkg.NewID()
	if err != nil {
		return result, fmt.Errorf("生成运行 ID 失败: %w", err)
	}
	result = runpkg.Result{RunID: id, SessionID: session.ID, StartedAt: time.Now()}
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	runCtx, rootSpan := observability.StartSpan(runCtx, "Agent.Run")
	rootSpan.AddAttribute("RunID", id)
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("WorkDir", session.WorkDir)
	defer func() {
		result.EndedAt = time.Now()
		rootSpan.AddAttribute("Status", result.Status)
		rootSpan.AddAttribute("Reason", result.Reason)
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID)
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

	systemMsg := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode).Build()
	for {
		if runCtx.Err() != nil {
			return stopContext()
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
				return stop(runpkg.StatusFailed, runpkg.ReasonProviderError,
					fmt.Errorf("%w: Thinking 阶段: %w", runpkg.ErrProvider, thinkErr))
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
			return stop(runpkg.StatusFailed, runpkg.ReasonProviderError,
				fmt.Errorf("%w: Action 阶段: %w", runpkg.ErrProvider, actErr))
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

		results := make([]schema.ToolResult, len(actionResp.ToolCalls))
		observations := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup
		for i, call := range actionResp.ToolCalls {
			if reporter != nil {
				reporter.OnToolCall(turnCtx, call.Name, string(call.Arguments))
			}
			wg.Add(1)
			go func(idx int, toolCall schema.ToolCall) {
				defer wg.Done()
				var toolResult schema.ToolResult
				if err := runCtx.Err(); err != nil {
					toolResult = schema.ToolResult{ToolCallID: toolCall.ID, Output: err.Error(), IsError: true}
				} else {
					toolResult = e.registry.Execute(turnCtx, toolCall)
				}
				if toolResult.IsError {
					toolResult.Output = e.recovery.AnalyzeAndInject(toolCall.Name, toolResult.Output)
				}
				results[idx] = toolResult
				observations[idx] = schema.Message{Role: schema.RoleUser, Content: toolResult.Output, ToolCallID: toolCall.ID}
			}(i, call)
		}
		wg.Wait()
		for i, call := range actionResp.ToolCalls {
			if reporter != nil {
				preview := results[i].Output
				if len(preview) > 200 {
					preview = preview[:200] + "... (已截断)"
				}
				reporter.OnToolResult(turnCtx, call.Name, preview, results[i].IsError)
			}
		}
		session.Append(observations...)
		for i, call := range actionResp.ToolCalls {
			if reminder := e.injector.CheckAndInject(call, results[i]); reminder != nil {
				session.Append(*reminder)
			}
		}
		turnSpan.EndSpan()
		if runCtx.Err() != nil {
			return stopContext()
		}
	}
}
