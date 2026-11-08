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
	// ReadEvidenceID 是引擎绑定的观察身份，不发送给 Provider；不能按可能复用的 ToolCallID 猜测来源。
	ReadEvidenceID string `json:"-"`
	// ReadActionID 在普通读取与分析读取间统一定位观察，避免跨轮复用 ToolCallID 串联来源。
	ReadActionID string `json:"-"`
	// RequestRepresentation 只用于诊断来源映射，不编码进请求或原始历史。
	RequestRepresentation string `json:"-"`
	// SourceModelCallID 与 NotesCandidateCount 只供引擎关联本次响应的观测，不发送给 Provider 或持久历史。
	SourceModelCallID   string `json:"-"`
	NotesCandidateCount int64  `json:"-"`
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
	ToolCallID    string            `json:"tool_call_id"`
	Output        string            `json:"output"`
	Status        ToolStatus        `json:"status"`
	Effect        EffectStatus      `json:"effect"`
	ErrorKind     string            `json:"error_kind,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Retryable     *bool             `json:"retryable,omitempty"`
	ExitCode      *int              `json:"exit_code,omitempty"`
	IsError       bool              `json:"is_error"`
	Read          *ReadReceipt      `json:"read,omitempty"`
	Discovery     *DiscoveryReceipt `json:"discovery,omitempty"`
	ContentReview *ContentReview    `json:"content_review,omitempty"`
	// ReadContent 只在执行进程内传递实际交付的源字节，由引擎按需保存为不可变证据。
	ReadContent []byte `json:"-"`
}

// ReadReceipt 记录一次成功读取实际交付的源码范围，不证明文件当前仍是该版本。
type ReadReceipt struct {
	Path       string      `json:"path"`
	Version    string      `json:"version"`
	ByteStart  int64       `json:"byte_start"`
	ByteEnd    int64       `json:"byte_end"`
	FirstLine  int         `json:"first_line"`
	LastLine   int         `json:"last_line"`
	TotalBytes int64       `json:"total_bytes"`
	AgentID    string      `json:"agent_id,omitempty"`
	ActionID   string      `json:"action_id,omitempty"`
	EvidenceID string      `json:"evidence_id,omitempty"`
	FileRef    string      `json:"file_ref,omitempty"`
	ArtifactID string      `json:"artifact_id,omitempty"`
	Review     *ReadReview `json:"review,omitempty"`
	// LineBoundary来自读取实现；历史记录缺失时不能推测半行是完整原文。
	LineBoundary *ReadBoundary `json:"line_boundary,omitempty"`
	// SourceView 指向原始读取材料的不可变切片，不继承原证据的处理或交付资格。
	SourceView *SourceView `json:"source_view,omitempty"`
}

// SourceView 的材料偏移量与源文件字节范围分别记录，不从输出文字推导。
type SourceView struct {
	OriginEvidenceID string `json:"origin_evidence_id"`
	OriginActionID   string `json:"origin_action_id"`
	ArtifactID       string `json:"artifact_id"`
	ArtifactStart    int64  `json:"artifact_start"`
	ArtifactEnd      int64  `json:"artifact_end"`
}

type ReadBoundary struct {
	StartsLine bool `json:"starts_line"`
	EndsLine   bool `json:"ends_line"`
}

// ReadReview 只描述复核目标；引擎校验目标是否属于当前任务及是否仍可用。
type ReadReview struct {
	TargetID string `json:"target_id"`
	Question string `json:"question"`
}

// DiscoveryReceipt 仅记录本次有界发现清单及当时的文件大小，不证明版本或读取完成。
type DiscoveryReceipt struct {
	ManifestID string           `json:"manifest_id"`
	Path       string           `json:"path"`
	NameGlob   string           `json:"name_glob"`
	Files      []DiscoveredFile `json:"files"`
	Truncated  bool             `json:"truncated"`
	Unreadable int              `json:"unreadable"`
}

type DiscoveredFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
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
