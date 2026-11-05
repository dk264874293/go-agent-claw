/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-08 11:32:10
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-14 16:26:48
 * @FilePath: /go-agent-claw/internal/tools/registry.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// MiddlewareFunc 在准入之后、实际执行前检查附加条件。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

type executionHookKey struct{}

// WithExecutionHook 在准入和审批通过后、工具开始执行前写入持久记录。
func WithExecutionHook(ctx context.Context, hook func(context.Context) error) context.Context {
	return context.WithValue(ctx, executionHookKey{}, hook)
}

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc)
	GetAvailableTools() []schema.ToolDefinition
	VisibleTools(ctx context.Context) []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	mu          sync.RWMutex
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc
}

func NewRegistry() Registry {
	return &registryImpl{tools: make(map[string]BaseTool)}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	r.mu.Lock()
	r.tools[name] = tool
	r.mu.Unlock()
	log.Printf("[工具注册表] 已挂载 %s", name)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	return r.VisibleTools(context.Background())
}

func (r *registryImpl) VisibleTools(ctx context.Context) []schema.ToolDefinition {
	scope := ScopeFromContext(ctx)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.tools {
		if scope.allowsTool(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	defs := make([]schema.ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, r.tools[name].Definition())
	}
	return defs
}

type executionError struct {
	status   schema.ToolStatus
	effect   schema.EffectStatus
	kind     string
	exitCode *int
	err      error
}

func (e *executionError) Error() string { return e.err.Error() }
func (e *executionError) Unwrap() error { return e.err }

func reject(kind string, err error) error {
	return &executionError{status: schema.ToolRejected, effect: schema.EffectNone, kind: kind, err: err}
}

func decodeArgs(raw json.RawMessage, target any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return reject("参数错误", fmt.Errorf("工具参数必须是 JSON 对象"))
	}
	for _, key := range required {
		if fields[key] == nil {
			return reject("参数错误", fmt.Errorf("缺少必填参数 %s", key))
		}
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return reject("参数错误", fmt.Errorf("参数 %s 不能为 null", key))
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return reject("参数错误", fmt.Errorf("工具参数无效: %w", err))
	}
	return nil
}

type toolPreflight interface {
	validate(context.Context, json.RawMessage) error
}

type toolExecution interface {
	execute(context.Context, json.RawMessage) (output string, exitCode *int, err error)
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) (result schema.ToolResult) {
	if observe.Identity(ctx).ActionID == "" {
		ctx = observe.WithAction(ctx, observe.NewID())
	}
	startedAt := time.Now()
	attempted := observationForTool(call)
	observe.Emit(ctx, "tool_attempted", attempted)
	stage, handlerStarted := "context", false
	defer func() {
		payload := observationForTool(call)
		payload.Stage, payload.Status, payload.Effect, payload.ErrorClass = stage, string(result.Status), string(result.Effect), result.ErrorKind
		payload.ResultDigest = observe.Digest(result.Output)
		payload.DurationMS = time.Since(startedAt).Milliseconds()
		if !handlerStarted {
			payload.Decision = "rejected"
			observe.Emit(ctx, "tool_admission", payload)
		}
		observe.Emit(ctx, "tool_finished", payload)
	}()
	ctx, span := observability.StartSpan(ctx, "Tool.Execute")
	span.AddAttribute("tool_name", call.Name)
	span.AddAttribute("tool_call_id", call.ID)
	defer func() {
		span.AddAttribute("status", result.Status)
		span.AddAttribute("effect", result.Effect)
		if result.ErrorKind != "" {
			span.AddAttribute("error_kind", result.ErrorKind)
		}
		if result.ExitCode != nil {
			span.AddAttribute("exit_code", *result.ExitCode)
		}
		span.EndSpan()
	}()

	if err := ctx.Err(); err != nil {
		return cancelledBeforeExecution(call.ID, err)
	}
	stage = "registration"
	r.mu.RLock()
	tool, ok := r.tools[call.Name]
	middlewares := append([]MiddlewareFunc(nil), r.middlewares...)
	r.mu.RUnlock()
	if !ok {
		return schema.NewToolResult(call.ID, fmt.Sprintf("未注册工具 %s", call.Name), schema.ToolRejected, schema.EffectNone, "未知工具", nil)
	}
	stage = "permission"
	if err := requireTool(ctx, call.Name); err != nil {
		return schema.NewToolResult(call.ID, err.Error(), schema.ToolRejected, schema.EffectNone, "权限不足", nil)
	}
	stage = "parameters"
	if preflight, ok := tool.(toolPreflight); ok {
		if err := preflight.validate(ctx, call.Arguments); err != nil {
			return toolErrorResult(call, "", nil, err)
		}
	}
	stage = "middleware"
	for _, mw := range middlewares {
		allowed, reason := mw(ctx, call)
		if err := ctx.Err(); err != nil {
			return cancelledBeforeExecution(call.ID, err)
		}
		if !allowed {
			return schema.NewToolResult(call.ID, "执行被拦截: "+reason, schema.ToolRejected, schema.EffectNone, "准入拒绝", nil)
		}
	}
	stage = "persist_hook"
	if hook, _ := ctx.Value(executionHookKey{}).(func(context.Context) error); hook != nil {
		if err := hook(ctx); err != nil {
			return schema.NewToolResult(call.ID, err.Error(), schema.ToolRejected, schema.EffectNone, "执行前记录失败", nil)
		}
		// 分发钩子仅适用于本次调用。
		ctx = context.WithValue(ctx, executionHookKey{}, (func(context.Context) error)(nil))
	}
	if err := ctx.Err(); err != nil {
		return cancelledBeforeExecution(call.ID, err)
	}
	handlerStarted, stage = true, "handler"
	admitted := observationForTool(call)
	admitted.Decision = "allowed"
	observe.Emit(ctx, "tool_admission", admitted)
	observe.Emit(ctx, "tool_handler_started", observationForTool(call))
	var output string
	var exitCode *int
	var err error
	if executor, ok := tool.(toolExecution); ok {
		output, exitCode, err = executor.execute(ctx, call.Arguments)
	} else {
		output, err = tool.Execute(ctx, call.Arguments)
	}
	if err != nil {
		return toolErrorResult(call, output, exitCode, err)
	}
	effect := schema.EffectUnknown
	switch call.Name {
	case "edit_file", "write_file":
		effect = schema.EffectConfirmed
	case "spawn_subagent":
		effect = schema.EffectNone
	default:
		if IsReadOnly(call.Name) {
			effect = schema.EffectNone
		}
	}
	return schema.NewToolResult(call.ID, output, schema.ToolSucceeded, effect, "", exitCode)
}

func cancelledBeforeExecution(id string, err error) schema.ToolResult {
	message, kind := "工具执行已取消", "取消"
	if errors.Is(err, context.DeadlineExceeded) {
		message, kind = "工具在超时前未能开始", "超时"
	}
	return schema.NewToolResult(id, message, schema.ToolCancelled, schema.EffectNone, kind, nil)
}

func toolErrorResult(call schema.ToolCall, output string, exitCode *int, err error) schema.ToolResult {
	status, effect, kind := schema.ToolFailed, schema.EffectUnknown, "执行失败"
	if IsReadOnly(call.Name) || call.Name == "spawn_subagent" {
		effect = schema.EffectNone
	}
	var detail *executionError
	if errors.As(err, &detail) {
		status, effect, kind = detail.status, detail.effect, detail.kind
		if detail.exitCode != nil {
			exitCode = detail.exitCode
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status, kind = schema.ToolCancelled, "取消"
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "超时"
		}
	}
	message := fmt.Sprintf("%s: %v", call.Name, err)
	if strings.TrimSpace(output) != "" {
		message += "\n" + output
	}
	return schema.NewToolResult(call.ID, message, status, effect, kind, exitCode)
}
