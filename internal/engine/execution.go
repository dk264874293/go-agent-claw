package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
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
	ctx = observe.WithAction(context.WithValue(ctx, actionIDKey{}, actionID), actionID)
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
			observe.Emit(ctx, "receipt_reused", observe.Payload{Tool: call.Name, ToolCallID: call.ID, Status: string(previous.Status), Effect: string(previous.Effect), RequestDigest: observe.Digest(string(call.Arguments))})
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
	if rejected := r.rejectDuplicateRead(ctx, call); rejected != nil {
		result = *rejected
		observe.Emit(ctx, "tool_attempted", observe.Payload{Tool: call.Name, ToolCallID: call.ID, RequestDigest: observe.Digest(string(call.Arguments))})
		observe.Emit(ctx, "tool_admission", observe.Payload{Tool: call.Name, Decision: "rejected", ErrorClass: result.ErrorCode, Stage: "read_reuse"})
		observe.Emit(ctx, "tool_finished", observe.Payload{Tool: call.Name, Status: string(result.Status), Effect: string(result.Effect), ErrorClass: result.ErrorCode})
	} else {
		result = registry.Execute(ctx, call)
	}
	bindRead(ctx, actionID, &result)
	if err := r.captureAnalysisRead(ctx, actionID, &result); err != nil {
		return result, err
	}
	r.annotateReadOverlap(ctx, &result)
	if storageErr := r.storageError(); storageErr != nil {
		return result, storageErr
	}
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
	if len(result.Output) > 12000 && r.artifact != nil && !((r.analysisEnabled() || r.efficiencyEnabled()) && result.Read != nil) {
		id := newID()
		if err := r.artifact(id, []byte(result.Output)); err != nil {
			// 执行观察仍可交付，但持久化失败后不能宣称证据已保存。
			if started {
				r.mu.Lock()
				setEffect(&r.state, effectReceipt(pending, result))
				r.mu.Unlock()
			}
			return result, r.noteStorageError(fmt.Errorf("%w：保存动作证据失败: %w", errStorage, err))
		}
		observe.Emit(ctx, "artifact_saved", observe.Payload{Tool: call.Name, ArtifactID: id, ContentBytes: int64(len(result.Output))})
		result.Output = truncateUTF8(result.Output, 4000) + "\n完整结果引用：" + id + "（可用 read_evidence 分段读取）"
	}
	if err := r.record("action_result", func(s *taskState) error {
		if started {
			path := ""
			if pending.Before != nil {
				path = pending.Before.Path
			}
			r.invalidateReadsLocked(path, call.Name == "bash" || path == "")
		}
		r.rememberReadLocked(result)
		r.rememberReadReuseLocked(call, result)
		r.rememberDiscoveryLocked(ctx, result)
		r.rememberAnalysisReadLocked(result)
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
		if r.actionRefs == nil {
			r.actionRefs = make(map[string]string)
		}
		r.actionRefs[observe.Identity(ctx).AgentID+"\x00"+call.ID] = actionID
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
				"id": map[string]any{"type": "string", "description": "当前任务保存的材料 ID"},
				"offset": map[string]any{"type": "integer", "minimum": 0, "default": 0,
					"description": "源材料字节起点；继续读取使用上一页返回的下一段 offset"},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 4000, "default": 4000,
					"description": "最大源材料字节数，默认 4000，正文按完整 UTF-8 字符截断"},
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
		return "", fmt.Errorf("证据读取范围无效：offset 必须非负，limit 必须在 1 至 4000 之间（默认 4000）；继续读取请使用上一页返回的下一段 offset")
	}
	r := currentRun(ctx)
	if r == nil || r.readArtifactPage == nil {
		return "", fmt.Errorf("当前没有任务证据可读取")
	}
	if known, allowed := r.handoffArtifactAccess(ctx, args.ID); known && !allowed {
		return "", fmt.Errorf("报告材料超出当前父任务或路径权限")
	}
	page, err := r.readArtifactPage(args.ID, args.Offset, args.Limit)
	if err != nil {
		return "", err
	}
	next := "无"
	if !page.EOF {
		next = fmt.Sprint(page.NextOffset)
	}
	return fmt.Sprintf("证据 %s，范围 [%d,%d)\n总字节数: %d\n下一段 offset: %s\nEOF: %t\n\n%s",
		args.ID, page.Offset, page.NextOffset, page.Total, next, page.EOF, page.Data), nil
}

func NewExplorerRegistry(workDir string) tools.Registry {
	r := tools.NewRegistry()
	r.Register(tools.NewFindFilesTool(workDir))
	r.Register(tools.NewReadFileTool(workDir))
	r.Register(tools.NewReadLinesTool(workDir))
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
	r.Register(readReportTool{})
	return r
}
