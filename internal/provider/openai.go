// internal/provider/openai.go
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

const DefaultOpenAIModel = "gpt-6.1-terra"

type OpenAIProvider struct {
	chat      openai.ChatCompletionService
	responses responses.ResponseService
	model     string
	provider  string
}

// NewOpenAIProvider 构造函数：基于 OpenAI V3 SDK，默认使用 OpenAI 官方 API
func NewOpenAIProvider(model string) *OpenAIProvider {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		panic("请设置 OPENAI_API_KEY 环境变量")
	}
	// 自定义兼容端点时，URL 应包含其 API 版本路径。
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1/"
	}

	return newCompatibleProviderForChannel(model, apiKey, baseURL, "openai")
}

// NewDeepseekOpenAIProvider 构造函数：基于 OpenAI V3 SDK，使用 DeepSeek 兼容端点
func NewDeepseekOpenAIProvider(model string) *OpenAIProvider {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		panic("请设置 DEEPSEEK_API_KEY 环境变量")
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}

	return newCompatibleProviderForChannel(model, apiKey, baseURL, "deepseek")
}

// NewZhipuOpenAIProvider 构造函数：基于 OpenAI V3 SDK，指向智谱底座
func NewZhipuOpenAIProvider(model string) *OpenAIProvider {
	apiKey := os.Getenv("ZHIPU_API_KEY")
	if apiKey == "" {
		panic("请设置 ZHIPU_API_KEY 环境变量")
	}
	baseURL := strings.TrimSpace(os.Getenv("ZHIPU_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://open.bigmodel.cn/api/paas/v4/"
	}
	return newCompatibleProviderForChannel(model, apiKey, baseURL, "zhipu")
}

func (p *OpenAIProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	if p.model == DefaultOpenAIModel || p.model == "gpt-6.1-sol" {
		return p.generateResponses(ctx, msgs, availableTools)
	}
	ctx = withProviderResponse(ctx)

	var openaiMsgs []openai.ChatCompletionMessageParamUnion
	mappings := map[int][]string{}

	// 1. 翻译上下文消息
	for inputIndex, msg := range msgs {
		before := len(openaiMsgs)
		switch msg.Role {
		case schema.RoleSystem:
			openaiMsgs = append(openaiMsgs, openai.SystemMessage(msg.Content))

		case schema.RoleUser:
			if msg.ToolCallID != "" {
				// 注意：v3 新版参数顺序是 (content, toolCallID)
				openaiMsgs = append(openaiMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			} else {
				openaiMsgs = append(openaiMsgs, openai.UserMessage(msg.Content))
			}

		case schema.RoleAssistant:
			astParam := openai.ChatCompletionAssistantMessageParam{}

			if msg.Content != "" {
				astParam.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}

			// 【重要】如果历史包含 ToolCalls，必须原样放回，以维系大模型的逻辑链
			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ChatCompletionMessageToolCallUnionParam
				for _, tc := range msg.ToolCalls {
					// OfFunction 对应 GetFunction()，字段类型严格要求为指针
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(tc.Arguments),
							},
						},
					})
				}
				astParam.ToolCalls = toolCalls
			}

			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &astParam,
			})
		}
		if len(openaiMsgs) > before {
			mappings[inputIndex] = []string{fmt.Sprintf("messages[%d]", before)}
		}
	}
	ctx = requestdiag.WithWireMappings(ctx, mappings)

	// 2. 翻译工具定义 (v3 新 API 特性适配)
	var openaiTools []openai.ChatCompletionToolUnionParam
	for _, toolDef := range availableTools {
		var params shared.FunctionParameters

		// 尝试直接断言，如果不成功则通过 JSON 往返序列化来保证类型匹配
		if m, ok := toolDef.InputSchema.(map[string]interface{}); ok {
			params = shared.FunctionParameters(m)
		} else {
			// fallback：JSON 往返序列化
			b, _ := json.Marshal(toolDef.InputSchema)
			_ = json.Unmarshal(b, &params)
		}

		openaiTools = append(openaiTools, openai.ChatCompletionFunctionTool(
			shared.FunctionDefinitionParam{
				Name:        toolDef.Name,
				Description: openai.String(toolDef.Description),
				Parameters:  params,
			},
		))
	}

	// 3. 构建请求并发送
	params := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: openaiMsgs,
	}

	// 【慢思考机制支撑】仅当 availableTools 存在时才挂载 Tools
	if len(openaiTools) > 0 {
		params.Tools = openaiTools
	}

	resp, err := p.chat.New(ctx, params)
	if err != nil {
		return nil, NormalizeRequestError(ctx, actualProvider(p.provider), "chat_completions", err)
	}
	// 4. 将 API Response 反向翻译为内部 schema.Message
	resultMsg := &schema.Message{
		Role: schema.RoleAssistant,
	}

	// 【新增】提取 Usage 信息
	if resp.JSON.Usage.Valid() && resp.Usage.JSON.PromptTokens.Valid() && resp.Usage.JSON.CompletionTokens.Valid() {
		resultMsg.Usage = &schema.Usage{
			PromptTokens:     int(resp.Usage.PromptTokens),
			CompletionTokens: int(resp.Usage.CompletionTokens),
		}
	}
	if len(resp.Choices) == 0 {
		return resultMsg, responseInvalid(ctx, actualProvider(p.provider), "chat_completions", "API 返回了空的 Choices")
	}
	choice := resp.Choices[0].Message
	resultMsg.Content = choice.Content

	for _, tc := range choice.ToolCalls {
		if tc.Type == "function" {
			resultMsg.ToolCalls = append(resultMsg.ToolCalls, schema.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: []byte(tc.Function.Arguments), // 提取 JSON 字符串字节
			})
		}
	}

	return validatedProviderMessage(ctx, actualProvider(p.provider), "chat_completions", resultMsg)
}
