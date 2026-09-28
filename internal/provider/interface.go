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

// InputEstimateMethod 标识两种适配器当前使用的请求估算规则。
const InputEstimateMethod = "request_json_utf8_bytes_plus_64_per_item_v1"

type outputLimitKey struct{}

// WithMaxOutputTokens 为一次模型请求设置明确的输出 Token 上限。
func WithMaxOutputTokens(ctx context.Context, tokens int) context.Context {
	return context.WithValue(ctx, outputLimitKey{}, tokens)
}

func MaxOutputTokens(ctx context.Context) int {
	tokens, _ := ctx.Value(outputLimitKey{}).(int)
	if tokens <= 0 {
		return 4096
	}
	return tokens
}

// InputEstimator 由具体适配器估算发出的请求体大小及协议开销。
type InputEstimator interface {
	EstimateInput(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (int64, error)
}

// LLMProvider 定义了与大模型通信的统一契约
type LLMProvider interface {
	// Generate 接收当前的上下文历史、可用工具列表，并发起一次大模型推理
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}
