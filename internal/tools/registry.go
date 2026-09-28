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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// MiddlewareFunc 定义了中间件的签名。
// 它接收当前的 ToolCall，并返回一个是否允许执行的布尔值 (allowed)，以及拦截时的原因 (rejectReason)。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

// BaseTool 是所有具体工具必须实现的通用接口
type BaseTool interface {
	// Name 返回工具的全局唯一名称 (大模型通过这个名字调用它)
	Name() string
	// Definition 返回用于提交给大模型的工具元信息和参数 JSON Schema
	Definition() schema.ToolDefinition
	// Execute 接收大模型吐出的 JSON 参数，执行具体业务逻辑
	// 注意：参数是 json.RawMessage，反序列化由各个具体工具内部自行处理
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry 定义了工具的注册与分发执行接口
type Registry interface {
	// Register 挂载一个新的工具到系统中
	Register(tool BaseTool)
	Use(mw MiddlewareFunc)
	// GetAvailableTools 返回当前系统挂载的所有可用工具的 Schema
	GetAvailableTools() []schema.ToolDefinition

	// Execute 实际执行模型请求的工具，并返回结果
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

// registryImpl 是 Registry 接口的默认实现
type registryImpl struct {
	// 使用 map 以工具的 Name 作为 Key 进行快速 O(1) 路由查找
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs []schema.ToolDefinition
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

// CanRunConcurrently 仅信任已装配工具的显式只读声明。
func (r *registryImpl) CanRunConcurrently(name string) bool {
	tool, ok := r.tools[name].(interface{ ReadOnly() bool })
	return ok && tool.ReadOnly()
}

// detailedTool 在不改变 BaseTool 的前提下提供结构化执行元数据。
type detailedTool interface {
	ExecuteDetailed(context.Context, json.RawMessage) (string, schema.ToolMetadata, error)
}

func cancelledResult(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{ToolCallID: call.ID, Output: fmt.Sprintf("工具调用已中断: %v", ctx.Err()), IsError: true,
		ToolMetadata: schema.ToolMetadata{TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Cancelled: errors.Is(ctx.Err(), context.Canceled)}}
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) (result schema.ToolResult) {
	ctx, span := observability.StartSpan(ctx, "Tool.Execute")
	span.AddAttribute("tool_name", call.Name)
	span.AddAttribute("arguments", string(call.Arguments))
	result.ToolCallID = call.ID
	defer func() {
		if ctx.Err() != nil {
			result.IsError = true
			result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			result.Cancelled = errors.Is(ctx.Err(), context.Canceled)
		}
		if len(result.Output) > maxOutputBytes {
			result.Output = outputText([]byte(result.Output[:maxOutputBytes]), true)
			result.Truncated = true
		}
		span.AddAttribute("output_preview", truncate(result.Output, 200))
		span.AddAttribute("is_error", result.IsError)
		span.EndSpan()
	}()
	if ctx.Err() != nil {
		return cancelledResult(ctx, call)
	}
	tool, exists := r.tools[call.Name]
	if !exists {
		result.IsError = true
		result.Output = fmt.Sprintf("系统中不存在名为 %q 的工具", call.Name)
		return result
	}
	for _, middleware := range r.middlewares {
		if ctx.Err() != nil {
			return cancelledResult(ctx, call)
		}
		allowed, reason := middleware(ctx, call)
		if !allowed {
			result.IsError = true
			result.Output = "执行被系统拦截。原因: " + reason
			return result
		}
	}
	if ctx.Err() != nil {
		return cancelledResult(ctx, call)
	}
	var err error
	if detailed, ok := tool.(detailedTool); ok {
		result.Output, result.ToolMetadata, err = detailed.ExecuteDetailed(ctx, call.Arguments)
	} else {
		result.Output, err = tool.Execute(ctx, call.Arguments)
	}
	if err != nil {
		result.IsError = true
		result.TimedOut = result.TimedOut || errors.Is(err, context.DeadlineExceeded)
		result.Cancelled = result.Cancelled || errors.Is(err, context.Canceled)
		result.Output = fmt.Sprintf("工具 %s 执行失败: %v\n%s", call.Name, err, result.Output)
	}
	return result
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
