package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func (p *OpenAIProvider) generateResponses(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	input := make(responses.ResponseInputParam, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem))
		case schema.RoleUser:
			if msg.ToolCallID != "" {
				input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, msg.Content))
			} else {
				input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleUser))
			}
		case schema.RoleAssistant:
			if len(msg.ResponseItems) > 0 {
				// 与工具结果一起回放原始推理、文本和工具调用项，保留 API 返回的顺序。
				for _, item := range msg.ResponseItems {
					input = append(input, param.Override[responses.ResponseInputItemUnionParam](item))
				}
				continue
			}
			if msg.Content != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
			}
			for _, call := range msg.ToolCalls {
				input = append(input, responses.ResponseInputItemParamOfFunctionCall(string(call.Arguments), call.ID, call.Name))
			}
		}
	}

	tools := make([]responses.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		parameters, ok := def.InputSchema.(map[string]any)
		if !ok && def.InputSchema != nil {
			data, err := json.Marshal(def.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("编码工具 %s 的参数定义: %w", def.Name, err)
			}
			if err := json.Unmarshal(data, &parameters); err != nil {
				return nil, fmt.Errorf("解析工具 %s 的参数定义: %w", def.Name, err)
			}
		}
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// 项目工具允许部分字段省略，保留其现有参数约定。
		function := &responses.FunctionToolParam{
			Name:       def.Name,
			Parameters: parameters,
			Strict:     openai.Bool(false),
		}
		if def.Description != "" {
			function.Description = openai.String(def.Description)
		}
		tools = append(tools, responses.ToolUnionParam{OfFunction: function})
	}

	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:   shared.ResponsesModel(p.model),
		Input:   responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Tools:   tools,
		Store:   openai.Bool(false),
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI Responses API 请求失败: %w", err)
	}

	msg := &schema.Message{Role: schema.RoleAssistant, Content: resp.OutputText()}
	if resp.JSON.Usage.Valid() && resp.Usage.JSON.InputTokens.Valid() && resp.Usage.JSON.OutputTokens.Valid() {
		msg.Usage = &schema.Usage{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
		}
	}
	if resp.Status != "" && resp.Status != responses.ResponseStatusCompleted {
		return msg, fmt.Errorf("OpenAI Responses 未完成，状态为 %s", resp.Status)
	}
	for _, item := range resp.Output {
		switch item.Type {
		case "function_call":
			call := item.AsFunctionCall()
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:        call.CallID,
				Name:      call.Name,
				Arguments: json.RawMessage(call.Arguments),
			})
		case "reasoning", "message":
		default:
			continue
		}
		msg.ResponseItems = append(msg.ResponseItems, json.RawMessage(item.RawJSON()))
	}
	return msg, nil
}
