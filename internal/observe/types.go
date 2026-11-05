// Package observe 提供不参与执行裁决的轻量观测记录。
// 本包只依赖标准库，观测失败不会改变任务结果。
package observe

import "time"

const SchemaVersion = 1

type Event struct {
	SchemaVersion      int       `json:"schema_version"`
	AttemptID          string    `json:"attempt_id"`
	Sequence           uint64    `json:"sequence"`
	RecordedAt         time.Time `json:"recorded_at"`
	EventType          string    `json:"event_type"`
	WorkspaceID        string    `json:"workspace_id"`
	TaskID             string    `json:"task_id,omitempty"`
	AgentID            string    `json:"agent_id,omitempty"`
	ParentAgentID      string    `json:"parent_agent_id,omitempty"`
	ActionID           string    `json:"action_id,omitempty"`
	ModelCallID        string    `json:"model_call_id,omitempty"`
	SourceTaskSequence uint64    `json:"source_task_sequence,omitempty"`
	Payload            Payload   `json:"payload"`
}

// Payload 是允许落盘的结构化字段；不能放入消息、完整参数或工具正文。
type Payload struct {
	Entry            string     `json:"entry,omitempty"`
	Goal             string     `json:"goal,omitempty"`
	Kind             string     `json:"kind,omitempty"`
	Mode             string     `json:"mode,omitempty"`
	Model            string     `json:"model,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	Status           string     `json:"status,omitempty"`
	StopCode         string     `json:"stop_code,omitempty"`
	ResultRunID      string     `json:"result_run_id,omitempty"`
	ErrorClass       string     `json:"error_class,omitempty"`
	Tool             string     `json:"tool,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ParentActionID   string     `json:"parent_action_id,omitempty"`
	RequestDigest    string     `json:"request_digest,omitempty"`
	ResultDigest     string     `json:"result_digest,omitempty"`
	Path             string     `json:"path,omitempty"`
	BeforeVersion    string     `json:"before_version,omitempty"`
	AfterVersion     string     `json:"after_version,omitempty"`
	Effect           string     `json:"effect,omitempty"`
	Summary          string     `json:"summary,omitempty"`
	ArtifactID       string     `json:"artifact_id,omitempty"`
	Decision         string     `json:"decision,omitempty"`
	SourceKind       string     `json:"source_kind,omitempty"`
	DurationMS       int64      `json:"duration_ms,omitempty"`
	ContextBytes     int64      `json:"context_bytes,omitempty"`
	ToolCount        int64      `json:"tool_count,omitempty"`
	ContentBytes     int64      `json:"content_bytes,omitempty"`
	Offset           int64      `json:"offset,omitempty"`
	Limit            int64      `json:"limit,omitempty"`
	ModelCalls       int64      `json:"model_calls,omitempty"`
	ToolCalls        int64      `json:"tool_calls,omitempty"`
	Turns            int64      `json:"turns,omitempty"`
	MaxModelCalls    int64      `json:"max_model_calls,omitempty"`
	MaxToolCalls     int64      `json:"max_tool_calls,omitempty"`
	MaxTurns         int64      `json:"max_turns,omitempty"`
	TimeoutMS        int64      `json:"timeout_ms,omitempty"`
	ElapsedMS        int64      `json:"elapsed_ms,omitempty"`
	InputTokens      *int64     `json:"input_tokens"`
	OutputTokens     *int64     `json:"output_tokens"`
	Cost             *float64   `json:"cost"`
	Currency         string     `json:"currency,omitempty"`
	InputPerMillion  *float64   `json:"input_per_million,omitempty"`
	OutputPerMillion *float64   `json:"output_per_million,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Dropped          uint64     `json:"dropped,omitempty"`
	Incomplete       bool       `json:"incomplete,omitempty"`
}

type Options struct {
	WorkDir, Entry, TaskID, Goal, Kind, Mode, Model string
	MaxModelCalls, MaxToolCalls, MaxTurns           int
	TimeoutMS                                       int64
}

// IdentityInfo 中的 ID 均由运行程序绑定，不从模型文字提取。
type IdentityInfo struct {
	AttemptID, WorkspaceID, TaskID, AgentID, ParentAgentID, ActionID, ModelCallID string
	SourceTaskSequence                                                            uint64
}
