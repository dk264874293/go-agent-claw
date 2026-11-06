package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func regressionObservation(t *testing.T, root, aid, wid, tid string, at time.Time, payloads ...map[string]any) {
	t.Helper()
	dir := filepath.Join(root, aid)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i, p := range payloads {
		kind, _ := p["event_type"].(string)
		delete(p, "event_type")
		if kind == "" {
			kind = "run_configured"
		}
		ev := Event{SchemaVersion: 1, AttemptID: aid, Sequence: uint64(i + 1), RecordedAt: &at, WorkspaceID: wid, TaskID: tid, EventType: kind, ModelCallID: "model-1", Payload: p}
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionRegressionResumeSuppressesOldFinal(t *testing.T) {
	work := t.TempDir()
	id := strings.Repeat("a", 32)
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, _ := os.ReadFile(path)
	var old taskstore.Event
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	old.Kind, old.Sequence = "started", 2
	if err := json.NewEncoder(f).Encode(old); err != nil {
		t.Fatal(err)
	}
	f.Close()
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	list := x.Tasks("", "", "")
	if len(list) != 1 {
		t.Fatal(len(list))
	}
	if list[0].EngineStatus != "" || list[0].FinalText != "" || list[0].Activity == "result_recorded" {
		t.Fatalf("恢复中误用旧终态: %+v", list[0])
	}
	if len(list[0].Attempts) != 2 || len(list[0].Effects) != 1 {
		t.Fatal("丢失历史尝试或效果")
	}
}

func TestProjectionRegressionLatestBudgetDoesNotDependOnDirectoryOrder(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	tid := strings.Repeat("a", 32)
	fixture(t, work, tid)
	a, b := strings.Repeat("b", 32), strings.Repeat("c", 32)
	for _, aid := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(root, aid), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := readDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	wid := observe.WorkspaceID(work)
	now := time.Now()
	// 目录顺序中的最后一个故意放旧运行，不能覆盖当前taskstore额度8。
	regressionObservation(t, root, dirs[0].Name(), wid, tid, now, map[string]any{"event_type": "run_attempt_started"}, map[string]any{"max_model_calls": 8, "max_tool_calls": 20})
	regressionObservation(t, root, dirs[1].Name(), wid, tid, now.Add(-time.Hour), map[string]any{"event_type": "run_attempt_started"}, map[string]any{"max_model_calls": 3, "max_tool_calls": 5})
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.Budget.MaxModelCalls != 8 || task.Budget.MaxToolCalls != 20 {
		t.Fatalf("旧attempt覆盖权威额度: %+v", task.Budget)
	}
}

func TestProjectionRegressionWorkspaceAndTaskIdentityCannotChange(t *testing.T) {
	for _, field := range []string{"workspace", "task"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			aid := strings.Repeat("a", 32)
			tid := strings.Repeat("b", 32)
			wid := strings.Repeat("c", 32)
			regressionObservation(t, root, aid, wid, tid, time.Now(), map[string]any{"event_type": "run_attempt_started"})
			f, err := os.OpenFile(filepath.Join(root, aid, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			ev := Event{SchemaVersion: 1, AttemptID: aid, Sequence: 2, WorkspaceID: wid, TaskID: tid, EventType: "heartbeat"}
			if field == "workspace" {
				ev.WorkspaceID = strings.Repeat("d", 32)
			} else {
				ev.TaskID = strings.Repeat("d", 32)
			}
			json.NewEncoder(f).Encode(ev)
			f.Close()
			s := observationSource{}
			s.poll(root, filepath.Join(aid, "events.jsonl"), aid)
			if s.err == "" || len(s.events) != 1 {
				t.Fatalf("跨身份事件被采纳: error=%q count=%d", s.err, len(s.events))
			}
		})
	}
}

func TestProjectionRegressionLegacyPendingRetainsUnknown(t *testing.T) {
	work := t.TempDir()
	tid := strings.Repeat("a", 32)
	fixture(t, work, tid)
	path := filepath.Join(work, ".claw/tasks", tid, "events.jsonl")
	raw, _ := os.ReadFile(path)
	var ev taskstore.Event
	json.Unmarshal(raw, &ev)
	var state map[string]any
	json.Unmarshal(ev.Payload, &state)
	delete(state, "effect_ledger")
	state["pending"] = map[string]any{"pending-action": map[string]any{"id": "pending-action", "call": map[string]any{"id": "call", "name": "write_file", "arguments": map[string]any{"path": "unknown.txt"}}}}
	ev.Payload, _ = json.Marshal(state)
	b, _ := json.Marshal(ev)
	os.WriteFile(path, append(b, '\n'), 0600)
	x, _ := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.HistoryComplete || len(task.Effects) != 1 || task.Effects[0].Effect != "UNKNOWN" || task.Effects[0].File != nil {
		t.Fatalf("旧记录未知效果被猜测: %+v", task)
	}
}

func TestProjectionRegressionSameTaskIDInDifferentWorkspaces(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	id := strings.Repeat("a", 32)
	fixture(t, a, id)
	fixture(t, b, id)
	x, err := NewIndex(Config{Workspaces: []string{a, b}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	list := x.Tasks("", "", "")
	if len(list) != 2 || list[0].ID == list[1].ID || list[0].WorkspaceID == list[1].WorkspaceID {
		t.Fatalf("工作区串任务: %+v", list)
	}
}

func TestProjectionRegressionNegativeUsageCannotBecomeKnown(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	tid := strings.Repeat("b", 32)
	aid := strings.Repeat("c", 32)
	regressionObservation(t, root, aid, observe.WorkspaceID(work), tid, time.Now(), map[string]any{"event_type": "model_started"}, map[string]any{"event_type": "model_usage", "input_tokens": -1, "output_tokens": 2, "cost": -3, "currency": "CNY"}, map[string]any{"event_type": "run_returned", "status": "ANSWERED"})
	x, _ := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
	defer x.Close()
	attempt, ok := x.Attempt(aid)
	if !ok {
		t.Fatal("缺少attempt")
	}
	if attempt.Usage.InputTokens != nil || attempt.Usage.Cost != nil || attempt.Usage.KnownInput < 0 || attempt.Usage.KnownCost < 0 {
		t.Fatalf("负用量被当作已知: %+v", attempt.Usage)
	}
}

func TestProjectionRegressionConcurrentRefreshAndPlanSerialization(t *testing.T) {
	work := t.TempDir()
	tid := strings.Repeat("a", 32)
	fixture(t, work, tid)
	path := filepath.Join(work, ".claw/tasks", tid, "events.jsonl")
	raw, _ := os.ReadFile(path)
	var ev taskstore.Event
	json.Unmarshal(raw, &ev)
	var state map[string]any
	json.Unmarshal(ev.Payload, &state)
	state["plan"] = []any{map[string]any{"id": "step", "goal": "检查计划", "depends_on": []string{}, "done": false}}
	ev.Payload, _ = json.Marshal(state)
	data, _ := json.Marshal(ev)
	os.WriteFile(path, append(data, '\n'), 0600)
	x, _ := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	defer x.Close()
	snapshot := x.Tasks("", "", "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			x.Refresh()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if _, err := json.Marshal(snapshot); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
}

func TestProjectionRegressionCachedResultPreservesOriginalAttempt(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	tid := strings.Repeat("a", 32)
	fixture(t, work, tid)
	regressionObservation(t, root, strings.Repeat("b", 32), observe.WorkspaceID(work), tid, time.Now(), map[string]any{"event_type": "run_attempt_started"}, map[string]any{"event_type": "run_returned", "status": "BUDGET_EXHAUSTED", "stop_code": "model_limit", "result_run_id": "old-run"})
	x, _ := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if len(task.Attempts) != 2 {
		t.Fatalf("缓存返回不能吞掉原运行的历史动作: %+v", task.Attempts)
	}
}
