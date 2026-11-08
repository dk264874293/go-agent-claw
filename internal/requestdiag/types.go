// Package requestdiag 提供旁路请求诊断；任何采集或写入失败均不参与任务裁决。
package requestdiag

import (
	"encoding/json"
	"time"
)

const (
	Off          = "off"
	MetadataOnly = "metadata"
	OnError      = "on_error"
)

type Config struct {
	Mode, Dir                                                           string
	MaxRequestBytes, MaxMemoryBytes, MaxAttemptBytes, MaxDirectoryBytes int64
	CloseTimeout                                                        time.Duration
	QueueSize                                                           int
}

type Identity struct {
	ObservationAttemptID string `json:"observation_attempt_id,omitempty"`
	CaptureAttemptID     string `json:"capture_attempt_id"`
	WorkspaceID          string `json:"workspace_id,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	AgentID              string `json:"agent_id,omitempty"`
	ParentAgentID        string `json:"parent_agent_id,omitempty"`
	ModelCallID          string `json:"model_call_id,omitempty"`
}

type Origin struct {
	InputIndex     int    `json:"input_index"`
	Representation string `json:"representation"`
	SourceKind     string `json:"source_kind,omitempty"`
	EvidenceID     string `json:"evidence_id,omitempty"`
	ActionID       string `json:"action_id,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	Version        string `json:"version,omitempty"`
	ByteStart      int64  `json:"byte_start,omitempty"`
	ByteEnd        int64  `json:"byte_end,omitempty"`
}

type Parameter struct {
	Presence string          `json:"presence"` // absent / null / value / unknown
	Value    json.RawMessage `json:"value,omitempty"`
}

type InputItem struct {
	JSONPath    string  `json:"json_path"`
	Role        string  `json:"role,omitempty"`
	Type        string  `json:"type,omitempty"`
	Bytes       int64   `json:"bytes"`
	Fingerprint string  `json:"fingerprint,omitempty"`
	Origin      *Origin `json:"origin,omitempty"`
}

type ErrorInfo struct {
	Kind            string                     `json:"kind,omitempty"`
	HTTPStatus      *int                       `json:"http_status,omitempty"`
	Code            *string                    `json:"code,omitempty"`
	Type            *string                    `json:"type,omitempty"`
	Param           *string                    `json:"param,omitempty"`
	RequestID       *string                    `json:"request_id,omitempty"`
	RequestIDSource string                     `json:"request_id_source,omitempty"`
	SafeSummary     string                     `json:"safe_summary,omitempty"`
	FieldStates     map[string]string          `json:"field_states,omitempty"`
	FieldValues     map[string]json.RawMessage `json:"field_values,omitempty"`
}

type Metadata struct {
	ProjectionVersion               int                  `json:"projection_version"`
	Build                           BuildInfo            `json:"build"`
	ManifestID                      string               `json:"manifest_id,omitempty"`
	CaptureMode                     string               `json:"capture_mode"`
	Version                         int                  `json:"version"`
	Identity                        Identity             `json:"identity"`
	Provider                        string               `json:"provider,omitempty"`
	Protocol                        string               `json:"protocol,omitempty"`
	EndpointID                      string               `json:"endpoint_id,omitempty"`
	Model                           string               `json:"model,omitempty"`
	Method                          string               `json:"method,omitempty"`
	HTTPAttempt                     int                  `json:"http_attempt"`
	SDKRetryCount                   *int                 `json:"sdk_retry_count,omitempty"`
	RequestBytes                    *int64               `json:"request_bytes,omitempty"`
	Fingerprint                     string               `json:"fingerprint,omitempty"`
	Parameters                      map[string]Parameter `json:"parameters,omitempty"`
	InputCount                      int                  `json:"input_count"`
	RoleCounts                      map[string]int       `json:"role_counts,omitempty"`
	Items                           []InputItem          `json:"items,omitempty"`
	OmittedItems                    int                  `json:"omitted_items,omitempty"`
	ToolNames                       []string             `json:"tool_names,omitempty"`
	ToolCount                       int                  `json:"tool_count"`
	SchemaFingerprint               string               `json:"schema_fingerprint,omitempty"`
	ToolChoiceMode                  string               `json:"tool_choice_mode,omitempty"`
	ToolChoiceName                  string               `json:"tool_choice_name,omitempty"`
	ResponseFormatType              string               `json:"response_format_type,omitempty"`
	ResponseFormatSchemaFingerprint string               `json:"response_format_schema_fingerprint,omitempty"`
	ReasoningEffort                 string               `json:"reasoning_effort,omitempty"`
	BodyCaptureState                string               `json:"body_capture_state"`
	CaptureState                    string               `json:"capture_state"`
	CaptureReason                   string               `json:"capture_reason,omitempty"`
	HTTPStatus                      *int                 `json:"http_status,omitempty"`
	RequestID                       *string              `json:"request_id,omitempty"`
	RequestIDSource                 string               `json:"request_id_source,omitempty"`
	DurationMS                      int64                `json:"duration_ms"`
	Error                           ErrorInfo            `json:"error"`
	PreviousSuccessfulCallID        string               `json:"previous_successful_call_id,omitempty"`
	AddedPaths                      []string             `json:"added_paths,omitempty"`
	RemovedPaths                    []string             `json:"removed_paths,omitempty"`
	ChangedPaths                    []string             `json:"changed_paths,omitempty"`
	ChangedParameters               []string             `json:"changed_parameters,omitempty"`
	SchemaChanged                   bool                 `json:"schema_changed"`
	DiffComplete                    bool                 `json:"diff_complete"`
	StartedAt                       time.Time            `json:"started_at"`
}

type BuildInfo struct {
	GoVersion   string            `json:"go_version,omitempty"`
	VCSRevision string            `json:"vcs_revision,omitempty"`
	VCSModified *bool             `json:"vcs_modified,omitempty"`
	SDKVersions map[string]string `json:"sdk_versions,omitempty"`
}

type Diagnostic struct {
	ID                string   `json:"id,omitempty"`
	Mode              string   `json:"mode"`
	State             string   `json:"state"`
	Reason            string   `json:"reason,omitempty"`
	OriginalBytes     *int64   `json:"original_bytes,omitempty"`
	SavedBytes        int64    `json:"saved_bytes"`
	Redacted          bool     `json:"redacted"`
	Complete          bool     `json:"complete"`
	PreviousAvailable bool     `json:"previous_available"`
	RedactionVersion  int      `json:"redaction_version"`
	OmittedFields     []string `json:"omitted_fields,omitempty"`
}

type Event struct {
	Kind       string
	Identity   Identity
	Metadata   *Metadata
	Diagnostic *Diagnostic
}
type Sink func(Event)
type Outcome struct {
	Success    bool
	ErrorClass string
}
