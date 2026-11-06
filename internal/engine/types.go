package engine

import (
	"fmt"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type TaskKind string

const (
	TaskAnswer TaskKind = "answer"
	TaskChange TaskKind = "change"
)

type TaskStatus string

const (
	Answered          TaskStatus = "ANSWERED"
	Verified          TaskStatus = "VERIFIED"
	NeedsVerification TaskStatus = "NEEDS_VERIFICATION"
	Blocked           TaskStatus = "BLOCKED"
	BudgetExhausted   TaskStatus = "BUDGET_EXHAUSTED"
	Cancelled         TaskStatus = "CANCELLED"
	Failed            TaskStatus = "FAILED"
)

// Budget 的零值使用默认值。恢复时仅应用显式调高的额度。
type Budget struct {
	MaxTurns      int
	MaxModelCalls int
	MaxToolCalls  int
	Timeout       time.Duration
}

func (b Budget) defaults() Budget {
	if b.MaxTurns == 0 {
		b.MaxTurns = 30
	}
	if b.MaxModelCalls == 0 {
		b.MaxModelCalls = 60
	}
	if b.MaxToolCalls == 0 {
		b.MaxToolCalls = 120
	}
	if b.Timeout == 0 {
		b.Timeout = 10 * time.Minute
	}
	return b
}

func (b Budget) valid() bool {
	return b.MaxTurns >= 0 && b.MaxModelCalls >= 0 && b.MaxToolCalls >= 0 && b.Timeout >= 0
}

type Usage struct {
	Turns      int           `json:"turns"`
	ModelCalls int           `json:"model_calls"`
	ToolCalls  int           `json:"tool_calls"`
	Elapsed    time.Duration `json:"elapsed"`
}

type EngineOptions struct {
	Mode                 tools.Mode
	EnableThinking       bool
	Plan                 bool
	Budget               Budget
	MaxContextBytes      int
	AnalysisContextBytes int // 分析请求软目标；零值为 96 KiB，不覆盖硬上限。
}

// TaskSpec 由调用方提供。恢复时读取原目标、验收命令和规划设置。
type TaskSpec struct {
	ID            string     `json:"id"`
	Goal          string     `json:"goal"`
	Kind          TaskKind   `json:"kind"`
	Mode          tools.Mode `json:"mode"`
	VerifyCommand string     `json:"verify_command,omitempty"`
	Plan          bool       `json:"plan"`
	Analysis      bool       `json:"analysis,omitempty"`
	Delegation    string     `json:"delegation,omitempty"`
	Resume        bool       `json:"-"`
}

type Verification struct {
	Passed  bool   `json:"passed"`
	Version string `json:"version,omitempty"`
	Output  string `json:"output,omitempty"`
}

// Delivery 描述动作当时的效果，不替代当前文件检查或任务验收。
type Delivery struct {
	HistoryComplete bool            `json:"history_complete"`
	Effects         []EffectReceipt `json:"effects"`
}

type EffectReceipt struct {
	ActionID string              `json:"action_id"`
	Tool     string              `json:"tool"`
	Status   schema.ToolStatus   `json:"status,omitempty"`
	Effect   schema.EffectStatus `json:"effect"`
	File     *FileEffect         `json:"file,omitempty"`
}

type FileEffect struct {
	Path          string `json:"path"`
	BeforeVersion string `json:"before_version"`
	AfterVersion  string `json:"after_version"`
}

// PartialReport 仅交付停止时的部分分析，不代表任务完成或通过验收。
type PartialReport struct {
	Text        string `json:"text"`
	Source      string `json:"source"`
	OriginRunID string `json:"origin_run_id"`
}

type RunResult struct {
	TaskID        string           `json:"task_id"`
	RunID         string           `json:"run_id"`
	Status        TaskStatus       `json:"status"`
	StopCode      string           `json:"stop_code,omitempty"`
	Reason        string           `json:"reason"`
	FinalText     string           `json:"final_text,omitempty"`
	PartialReport *PartialReport   `json:"partial_report,omitempty"`
	Analysis      *AnalysisReport  `json:"analysis,omitempty"`
	Reading       *ReadingCoverage `json:"reading,omitempty"`
	Delivery      *Delivery        `json:"delivery,omitempty"`
	Verification  *Verification    `json:"verification,omitempty"`
	Usage         Usage            `json:"usage"`
}

func (r RunResult) ExitCode() int {
	switch r.Status {
	case Answered, Verified:
		return 0
	case NeedsVerification, Blocked:
		return 2
	case BudgetExhausted:
		return 3
	case Cancelled:
		return 130
	default:
		return 1
	}
}

func (r RunResult) Summary() string {
	return fmt.Sprintf("任务 %s · %s · %s", r.TaskID, r.Status, r.Reason)
}
