package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func qualityInspectionState() taskState {
	return taskState{
		Version: 2,
		Spec:    TaskSpec{Analysis: true, Mode: tools.ModeReadOnly, Kind: TaskAnswer, Delegation: "off", Quality: &QualitySpec{Version: 1}},
		Quality: &QualityState{Version: 1, Status: "exploring", CheckResult: "not_run", ReviewStatus: "not_run", RevisionStatus: "not_run"},
	}
}

func TestInspectionReadsQualityVersionAndMatchingElapsed(t *testing.T) {
	s := qualityInspectionState()
	raw, _ := json.Marshal(s)
	var view Inspection
	if err := view.Apply(taskstore.Event{Version: 1, Sequence: 1, Kind: "started", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if view.Quality == nil || view.Quality.Status != "exploring" || view.Spec.Quality == nil {
		t.Fatal("质量状态没有投影")
	}
	ordinary, _ := json.Marshal(taskState{Version: 1})
	if err := view.Apply(taskstore.Event{Version: 1, Sequence: 2, Kind: "model", Payload: ordinary}); err == nil || view.Spec.Quality == nil {
		t.Fatal("同一任务移除了质量门禁")
	}
	for _, version := range []int{1, 3} {
		clock, _ := json.Marshal(elapsedEvent{Version: version, Elapsed: time.Second})
		if err := view.Apply(taskstore.Event{Version: 1, Sequence: 2, Kind: "elapsed", Payload: clock}); err == nil || view.Sequence != 1 || view.Usage.Elapsed != 0 {
			t.Fatal("接受错误版本计时或污染已确认状态")
		}
	}
	clock, _ := json.Marshal(elapsedEvent{Version: 2, Elapsed: time.Second})
	if err := view.Apply(taskstore.Event{Version: 1, Sequence: 2, Kind: "elapsed", Payload: clock}); err != nil || view.Usage.Elapsed != time.Second {
		t.Fatalf("质量任务计时丢失: %v", err)
	}
	snapshot, _ := json.Marshal(taskstore.Snapshot{Version: 1, Sequence: 1, State: raw})
	if err := ValidateInspectionSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionRejectsMissingQualityGateAndUnknownVersions(t *testing.T) {
	for _, mutate := range []func(*taskState){
		func(s *taskState) { s.Version = 1 },
		func(s *taskState) { s.Version = 3 },
		func(s *taskState) { s.Spec.Quality = nil },
		func(s *taskState) { s.Quality = nil },
		func(s *taskState) { s.Spec.Quality.Version = 2 },
		func(s *taskState) { s.Quality.Version = 2 },
	} {
		s := qualityInspectionState()
		mutate(&s)
		raw, _ := json.Marshal(s)
		view := Inspection{Sequence: 1, Running: true}
		if err := view.Apply(taskstore.Event{Version: 1, Sequence: 2, Kind: "finished", Payload: raw}); err == nil || view.Sequence != 1 || !view.Running {
			t.Fatal("坏质量状态被接受或修改了投影")
		}
		snapshot, _ := json.Marshal(taskstore.Snapshot{Version: 1, Sequence: 1, State: raw})
		if err := ValidateInspectionSnapshot(snapshot); err == nil {
			t.Fatal("坏质量快照被接受")
		}
	}
}
