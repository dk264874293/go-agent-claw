package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type actionIDKey struct{}

// ActionID 返回本次实际尝试的标识，供审批绑定。
func ActionID(ctx context.Context) string {
	id, _ := ctx.Value(actionIDKey{}).(string)
	return id
}

func (e *AgentEngine) execute(ctx context.Context, r *runState, registry tools.Registry, reporter Reporter, call schema.ToolCall, stepID string, verifier bool) (result schema.ToolResult, err error) {
	if err := r.reserve(ctx, "tool"); err != nil {
		return schema.ToolResult{}, err
	}
	actionID := newID()
	ctx = context.WithValue(ctx, actionIDKey{}, actionID)
	ctx, span := observability.StartSpan(ctx, "Action")
	span.AddAttribute("action_id", actionID)
	span.AddAttribute("tool_call_id", call.ID)
	span.AddAttribute("tool_name", call.Name)
	defer func() {
		span.AddAttribute("status", result.Status)
		span.AddAttribute("effect", result.Effect)
		span.EndSpan()
	}()
	if err := ctx.Err(); err != nil {
		return schema.ToolResult{}, err
	}

	mutation := call.Name == "write_file" || call.Name == "edit_file" || call.Name == "bash"
	key := actionKey(stepID, call)
	if mutation && !verifier {
		r.mu.Lock()
		previous, ok := r.resumeReceipts[key]
		if stepID != "" && !ok {
			previous, ok = r.state.Completed[key]
		}
		r.mu.Unlock()
		if ok {
			previous.ToolCallID = call.ID
			return previous, nil
		}
	}
	pending := pendingAction{ID: actionID, Call: call, StepID: stepID}
	var startErr error
	started := false
	if mutation {
		ctx = tools.WithExecutionHook(ctx, func(dispatchCtx context.Context) error {
			if call.Name != "bash" {
				if preview, err := tools.PreviewWrite(dispatchCtx, call); err == nil {
					pending.Before = &fileVersion{Path: preview.Path, Version: preview.Before}
					pending.Expected = &fileVersion{Path: preview.Path, Version: preview.After}
				}
			}
			startErr = r.record("action_started", func(s *taskState) error {
				if err := dispatchCtx.Err(); err != nil {
					return err
				}
				if s.Pending == nil {
					s.Pending = make(map[string]pendingAction)
				}
				s.Pending[actionID] = pending
				setEffect(s, EffectReceipt{ActionID: actionID, Tool: call.Name, Effect: schema.EffectUnknown})
				s.Verification = nil
				return nil
			})
			started = startErr == nil
			return startErr
		})
	}
	if reporter != nil {
		r.reportMu.Lock()
		reporter.OnToolCall(ctx, call.Name, truncateUTF8(string(call.Arguments), 200))
		r.reportMu.Unlock()
	}
	result = registry.Execute(ctx, call)
	if startErr != nil {
		return result, startErr
	}
	// 一个已退出的用户验收命令可以提供失败证据。超时或中断保留为未知。
	unknown := mutation && result.Effect == schema.EffectUnknown &&
		result.Status != schema.ToolSucceeded && !(verifier && result.ExitCode != nil && result.Status == schema.ToolFailed)
	if unknown && pending.Before != nil {
		if version, err := tools.FileVersion(ctx, pending.Before.Path); err == nil {
			switch version {
			case pending.Expected.Version:
				result.Effect, unknown = schema.EffectConfirmed, false
			case pending.Before.Version:
				result.Effect, unknown = schema.EffectNone, false
			}
		}
	}
	if len(result.Output) > 12000 && r.artifact != nil {
		id := newID()
		if err := r.artifact(id, []byte(result.Output)); err != nil {
			// 执行观察仍可交付，但持久化失败后不能宣称证据已保存。
			if started {
				r.mu.Lock()
				setEffect(&r.state, effectReceipt(pending, result))
				r.mu.Unlock()
			}
			return result, fmt.Errorf("%w：保存动作证据失败: %w", errStorage, err)
		}
		result.Output = truncateUTF8(result.Output, 4000) + "\n完整结果引用：" + id + "（可用 read_evidence 分段读取）"
	}
	if err := r.record("action_result", func(s *taskState) error {
		if started {
			setEffect(s, effectReceipt(pending, result))
		}
		if !unknown {
			delete(s.Pending, actionID)
		}
		if mutation && !verifier && (result.Status == schema.ToolSucceeded || result.Effect == schema.EffectConfirmed) {
			if s.Completed == nil {
				s.Completed = make(map[string]schema.ToolResult)
			}
			s.Completed[key] = result
		}
		s.LastAction = &actionRecord{ID: actionID, Tool: call.Name, StepID: stepID, Digest: key, Result: result}
		r.note(s, fmt.Sprintf("动作 %s · %s · %s\n%s", actionID, call.Name, result.Status, result.Output))
		return nil
	}); err != nil {
		return result, err
	}
	if reporter != nil {
		r.reportMu.Lock()
		reporter.OnToolResult(ctx, call.Name, truncateUTF8(observation(result), 280), result.IsError)
		r.reportMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if unknown {
		return result, fmt.Errorf("%w：动作 %s 的效果未知，停止重试", errBlocked, actionID)
	}
	return result, nil
}

type evidenceTool struct{}

func (evidenceTool) Name() string { return "read_evidence" }
func (evidenceTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "read_evidence", Description: "按字节范围读取当前任务中已保存的完整工具输出。",
		InputSchema: map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{
				"id":     map[string]string{"type": "string"},
				"offset": map[string]string{"type": "integer"},
				"limit":  map[string]string{"type": "integer"},
			},
		},
	}
}
func (evidenceTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID     string `json:"id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("证据读取参数无效: %w", err)
	}
	if args.Limit == 0 {
		args.Limit = 4000
	}
	if args.Offset < 0 || args.Limit < 1 || args.Limit > 4000 {
		return "", fmt.Errorf("证据读取范围无效")
	}
	r := currentRun(ctx)
	if r == nil || r.readArtifact == nil {
		return "", fmt.Errorf("当前没有任务证据可读取")
	}
	bytes, err := r.readArtifact(args.ID, args.Offset, args.Limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("证据 %s，范围 [%d,%d)\n%s", args.ID, args.Offset, args.Offset+len(bytes), bytes), nil
}

func NewExplorerRegistry(workDir string) tools.Registry {
	r := tools.NewRegistry()
	r.Register(tools.NewReadFileTool(workDir))
	r.Register(tools.NewListDirTool(workDir))
	r.Register(tools.NewSearchTextTool(workDir))
	return r
}

func NewWorkspaceRegistry(workDir string) tools.Registry {
	r := NewExplorerRegistry(workDir)
	r.Register(tools.NewWriteFileTool(workDir))
	r.Register(tools.NewEditFileTool(workDir))
	r.Register(tools.NewBashTool(workDir))
	r.Register(evidenceTool{})
	return r
}
