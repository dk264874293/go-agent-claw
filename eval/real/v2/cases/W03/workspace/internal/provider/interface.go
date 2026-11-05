/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-08 11:26:57
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-08 11:27:07
 * @FilePath: /go-agent-claw/internal/provider/interface.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
package provider

import (
    "context"
    "github.com/dk264874293/go-agent-claw/internal/schema"
)

// LLMProvider 定义了与大模型通信的统一契约
type LLMProvider interface {
    // Generate 接收当前的上下文历史、可用工具列表，并发起一次大模型推理
    Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}