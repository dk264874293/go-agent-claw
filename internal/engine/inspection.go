package engine

import (
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

// Inspection 只解释已记录的状态，不恢复任务、不核对当前工作区也不调用工具。
type Inspection struct {
	Spec         TaskSpec
	SessionID    string
	Limits       Budget
	Usage        Usage
	Plan         []InspectionStep
	Pending      int
	Delivery     *Delivery
	Verification *Verification
	Result       *RunResult
	Action       *InspectionAction
	Running      bool
	Sequence     uint64
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
		if json.Unmarshal(event.Payload, &clock) != nil || clock.Version != 1 || clock.Elapsed < v.Usage.Elapsed {
			return fmt.Errorf("任务计时事件无效")
		}
		v.Usage.Elapsed = clock.Elapsed
		v.Sequence = event.Sequence
		v.Action = nil
		return nil
	}
	var s taskState
	if json.Unmarshal(event.Payload, &s) != nil || s.Version != 1 {
		return fmt.Errorf("任务状态版本或内容无效")
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
	if json.Unmarshal(s.State, &state) != nil || state.Version != 1 {
		return fmt.Errorf("任务快照状态无效")
	}
	return nil
}
