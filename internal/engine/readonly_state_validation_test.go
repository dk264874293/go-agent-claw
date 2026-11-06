package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func readonlyValidationFixture() taskState {
	return taskState{
		Version: 1, Spec: TaskSpec{Goal: "检查只读停止记录", Kind: TaskAnswer, Mode: tools.ModeReadOnly}, Limits: (Budget{}).defaults(),
		ReadonlyStopID: "boundary",
		ReadonlyFailures: map[string]*ReadonlyFailureBoundary{"key": {
			ID: "boundary", Key: "key", Tool: "list_dir", Path: "claw", ErrorCode: tools.ErrPathNotDirectory,
			Count: 3, LastActionID: "action", Stopped: true,
		}},
	}
}

func TestValidateReadonlyStateRejectsMalformedOptionalRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskState)
	}{
		{"空边界", func(s *taskState) { s.ReadonlyFailures["key"] = nil }},
		{"空边界身份", func(s *taskState) { s.ReadonlyFailures["key"].ID = "" }},
		{"边界键不匹配", func(s *taskState) { s.ReadonlyFailures["key"].Key = "other" }},
		{"空边界键", func(s *taskState) {
			b := s.ReadonlyFailures["key"]
			delete(s.ReadonlyFailures, "key")
			b.Key = ""
			s.ReadonlyFailures[""] = b
		}},
		{"重复边界身份", func(s *taskState) { b := *s.ReadonlyFailures["key"]; b.Key = "other"; s.ReadonlyFailures["other"] = &b }},
		{"绝对路径", func(s *taskState) { s.ReadonlyFailures["key"].Path = "/outside" }},
		{"上级路径", func(s *taskState) { s.ReadonlyFailures["key"].Path = "../outside" }},
		{"未规范路径", func(s *taskState) { s.ReadonlyFailures["key"].Path = "dir/../claw" }},
		{"空路径", func(s *taskState) { s.ReadonlyFailures["key"].Path = "" }},
		{"NUL路径", func(s *taskState) { s.ReadonlyFailures["key"].Path = "claw\x00" }},
		{"错误工具", func(s *taskState) { s.ReadonlyFailures["key"].Tool = "bash" }},
		{"错误代码", func(s *taskState) { s.ReadonlyFailures["key"].ErrorCode = "permission_denied" }},
		{"零次数", func(s *taskState) { s.ReadonlyFailures["key"].Count = 0 }},
		{"负次数", func(s *taskState) { s.ReadonlyFailures["key"].Count = -1 }},
		{"次数未达停止", func(s *taskState) { s.ReadonlyFailures["key"].Count = 2 }},
		{"停止标记不匹配", func(s *taskState) { s.ReadonlyFailures["key"].Stopped = false }},
		{"缺少动作引用", func(s *taskState) { s.ReadonlyFailures["key"].LastActionID = "" }},
		{"停止身份未找到", func(s *taskState) { s.ReadonlyStopID = "missing" }},
		{"当前停止已解决", func(s *taskState) { s.ReadonlyFailures["key"].Resolved = true }},
		{"停止指向未达阈值边界", func(s *taskState) { s.ReadonlyFailures["key"].Count = 2; s.ReadonlyFailures["key"].Stopped = false }},
		{"空收尾", func(s *taskState) { s.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": nil} }},
		{"空收尾身份", func(s *taskState) {
			s.ReadonlyClosings = map[string]*ReadonlyClosing{"": {OriginRunID: "old-run", Status: "started"}}
		}},
		{"空来源运行", func(s *taskState) { s.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": {Status: "started"}} }},
		{"非法收尾状态", func(s *taskState) {
			s.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": {OriginRunID: "old-run", Status: "unknown"}}
		}},
		{"完成无文本", func(s *taskState) {
			s.ReadonlyClosings = map[string]*ReadonlyClosing{"boundary": {OriginRunID: "old-run", Status: "completed", Text: " \n"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := readonlyValidationFixture()
			tc.mutate(&state)
			if err := validateReadonlyState(state); err == nil {
				t.Fatalf("损坏的可选状态未被拒绝: %+v", state)
			}
		})
	}
}

func TestValidateReadonlyStateAllowsLegacyAndHistoricalRecords(t *testing.T) {
	states := []taskState{{Version: 1}, readonlyValidationFixture()}
	intermediate := readonlyValidationFixture()
	intermediate.ReadonlyStopID = ""
	states = append(states, intermediate) // 批次尚未选择停止原因时，边界已经达到三次。
	resolved := readonlyValidationFixture()
	resolved.ReadonlyStopID = ""
	resolved.ReadonlyFailures["key"].Resolved = true
	states = append(states, resolved)
	history := readonlyValidationFixture()
	history.ReadonlyClosings = map[string]*ReadonlyClosing{
		"old-boundary": {OriginRunID: "old-run", Status: "completed", Text: "历史部分分析"},
		"boundary":     {OriginRunID: "current-run", Status: "started"},
		"failed-old":   {OriginRunID: "earlier-run", Status: "failed", ErrorKind: "request_timeout"},
	}
	states = append(states, history)
	for i, state := range states {
		if err := validateReadonlyState(state); err != nil {
			t.Fatalf("合法状态 %d 被拒绝: %v", i, err)
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		view := Inspection{}
		if err := view.Apply(taskstore.Event{Version: 1, Sequence: 1, Kind: "model", Payload: raw}); err != nil {
			t.Fatal(err)
		}
		snapshot, _ := json.Marshal(taskstore.Snapshot{Version: 1, Sequence: 1, State: raw})
		if err := ValidateInspectionSnapshot(snapshot); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadonlyMalformedStateIsRejectedByAllReaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskState)
	}{
		{"边界null", func(s *taskState) { s.ReadonlyFailures["key"] = nil }},
		{"收尾null", func(s *taskState) { s.ReadonlyClosings = map[string]*ReadonlyClosing{"old": nil} }},
		{"缺少边界", func(s *taskState) { s.ReadonlyFailures = nil }},
		{"损坏收尾", func(s *taskState) {
			s.ReadonlyClosings = map[string]*ReadonlyClosing{"old": {OriginRunID: "old-run", Status: "completed"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := readonlyValidationFixture()
			broken := readonlyValidationFixture()
			tc.mutate(&broken)
			raw, _ := json.Marshal(broken)
			view := Inspection{Sequence: 4, Running: true, Spec: TaskSpec{Goal: "已核对目标"}}
			if err := view.Apply(taskstore.Event{Version: 1, Sequence: 5, Kind: "finished", Payload: raw}); err == nil {
				t.Fatal("观测读取没有拒绝损坏状态")
			}
			if view.Sequence != 4 || !view.Running || view.Spec.Goal != "已核对目标" {
				t.Fatal("拒绝坏事件时已修改观测投影")
			}
			snapshot, _ := json.Marshal(taskstore.Snapshot{Version: 1, Sequence: 1, State: raw})
			if err := ValidateInspectionSnapshot(snapshot); err == nil {
				t.Fatal("快照读取没有拒绝损坏状态")
			}
			for _, brokenEvent := range []bool{false, true} {
				store, err := taskstore.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				task, err := store.CreateTask(newID())
				if err != nil {
					t.Fatal(err)
				}
				eventState, snapshotState := valid, broken
				if brokenEvent {
					eventState, snapshotState = broken, valid
				}
				if _, err := task.Append("readonly_failure", eventState); err != nil {
					t.Fatal(err)
				}
				if err := task.SaveSnapshot(snapshotState); err != nil {
					t.Fatal(err)
				}
				if _, err := loadTaskState(task); err == nil {
					t.Fatalf("恢复读取没有拒绝损坏状态，坏事件=%t", brokenEvent)
				}
				task.Close()
				store.Close()
			}
		})
	}
}

func TestResumeRejectsNullReadonlyBoundaryBeforeModel(t *testing.T) {
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	session := ctxpkg.NewSession("损坏只读状态", dir)
	state := readonlyValidationFixture()
	state.Spec.ID, state.WorkDir, state.SessionID = newID(), canonical, session.ID
	state.ReadonlyFailures["key"] = nil
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(state.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Append("readonly_failure", state); err != nil {
		t.Fatal(err)
	}
	if err := task.SaveSnapshot(state); err != nil {
		t.Fatal(err)
	}
	task.Close()
	store.Close()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("损坏状态仍然调用模型")
		return nil, nil
	})
	eng := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := eng.Run(context.Background(), session, TaskSpec{ID: state.Spec.ID, Resume: true}, nil)
	if err != nil || result.Status != Blocked || result.PartialReport != nil || result.Usage.ModelCalls != 0 {
		t.Fatalf("损坏状态未安全停止: %+v %v", result, err)
	}
}
