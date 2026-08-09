/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-08 11:32:10
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-08 11:32:17
 * @FilePath: /go-agent-claw/internal/tools/registry.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
package tools

import (
    "context"
    "github.com/dk264874293/go-agent-claw/internal/schema"
)

// Registry 定义了工具的注册与分发执行接口
type Registry interface {
    // GetAvailableTools 返回当前系统挂载的所有可用工具的 Schema
    GetAvailableTools() []schema.ToolDefinition

    // Execute 实际执行模型请求的工具，并返回结果
    Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}