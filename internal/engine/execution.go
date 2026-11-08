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

	mutation := call.Name == "write_report" || call.Name == "write_file" || call.Name == "edit_file" || call.Name == "bash"
	localRejected := r.rejectLocalCheckCall(ctx, call)
	key := actionKey(stepID, call)
	if mutation && !verifier && localRejected == nil {
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
	deferred, _ := ctx.Value(deliveryReviewDeferredKey{}).(bool)
	if deferred {
		ctx = tools.WithExecutionHook(ctx, func(context.Context) error {
			return tools.NewRejectedError("delivery_review_required", "delivery_review_required", fmt.Errorf("本批未执行，请先处理交付清单；原写入不会自动重放。可使用已有材料、当前可见核对工具或明确待核实，再正常提交"))
		})
	} else if mutation {
		ctx = tools.WithExecutionHook(ctx, func(dispatchCtx context.Context) error {
			if call.Name != "bash" {
				var preview tools.FilePreview
				var previewErr error
				if call.Name == "write_report" {
					preview, previewErr = r.reportWritePreview(dispatchCtx)
				} else {
					preview, previewErr = tools.PreviewWrite(dispatchCtx, call)
				}
				if call.Name == "write_report" && previewErr != nil {
					startErr = previewErr
					return previewErr
				}
				if previewErr == nil {
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
				if call.Name == "bash" {
					r.invalidateReadsLocked("", true)
					r.invalidateMemoryLocked("", "bash_started", true)
				} else if pending.Before != nil {
					r.invalidateMemoryLocked(pending.Before.Path, "file_action_started", false)
				}
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
	if localRejected != nil {
		result = *localRejected
		observe.Emit(ctx, "tool_attempted", observe.Payload{Tool: call.Name, ToolCallID: call.ID, RequestDigest: observe.Digest(string(call.Arguments))})
		observe.Emit(ctx, "tool_admission", observe.Payload{Tool: call.Name, Decision: "rejected", ErrorClass: result.ErrorCode, Stage: "local_check_batch"})
		observe.Emit(ctx, "tool_finished", observe.Payload{Tool: call.Name, Status: string(result.Status), Effect: string(result.Effect), ErrorClass: result.ErrorCode})
	} else if r.qualityEnabled() && r.state.Quality != nil && r.state.Quality.DraftID != "" && call.Name != "write_report" && call.Name != "verify_findings" {
		result = schema.NewToolResult(call.ID, "质量草稿已登记，探索工具已关闭", schema.ToolRejected, schema.EffectNone, "质量准入拒绝", nil)
		result.ErrorCode = "quality_contract_invalid"
	} else if rejected := r.rejectDuplicateRead(ctx, call); rejected != nil {
		result = *rejected
		observe.Emit(ctx, "tool_attempted", observe.Payload{Tool: call.Name, ToolCallID: call.ID, RequestDigest: observe.Digest(string(call.Arguments))})
		observe.Emit(ctx, "tool_admission", observe.Payload{Tool: call.Name, Decision: "rejected", ErrorClass: result.ErrorCode, Stage: "read_reuse"})
		observe.Emit(ctx, "tool_finished", observe.Payload{Tool: call.Name, Status: string(result.Status), Effect: string(result.Effect), ErrorClass: result.ErrorCode})
	} else {
		result = registry.Execute(ctx, call)
	}
	if call.Name == "write_report" && !started && result.Status != schema.ToolSucceeded {
		result.Status, result.Effect, result.IsError = schema.ToolRejected, schema.EffectNone, true
		if result.ErrorCode == "" {
			result.ErrorCode = "report_invalid"
		}
		r.mu.Lock()
		if r.state.Quality != nil && r.state.Quality.ErrorCode != "" {
			result.ErrorCode = r.state.Quality.ErrorCode
		}
		if r.state.Report != nil && r.state.Report.ErrorCode != "" {
			result.ErrorCode = r.state.Report.ErrorCode
		}
		r.mu.Unlock()
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
	if len(result.Output) > 12000 && r.artifact != nil && !(result.Read != nil) {
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
	// 仅对本次确认写入附加非阻断提示，不改变业务身份、权限、预算或文件效果。
	r.attachDeliveryContentReview(ctx, call, &result)
	if storageErr := r.storageError(); storageErr != nil {
		if started {
			r.mu.Lock()
			setEffect(&r.state, effectReceipt(pending, result))
			r.mu.Unlock()
		}
		return result, storageErr
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
			recordDeliveryReviewContent(s, r.runID, pending, result)
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
	if call.Name == "write_report" && started {
		if err := r.completeReportWrite(ctx, actionID, result); err != nil {
			return result, err
		}
	}
	if call.Name == "write_report" && !started && result.Status != schema.ToolSucceeded {
		if r.qualityEnabled() {
			if result.ErrorCode != "" {
				return result, qualityError(result.ErrorCode, result.Output)
			}
			return result, r.qualityFailure(result.Output)
		}
		return result, r.reportFailure(result.Output)
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
	output, _, _, err := (evidenceTool{}).ExecuteReadSource(ctx, raw)
	return output, err
}
func (evidenceTool) executeLegacy(ctx context.Context, raw json.RawMessage) (string, error) {
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
	handoffKnown, handoffAllowed := r.handoffArtifactAccess(ctx, args.ID)
	if handoffKnown && !handoffAllowed {
		return "", fmt.Errorf("报告材料超出当前父任务或路径权限")
	}
	sourceKnown, sourceAllowed := r.sourceArtifactAccess(ctx, args.ID)
	checkKnown, checkAllowed, _ := r.localCheckMaterialAccess(ctx, args.ID)
	if checkKnown && !checkAllowed {
		return "", fmt.Errorf("核对材料超出当前Agent或路径权限")
	}
	if sourceKnown && !sourceAllowed {
		return "", fmt.Errorf("原文材料超出当前Agent或路径权限")
	}
	if observe.Identity(ctx).ParentAgentID != "" && !handoffKnown && !sourceKnown && !checkKnown {
		return "", fmt.Errorf("子任务不能读取来源未授权的材料")
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
	r.Register(verifyFindingsTool{})
	r.Register(evidenceTool{})
	r.Register(localCheckTool{})
	return r
}

func NewWorkspaceRegistry(workDir string) tools.Registry {
	r := NewExplorerRegistry(workDir)
	r.Register(tools.NewWriteFileTool(workDir))
	r.Register(tools.NewEditFileTool(workDir))
	r.Register(tools.NewBashTool(workDir))
	r.Register(readReportTool{})
	r.Register(reportTool{})
	return r
}
