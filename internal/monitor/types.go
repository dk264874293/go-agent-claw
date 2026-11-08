// Package monitor 提供本地只读任务观测，不执行或恢复任务。
package monitor

import (
	"time"

	"github.com/dk264874293/go-agent-claw/internal/engine"
)

type Config struct {
	Workspaces     []string
	EvalRoots      []string
	ObserveDir     string
	ModelSlow      time.Duration
	ToolSlow       time.Duration
	StaleAfter     time.Duration
	ModelRemaining int
	ToolRemaining  int
	RepeatCount    int
}

func (c *Config) defaults() {
	if c.ModelSlow <= 0 {
		c.ModelSlow = 60 * time.Second
	}
	if c.ToolSlow <= 0 {
		c.ToolSlow = 30 * time.Second
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = 10 * time.Second
	}
	if c.ModelRemaining == 0 {
		c.ModelRemaining = 1
	}
	if c.ToolRemaining == 0 {
		c.ToolRemaining = 2
	}
	if c.RepeatCount <= 0 {
		c.RepeatCount = 3
	}
}

type Workspace struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Health string `json:"health"`
	Error  string `json:"error,omitempty"`
}
type Budget struct {
	MaxModelCalls int   `json:"max_model_calls"`
	MaxToolCalls  int   `json:"max_tool_calls"`
	MaxTurns      int   `json:"max_turns"`
	TimeoutMS     int64 `json:"timeout_ms"`
}
type Usage struct {
	ModelCalls int   `json:"model_calls"`
	ToolCalls  int   `json:"tool_calls"`
	Turns      int   `json:"turns"`
	ElapsedMS  int64 `json:"elapsed_ms"`
}

// TaskSummary 用于列表与概览，不包含正文、报告、历史尝试和证据内容。
type TaskSummary struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	TaskID         string     `json:"task_id"`
	Goal           string     `json:"goal"`
	Mode           string     `json:"mode"`
	Kind           string     `json:"kind"`
	Activity       string     `json:"activity"`
	ActivityGroups []string   `json:"activity_groups"`
	EngineStatus   string     `json:"engine_status"`
	StopCode       string     `json:"stop_code,omitempty"`
	RecordQuality  string     `json:"record_quality"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	Budget         Budget     `json:"budget"`
	Usage          Usage      `json:"usage"`
	AlertCount     int        `json:"alert_count"`
}

type OverviewCounts struct {
	Running  int `json:"running"`
	Approval int `json:"approval"`
	Alerts   int `json:"alerts"`
	Unknown  int `json:"unknown"`
}

type Overview struct {
	Revision   uint64         `json:"revision"`
	Total      int            `json:"total"`
	Counts     OverviewCounts `json:"counts"`
	Recent     []TaskSummary  `json:"recent"`
	Attention  []TaskSummary  `json:"attention"`
	Workspaces []Workspace    `json:"workspaces"`
}

type Alert struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	AttemptID  string `json:"attempt_id,omitempty"`
	Sequence   uint64 `json:"sequence,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

type CategorizedAlert struct {
	Alert
	Category string `json:"category"`
}

type AlertGroup struct {
	Task   TaskSummary        `json:"task"`
	Alerts []CategorizedAlert `json:"alerts"`
}

type AlertPage struct {
	Items      []AlertGroup `json:"items"`
	Total      int          `json:"total"`
	AlertTotal int          `json:"alert_total"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
}
type Evidence struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Artifact 的读取入口由服务端映射，版本来自确认回执；页面不能传入文件路径。
type Artifact struct {
	ActionID   string `json:"action_id"`
	Path       string `json:"path"`
	Version    string `json:"version"`
	EvidenceID string `json:"evidence_id,omitempty"`
}
type ReadMemory struct {
	Version       int `json:"version"`
	EvidenceCount int `json:"evidence_count"`
	NoteCount     int `json:"note_count"`
	Processed     int `json:"processed_count"`
	Invalidations int `json:"invalidation_count"`
}

// Quality 只展示质量阶段摘要；计算输入、复核正文及原文不随页面状态返回。
type Quality struct {
	Version        int      `json:"version"`
	DraftID        string   `json:"draft_id,omitempty"`
	Status         string   `json:"status"`
	CheckResult    string   `json:"check_result"`
	ReviewStatus   string   `json:"review_status"`
	RevisionStatus string   `json:"revision_status"`
	Unresolved     []string `json:"unresolved,omitempty"`
}

// DeliveryReview只展示普通任务的处理声明与引用身份，不承载候选正文或工具意图。
type DeliveryReview struct {
	ContentComparisons []engine.DeliveryReviewContentComparison `json:"content_comparisons,omitempty"`
	Version            int                                      `json:"version"`
	Status             string                                   `json:"status"`
	AdmissionCount     int                                      `json:"admission_count"`
	OriginRunID        string                                   `json:"origin_run_id,omitempty"`
	TriggerReasons     []string                                 `json:"trigger_reasons,omitempty"`
	Unresolved         []DeliveryReviewIssue                    `json:"unresolved,omitempty"`
	Documents          int                                      `json:"documents"`
	Addressed          int                                      `json:"addressed"`
	NotRelevant        int                                      `json:"not_relevant"`
	Unverified         int                                      `json:"unverified"`
	Undeclared         int                                      `json:"undeclared"`
	ReadyToResolve     int                                      `json:"ready_to_resolve"`
	Omitted            int                                      `json:"omitted,omitempty"`
	ErrorClass         string                                   `json:"error_class,omitempty"`
}
type DeliveryReviewIssue struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Path        string   `json:"path,omitempty"`
	Version     string   `json:"version,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	FindingIDs  []string `json:"finding_ids,omitempty"`
	CheckIDs    []string `json:"check_ids,omitempty"`
	Reason      string   `json:"reason"`
}
type Activity struct {
	Kind      string     `json:"kind"`
	AgentID   string     `json:"agent_id"`
	ID        string     `json:"id"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}
type ModelUsage struct {
	KnownInput    int64    `json:"known_input_tokens"`
	KnownOutput   int64    `json:"known_output_tokens"`
	KnownCost     float64  `json:"known_cost"`
	UnknownTokens int      `json:"unknown_token_calls"`
	UnknownCost   int      `json:"unknown_cost_calls"`
	InputTokens   *int64   `json:"input_tokens"`
	OutputTokens  *int64   `json:"output_tokens"`
	Cost          *float64 `json:"cost"`
	Currency      string   `json:"currency"`
}
type Attempt struct {
	ID             string     `json:"id"`
	ResultRunID    string     `json:"result_run_id,omitempty"`
	Entry          string     `json:"entry"`
	Activity       string     `json:"activity"`
	Activities     []Activity `json:"activities"`
	Status         string     `json:"status,omitempty"`
	StopCode       string     `json:"stop_code,omitempty"`
	RecordQuality  string     `json:"record_quality"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	Usage          ModelUsage `json:"model_usage"`
	Legacy         bool       `json:"legacy"`
	Events         []Event    `json:"-"`
}
type Task struct {
	latestAttemptAt    *time.Time
	Traces             []TraceSpan             `json:"traces"`
	ID                 string                  `json:"id"`
	WorkspaceID        string                  `json:"workspace_id"`
	TaskID             string                  `json:"task_id"`
	Goal               string                  `json:"goal"`
	Mode               string                  `json:"mode"`
	Kind               string                  `json:"kind"`
	Activity           string                  `json:"activity"`
	EngineStatus       string                  `json:"engine_status"`
	StopCode           string                  `json:"stop_code,omitempty"`
	ResultRunID        string                  `json:"result_run_id,omitempty"`
	RecordQuality      string                  `json:"record_quality"`
	LastObservedAt     *time.Time              `json:"last_observed_at,omitempty"`
	Budget             Budget                  `json:"budget"`
	Usage              Usage                   `json:"usage"`
	Attempts           []Attempt               `json:"attempts"`
	Effects            []engine.EffectReceipt  `json:"effects"`
	HistoryComplete    bool                    `json:"history_complete"`
	Verification       *engine.Verification    `json:"verification,omitempty"`
	FinalText          string                  `json:"final_text"`
	FinalTextDisplay   string                  `json:"final_text_display"`
	PartialReport      *engine.PartialReport   `json:"partial_report,omitempty"`
	Analysis           *engine.AnalysisReport  `json:"analysis,omitempty"`
	AnalysisEnabled    bool                    `json:"analysis_enabled"`
	Delegation         string                  `json:"delegation,omitempty"`
	Reading            *engine.ReadingCoverage `json:"reading,omitempty"`
	ReadPageBytes      int                     `json:"read_page_bytes,omitempty"`
	ReadContextVersion int                     `json:"read_context_version,omitempty"`
	ReadFeatures       *engine.ReadFeatureSpec `json:"read_features,omitempty"`
	ReadScope          *engine.ReadScopeSpec   `json:"read_scope,omitempty"`
	OutputStyle        string                  `json:"output_style,omitempty"`
	ReadMemory         *ReadMemory             `json:"read_memory,omitempty"`
	Report             *engine.ReportDelivery  `json:"report,omitempty"`
	Quality            *Quality                `json:"quality,omitempty"`
	DeliveryReview     *DeliveryReview         `json:"delivery_review,omitempty"`
	LocalChecks        []LocalCheck            `json:"local_checks,omitempty"`
	Artifacts          []Artifact              `json:"artifacts,omitempty"`
	Alerts             []Alert                 `json:"alerts"`
	Plan               []engine.InspectionStep `json:"plan"`
	Evidence           []Evidence              `json:"evidence"`
	Evaluations        []Evaluation            `json:"evaluations"`
}
type Event struct {
	SchemaVersion      int            `json:"schema_version"`
	AttemptID          string         `json:"attempt_id"`
	Sequence           uint64         `json:"sequence"`
	RecordedAt         *time.Time     `json:"recorded_at,omitempty"`
	EventType          string         `json:"event_type"`
	WorkspaceID        string         `json:"workspace_id,omitempty"`
	TaskID             string         `json:"task_id,omitempty"`
	AgentID            string         `json:"agent_id,omitempty"`
	ParentAgentID      string         `json:"parent_agent_id,omitempty"`
	ActionID           string         `json:"action_id,omitempty"`
	ModelCallID        string         `json:"model_call_id,omitempty"`
	SourceTaskSequence uint64         `json:"source_task_sequence,omitempty"`
	Payload            map[string]any `json:"payload"`
}
type TraceSpan struct {
	Name       string    `json:"name"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	DurationMS int64     `json:"duration_ms"`
	Depth      int       `json:"depth"`
	RunID      string    `json:"run_id"`
}
type Evaluation struct {
	WorkspaceID       string     `json:"workspace_id"`
	ID                string     `json:"id"`
	ExperimentID      string     `json:"experiment_id"`
	CaseID            string     `json:"case_id"`
	Repeat            int        `json:"repeat"`
	Arm               string     `json:"arm"`
	TaskID            string     `json:"task_id"`
	EngineStatus      string     `json:"engine_status"`
	StopCode          string     `json:"stop_code"`
	ArtifactResult    string     `json:"artifact_result"`
	ScopeResult       string     `json:"scope_result"`
	EngineResult      string     `json:"engine_result"`
	EnvironmentResult string     `json:"environment_result"`
	CheckerResult     string     `json:"checker_result"`
	CombinedPass      bool       `json:"combined_pass"`
	Labels            []string   `json:"labels"`
	Evidence          []Evidence `json:"evidence"`
	ModelCalls        *int       `json:"model_calls"`
	ToolCalls         *int       `json:"tool_calls"`
	Cost              *float64   `json:"cost"`
	ReadError         string     `json:"read_error,omitempty"`
}
