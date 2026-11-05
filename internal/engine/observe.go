package engine

import (
	"context"
	"errors"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

// ObservationResult 只交付结构化运行元数据，不复制答复、工具输出或错误原文。
func ObservationResult(result RunResult, err error) observe.Payload {
	return observe.Payload{
		Status: string(result.Status), StopCode: result.StopCode, ResultRunID: result.RunID,
		ErrorClass: observationError(err), ModelCalls: int64(result.Usage.ModelCalls),
		ToolCalls: int64(result.Usage.ToolCalls), Turns: int64(result.Usage.Turns),
		ElapsedMS: result.Usage.Elapsed.Milliseconds(),
	}
}

// ObservationOptions 供入口创建观察尝试；恢复后的实际额度由 run_configured 补充。
func (e *AgentEngine) ObservationOptions(workDir, entry string, spec TaskSpec) observe.Options {
	b := e.options.Budget.defaults()
	mode := spec.Mode
	if mode == "" {
		mode = e.options.Mode
	}
	model := ""
	if named, ok := e.provider.(interface{ ModelName() string }); ok {
		model = named.ModelName()
	}
	return observe.Options{Model: model, WorkDir: workDir, Entry: entry, TaskID: spec.ID,
		Goal: spec.Goal, Kind: string(spec.Kind), Mode: string(mode),
		MaxModelCalls: b.MaxModelCalls, MaxToolCalls: b.MaxToolCalls,
		MaxTurns: b.MaxTurns, TimeoutMS: b.Timeout.Milliseconds()}
}

func observationError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, errStorage):
		return "storage_error"
	case errors.Is(err, errBudget):
		return "budget_exhausted"
	default:
		return "execution_error"
	}
}

type modelStageKey struct{}

func modelStage(ctx context.Context) string {
	if stage, ok := ctx.Value(modelStageKey{}).(string); ok {
		return stage
	}
	if observe.Identity(ctx).ParentAgentID != "" {
		return "subagent"
	}
	return "main"
}
