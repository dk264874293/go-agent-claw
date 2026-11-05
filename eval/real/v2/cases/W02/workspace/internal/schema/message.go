/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-08 11:21:34
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-14 13:58:24
 * @FilePath: /go-agent-claw/internal/schema/message.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
package schema

import "encoding/json"

// Role 定义消息的角色，这是与大模型沟通的基石
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词：确立 Agent 的性格与红线
	RoleUser      Role = "user"      // 用户输入 / 工具执行的返回结果 (Observation)
	RoleAssistant Role = "assistant" // 模型的输出：包含推理(Reasoning)或工具调用(ToolCall)
)

// Usage 记录了单次大模型 API 调用的 Token 消耗
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 输入的 Token 数量
	CompletionTokens int `json:"completion_tokens"` // 产生的 Token 数量
}

// Message 代表上下文中传递的单条消息
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"` // 存放纯文本内容
	// 如果模型决定调用工具，此字段将被填充 (支持并行调用多个工具)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ResponseItems 保存 Responses API 的原始输出项，供后续工具轮按原顺序回放。
	ResponseItems []json.RawMessage `json:"response_items,omitempty"`
	// 如果这是对某个工具调用的响应，此字段必须填写，以告知模型上下文的关联性
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolStatus ToolStatus `json:"tool_status,omitempty"`
	//是大模型 (Assistant) 的回复，此字段存放本次调用的 Token 消耗
	Usage *Usage `json:"usage,omitempty"`
}

// ToolCall 代表模型请求调用某个具体的工具
type ToolCall struct {
	ID   string `json:"id"`   // 工具调用的唯一 ID
	Name string `json:"name"` // 想要调用的工具名称 (例如 "bash")
	// Arguments 存放 JSON 参数。使用 RawMessage 是为了延迟解析，将解析责任交给具体的工具
	Arguments json.RawMessage `json:"arguments"`
}

type ToolStatus string

const (
	ToolSucceeded ToolStatus = "SUCCEEDED"
	ToolRejected  ToolStatus = "REJECTED"
	ToolFailed    ToolStatus = "FAILED"
	ToolCancelled ToolStatus = "CANCELLED"
)

type EffectStatus string

const (
	EffectNone      EffectStatus = "NONE"
	EffectConfirmed EffectStatus = "CONFIRMED"
	EffectUnknown   EffectStatus = "UNKNOWN"
)

// ToolResult 分别记录工具执行状态和效果是否已知。
type ToolResult struct {
	ToolCallID string       `json:"tool_call_id"`
	Output     string       `json:"output"`
	Status     ToolStatus   `json:"status"`
	Effect     EffectStatus `json:"effect"`
	ErrorKind  string       `json:"error_kind,omitempty"`
	ExitCode   *int         `json:"exit_code,omitempty"`
	IsError    bool         `json:"is_error"`
}

func NewToolResult(id, output string, status ToolStatus, effect EffectStatus, errorKind string, exitCode *int) ToolResult {
	return ToolResult{ToolCallID: id, Output: output, Status: status, Effect: effect,
		ErrorKind: errorKind, ExitCode: exitCode, IsError: status != ToolSucceeded}
}

// ToolDefinition 描述了一个大模型可以调用的工具元信息 (供模型理解工具有什么用)
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"` // 对应 JSON Schema
}
