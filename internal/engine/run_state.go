package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

var (
	errBudget        = errors.New("任务额度已耗尽")
	errContextBudget = errors.New("任务必要上下文超过请求上限")
	errStorage       = errors.New("任务记录未可靠保存")
)

type budgetLimit struct {
	code, label string
}

func (e budgetLimit) Error() string { return errBudget.Error() + "：" + e.label }
func (e budgetLimit) Unwrap() error { return errBudget }

type elapsedEvent struct {
	Version int           `json:"version"`
	Elapsed time.Duration `json:"elapsed"`
}

type pendingAction struct {
	ID       string          `json:"id"`
	Call     schema.ToolCall `json:"call"`
	StepID   string          `json:"step_id,omitempty"`
	Before   *fileVersion    `json:"before,omitempty"`
	Expected *fileVersion    `json:"expected,omitempty"`
}

type actionRecord struct {
	ID     string            `json:"id"`
	Tool   string            `json:"tool"`
	StepID string            `json:"step_id,omitempty"`
	Digest string            `json:"digest"`
	Result schema.ToolResult `json:"result"`
}

type taskState struct {
	DeliveryReview   *DeliveryReviewState                `json:"delivery_review,omitempty"`
	ReadMemory       *ReadMemoryState                    `json:"read_memory,omitempty"`
	Version          int                                 `json:"version"`
	Spec             TaskSpec                            `json:"spec"`
	SessionID        string                              `json:"session_id"`
	WorkDir          string                              `json:"work_dir"`
	Limits           Budget                              `json:"limits"`
	Usage            Usage                               `json:"usage"`
	Plan             []planStep                          `json:"plan,omitempty"`
	Pending          map[string]pendingAction            `json:"pending,omitempty"`
	Completed        map[string]schema.ToolResult        `json:"completed_actions,omitempty"`
	Progress         []string                            `json:"progress,omitempty"`
	Verification     *Verification                       `json:"verification,omitempty"`
	LastAction       *actionRecord                       `json:"last_action,omitempty"`
	LastResult       *RunResult                          `json:"last_result,omitempty"`
	EffectLedger     *Delivery                           `json:"effect_ledger,omitempty"`
	BudgetClosing    *BudgetClosing                      `json:"budget_closing,omitempty"`
	ReadonlyFailures map[string]*ReadonlyFailureBoundary `json:"readonly_failures,omitempty"`
	ReadonlyClosings map[string]*ReadonlyClosing         `json:"readonly_closings,omitempty"`
	ReadonlyStopID   string                              `json:"readonly_stop_id,omitempty"`
	Analysis         *AnalysisState                      `json:"analysis,omitempty"`
	Quality          *QualityState                       `json:"quality,omitempty"`
	Report           *ReportState                        `json:"report,omitempty"`
	Delegation       *DelegationState                    `json:"delegation,omitempty"`
	Reading          *ReadingCoverage                    `json:"reading,omitempty"`
	LocalChecks      []LocalCheckSummary                 `json:"local_checks,omitempty"`
}

type runState struct {
	mu                sync.Mutex
	state             taskState
	started           time.Time
	elapsed           time.Duration
	persist           func(string, taskState) error
	artifact          func(string, []byte) error
	readArtifact      func(string, int, int) ([]byte, error)
	readArtifactPage  func(string, int, int) (taskstore.ArtifactPage, error)
	runID             string
	reads             readCatalog
	fatalStorage      error
	actionRefs        map[string]string
	subSlots          chan struct{}
	reportMu          sync.Mutex
	resumeReceipts    map[string]schema.ToolResult
	analysisRuntime   analysisRuntime
	discoveries       []schema.DiscoveryReceipt
	discoveryOwners   map[string]string
	readReuse         readReuseRuntime
	handoffs          handoffRuntime
	historyStarts     map[string]int
	historyBackground map[string]string
	carriers          workNotesCarrierRuntime
}

func (r *runState) record(kind string, apply func(*taskState) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := apply(&r.state); err != nil {
		return err
	}
	r.state.Usage.Elapsed = r.elapsed + time.Since(r.started)
	if r.persist == nil {
		return nil
	}
	if err := r.persist(kind, r.state); err != nil {
		storageErr := fmt.Errorf("%w：保存任务事件 %s 失败: %w", errStorage, kind, err)
		r.fatalStorage = errors.Join(r.fatalStorage, storageErr)
		return storageErr
	}
	return nil
}

func (r *runState) reserve(ctx context.Context, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.record(kind, func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.checkSubBudgetLocked(ctx, kind); err != nil {
			return err
		}
		switch kind {
		case "turn":
			if s.Usage.Turns >= s.Limits.MaxTurns {
				return budgetLimit{code: "turn_limit", label: "主循环轮数"}
			}
			s.Usage.Turns++
		case "model":
			// 子任务可能在提示构建后耗尽共享工具额度，准入时再次检查。
			if s.Usage.ToolCalls >= s.Limits.MaxToolCalls {
				return toolLimit()
			}
			if s.Usage.ModelCalls >= s.Limits.MaxModelCalls {
				return budgetLimit{code: "model_limit", label: "模型调用次数"}
			}
			s.Usage.ModelCalls++
		case "tool":
			if s.Usage.ToolCalls >= s.Limits.MaxToolCalls {
				return budgetLimit{code: "tool_limit", label: "工具调用次数"}
			}
			s.Usage.ToolCalls++
		}
		r.chargeSubBudgetLocked(ctx, kind)
		return nil
	})
}

func (r *runState) note(s *taskState, value string) {
	s.Progress = append(s.Progress, truncateUTF8(value, 700))
	if len(s.Progress) > 20 {
		s.Progress = s.Progress[len(s.Progress)-20:]
	}
}

type runContextKey struct{}

func currentRun(ctx context.Context) *runState {
	r, _ := ctx.Value(runContextKey{}).(*runState)
	return r
}
