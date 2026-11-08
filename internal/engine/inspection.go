package engine

import (
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

// Inspection 只解释已记录的状态，不恢复任务、不核对当前工作区也不调用工具。
type Inspection struct {
	deliveryReviewState *DeliveryReviewState
	DeliveryReview      *DeliveryReviewSummary
	ReadMemory          *ReadMemoryState
	Report              *ReportState
	Quality             *QualityState
	LocalChecks         []LocalCheckSummary
	Spec                TaskSpec
	SessionID           string
	Limits              Budget
	Usage               Usage
	Plan                []InspectionStep
	Pending             int
	Delivery            *Delivery
	Verification        *Verification
	Result              *RunResult
	Analysis            *AnalysisState
	Reading             *ReadingCoverage
	Delegation          *DelegationState
	Action              *InspectionAction
	Running             bool
	Sequence            uint64
}
type InspectionStep struct {
	ID        string   `json:"id"`
	Goal      string   `json:"goal"`
	DependsOn []string `json:"depends_on"`
	Done      bool     `json:"done"`
}
type InspectionAction struct {
	ID     string            `json:"id"`
	Tool   string            `json:"tool"`
	StepID string            `json:"step_id,omitempty"`
	Digest string            `json:"digest"`
	Result schema.ToolResult `json:"result"`
}

func (v *Inspection) Apply(event taskstore.Event) error {
	if event.Version != 1 || event.Sequence != v.Sequence+1 {
		return fmt.Errorf("任务事件序号或版本无效")
	}
	if event.Kind == "elapsed" {
		var clock elapsedEvent
		version := 1
		if v.Spec.Quality != nil {
			version = 2
		}
		if v.Spec.DeliveryReview != nil {
			version = 3
		}
		if json.Unmarshal(event.Payload, &clock) != nil || clock.Version != version || clock.Elapsed < v.Usage.Elapsed {
			return fmt.Errorf("任务计时事件无效")
		}
		v.Usage.Elapsed = clock.Elapsed
		v.Sequence = event.Sequence
		v.Action = nil
		return nil
	}
	var s taskState
	if json.Unmarshal(event.Payload, &s) != nil {
		return fmt.Errorf("任务状态版本或内容无效")
	}
	if err := ValidateTaskStateVersion(s); err != nil {
		return fmt.Errorf("任务状态版本或内容无效: %w", err)
	}
	if v.Sequence > 0 && (v.Spec.Quality != nil) != (s.Spec.Quality != nil) {
		return fmt.Errorf("同一任务不能升级或移除质量策略")
	}
	if v.Sequence > 0 {
		if err := validateDeliveryReviewTransition(taskState{Spec: v.Spec, DeliveryReview: v.deliveryReviewState}, s); err != nil {
			return err
		}
		if err := validateReadFeatureTransition(taskState{Spec: v.Spec}, s); err != nil {
			return err
		}
		if err := validateQualityTransition(taskState{Spec: v.Spec, Quality: v.Quality}, s); err != nil {
			return err
		}
	}
	if err := validateReadonlyState(s); err != nil {
		return fmt.Errorf("任务只读停止状态无效: %w", err)
	}
	if err := validateReportState(s); err != nil {
		return err
	}
	if err := validateReadMemoryState(s); err != nil {
		return err
	}
	if err := validateReadPolicy(s); err != nil {
		return err
	}
	if err := validateAnalysisState(s); err != nil {
		return fmt.Errorf("任务分析状态无效: %w", err)
	}
	if err := ValidateDelegationState(s); err != nil {
		return fmt.Errorf("任务委派状态无效: %w", err)
	}
	if event.Kind == "started" {
		v.Running = true
		v.Result = nil
	}
	if event.Kind == "finished" {
		v.Running = false
		v.Result = s.LastResult
	}
	v.Spec, v.SessionID, v.Limits, v.Usage = s.Spec, s.SessionID, s.Limits, s.Usage
	v.Pending = len(s.Pending)
	v.Delivery = deliveryFromState(s)
	v.Verification = s.Verification
	v.Analysis = analysisStateOf(s)
	v.ReadMemory, v.Report = s.ReadMemory, s.Report
	v.Quality = s.Quality
	v.DeliveryReview = recordedDeliveryReviewSummary(s)
	v.deliveryReviewState = s.DeliveryReview
	v.LocalChecks = append([]LocalCheckSummary(nil), s.LocalChecks...)
	v.Reading, v.Delegation = s.Reading, s.Delegation
	v.Plan = nil
	for _, p := range s.Plan {
		v.Plan = append(v.Plan, InspectionStep{p.ID, p.Goal, p.DependsOn, p.Done})
	}
	v.Action = nil
	if event.Kind == "action_result" && s.LastAction != nil {
		a := s.LastAction
		v.Action = &InspectionAction{a.ID, a.Tool, a.StepID, a.Digest, a.Result}
	}
	v.Sequence = event.Sequence
	return nil
}

func ValidateInspectionSnapshot(raw []byte) error {
	var s taskstore.Snapshot
	if json.Unmarshal(raw, &s) != nil || s.Version != 1 || s.Sequence == 0 {
		return fmt.Errorf("任务快照无效")
	}
	var state taskState
	if json.Unmarshal(s.State, &state) != nil {
		return fmt.Errorf("任务快照状态无效")
	}
	if err := ValidateTaskStateVersion(state); err != nil {
		return fmt.Errorf("任务快照状态无效: %w", err)
	}
	if err := validateReadonlyState(state); err != nil {
		return fmt.Errorf("任务快照的只读停止状态无效: %w", err)
	}
	if err := validateReportState(state); err != nil {
		return err
	}
	if err := validateReadMemoryState(state); err != nil {
		return err
	}
	if err := validateReadPolicy(state); err != nil {
		return err
	}
	if err := validateAnalysisState(state); err != nil {
		return fmt.Errorf("任务快照的分析状态无效: %w", err)
	}
	if err := ValidateDelegationState(state); err != nil {
		return err
	}
	return nil
}
