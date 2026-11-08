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
	DeliveryReviewPromptBytes  *int64                 `json:"delivery_review_prompt_bytes,omitempty"`
	DeliveryReview             *DeliveryReviewSummary `json:"delivery_review,omitempty"`
	ReadFeatures               *ReadFeatureSummary    `json:"read_features,omitempty"`
	FeaturePromptBytes         *int64                 `json:"feature_prompt_bytes,omitempty"`
	LocalCheckSchemaBytes      *int64                 `json:"local_check_schema_bytes,omitempty"`
	SourceViewSchemaBytes      *int64                 `json:"source_view_schema_bytes,omitempty"`
	ChecksRequested            *int64                 `json:"checks_requested,omitempty"`
	FileRef                    string                 `json:"file_ref,omitempty"`
	Request                    *RequestSummary        `json:"request,omitempty"`
	Entry                      string                 `json:"entry,omitempty"`
	Goal                       string                 `json:"goal,omitempty"`
	Kind                       string                 `json:"kind,omitempty"`
	Mode                       string                 `json:"mode,omitempty"`
	Analysis                   bool                   `json:"analysis,omitempty"`
	Delegation                 string                 `json:"delegation,omitempty"`
	Model                      string                 `json:"model,omitempty"`
	Stage                      string                 `json:"stage,omitempty"`
	Status                     string                 `json:"status,omitempty"`
	StopCode                   string                 `json:"stop_code,omitempty"`
	ResultRunID                string                 `json:"result_run_id,omitempty"`
	ErrorClass                 string                 `json:"error_class,omitempty"`
	Tool                       string                 `json:"tool,omitempty"`
	ToolCallID                 string                 `json:"tool_call_id,omitempty"`
	ParentActionID             string                 `json:"parent_action_id,omitempty"`
	RequestDigest              string                 `json:"request_digest,omitempty"`
	ResultDigest               string                 `json:"result_digest,omitempty"`
	Path                       string                 `json:"path,omitempty"`
	BeforeVersion              string                 `json:"before_version,omitempty"`
	AfterVersion               string                 `json:"after_version,omitempty"`
	Effect                     string                 `json:"effect,omitempty"`
	Summary                    string                 `json:"summary,omitempty"`
	ArtifactID                 string                 `json:"artifact_id,omitempty"`
	OriginEvidenceID           string                 `json:"origin_evidence_id,omitempty"`
	ViewEvidenceID             string                 `json:"view_evidence_id,omitempty"`
	ByteStart                  *int64                 `json:"byte_start,omitempty"`
	ByteEnd                    *int64                 `json:"byte_end,omitempty"`
	CheckID                    string                 `json:"check_id,omitempty"`
	CheckResult                string                 `json:"check_result,omitempty"`
	PremiseResult              string                 `json:"premise_result,omitempty"`
	SubjectDigest              string                 `json:"subject_digest,omitempty"`
	ReportID                   string                 `json:"report_id,omitempty"`
	FindingCount               int64                  `json:"finding_count,omitempty"`
	Decision                   string                 `json:"decision,omitempty"`
	SourceKind                 string                 `json:"source_kind,omitempty"`
	DurationMS                 int64                  `json:"duration_ms,omitempty"`
	ReadPageBytes              int64                  `json:"read_page_bytes,omitempty"`
	ReadContextVersion         int64                  `json:"read_context_version,omitempty"`
	ContextBytes               int64                  `json:"context_bytes,omitempty"`
	EncodedRequestBytes        *int64                 `json:"request_encoded_bytes,omitempty"`
	HTTPAttempt                int64                  `json:"http_attempt,omitempty"`
	SourceBytes                int64                  `json:"source_bytes,omitempty"`
	AnalysisBytes              int64                  `json:"analysis_bytes,omitempty"`
	EvidenceBytes              int64                  `json:"evidence_bytes,omitempty"`
	ActionBytes                int64                  `json:"action_bytes,omitempty"`
	SchemaBytes                int64                  `json:"schema_bytes,omitempty"`
	FoldedReads                int64                  `json:"folded_reads,omitempty"`
	UnprocessedReadCount       int64                  `json:"unprocessed_read_count,omitempty"`
	FoldedSourceBytes          int64                  `json:"folded_source_bytes,omitempty"`
	OmittedNoteEntries         int64                  `json:"omitted_note_entries,omitempty"`
	NotesAccepted              int64                  `json:"notes_accepted,omitempty"`
	NotesRejected              int64                  `json:"notes_rejected,omitempty"`
	NotesMissing               int64                  `json:"notes_missing,omitempty"`
	NotesCarrierBytes          int64                  `json:"notes_carrier_bytes,omitempty"`
	NotesMetadataRemovedBytes  int64                  `json:"notes_metadata_removed_bytes,omitempty"`
	NotesMetadataRetainedBytes int64                  `json:"notes_metadata_retained_bytes,omitempty"`
	NotesPreviouslyDelivered   int64                  `json:"notes_previously_delivered,omitempty"`
	NotesThisRequest           int64                  `json:"notes_this_request,omitempty"`
	NotesCandidates            int64                  `json:"notes_candidates,omitempty"`
	NotesOpportunity           bool                   `json:"notes_opportunity,omitempty"`
	NotesResponseAccepted      bool                   `json:"notes_response_accepted,omitempty"`
	NotesProcessedSources      int64                  `json:"notes_processed_sources,omitempty"`
	NotesDeferredSources       int64                  `json:"notes_deferred_sources,omitempty"`
	NotesSubmitted             int64                  `json:"notes_submitted,omitempty"`
	NotesResponseRejected      int64                  `json:"notes_response_rejected,omitempty"`
	NotesDuplicates            int64                  `json:"notes_duplicates,omitempty"`
	SoftTargetBytes            int64                  `json:"soft_target_bytes,omitempty"`
	SoftTargetExceeded         bool                   `json:"soft_target_exceeded,omitempty"`
	ToolCount                  int64                  `json:"tool_count,omitempty"`
	ContentBytes               int64                  `json:"content_bytes,omitempty"`
	Offset                     int64                  `json:"offset,omitempty"`
	Limit                      int64                  `json:"limit,omitempty"`
	StartLine                  int64                  `json:"start_line,omitempty"`
	EndLine                    int64                  `json:"end_line,omitempty"`
	ModelCalls                 int64                  `json:"model_calls,omitempty"`
	ToolCalls                  int64                  `json:"tool_calls,omitempty"`
	Turns                      int64                  `json:"turns,omitempty"`
	MaxModelCalls              int64                  `json:"max_model_calls,omitempty"`
	MaxToolCalls               int64                  `json:"max_tool_calls,omitempty"`
	MaxTurns                   int64                  `json:"max_turns,omitempty"`
	TimeoutMS                  int64                  `json:"timeout_ms,omitempty"`
	ElapsedMS                  int64                  `json:"elapsed_ms,omitempty"`
	InputTokens                *int64                 `json:"input_tokens"`
	OutputTokens               *int64                 `json:"output_tokens"`
	Cost                       *float64               `json:"cost"`
	Currency                   string                 `json:"currency,omitempty"`
	InputPerMillion            *float64               `json:"input_per_million,omitempty"`
	OutputPerMillion           *float64               `json:"output_per_million,omitempty"`
	ExpiresAt                  *time.Time             `json:"expires_at,omitempty"`
	Dropped                    uint64                 `json:"dropped,omitempty"`
	Incomplete                 bool                   `json:"incomplete,omitempty"`
}

// DeliveryReviewSummary 只含阶段和计数，不接收文档处置理由或笔记正文。
type DeliveryReviewSummary struct {
	ContentChanged     int    `json:"content_changed,omitempty"`
	ContentUnchanged   int    `json:"content_unchanged,omitempty"`
	ContentUnconfirmed int    `json:"content_unconfirmed,omitempty"`
	Version            int    `json:"version"`
	Enabled            bool   `json:"enabled"`
	Status             string `json:"status"`
	AdmissionCount     int    `json:"admission_count"`
	Documents          int    `json:"documents"`
	Addressed          int    `json:"addressed"`
	NotRelevant        int    `json:"not_relevant"`
	Unverified         int    `json:"unverified"`
	Undeclared         int    `json:"undeclared"`
	ReadyToResolve     int    `json:"ready_to_resolve"`
	Unresolved         int    `json:"unresolved"`
}

// ReadFeatureSummary只记录入口解析后的能力配置，不保存业务材料。
type ReadFeatureSummary struct {
	Version     int    `json:"version"`
	SourceViews bool   `json:"source_views"`
	LocalChecks bool   `json:"local_checks"`
	ClaimFocus  bool   `json:"claim_focus"`
	Contract    string `json:"contract"`
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
