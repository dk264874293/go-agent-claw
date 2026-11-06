package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func partialFixture(t *testing.T, work, id, secret string) {
	t.Helper()
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event taskstore.Event
	var state map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(event.Payload, &state); err != nil {
		t.Fatal(err)
	}
	result := state["last_result"].(map[string]any)
	result["stop_code"] = "tool_limit"
	result["partial_report"] = map[string]any{"text": "已读取文件；尚未交叉核对。" + secret, "source": "model", "origin_run_id": "old-run"}
	event.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(event)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPartialProjectionRetainsFailureAndRedactsText(t *testing.T) {
	work, id := t.TempDir(), strings.Repeat("a", 32)
	secret := "test-private-credential-0123456789"
	t.Setenv("PARTIAL_TEST_API_KEY", secret)
	partialFixture(t, work, id, secret)
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.PartialReport == nil || task.PartialReport.Source != "model" || task.PartialReport.OriginRunID != "old-run" || task.FinalText != "" || task.EngineStatus != "BUDGET_EXHAUSTED" || task.StopCode != "tool_limit" {
		t.Fatalf("部分分析、引擎结果或来源丢失: %+v", task)
	}
	if strings.Contains(task.PartialReport.Text, secret) || !strings.Contains(task.PartialReport.Text, "[REDACTED]") {
		t.Fatal("部分分析未脱敏")
	}
}

func TestPartialProjectionNewRunBoundariesDoNotPublishOldAnalysis(t *testing.T) {
	for _, boundary := range []string{"task_started", "attempt_started", "committed_finish", "returned_mismatch", "returned_match"} {
		t.Run(boundary, func(t *testing.T) {
			work, root, id := t.TempDir(), t.TempDir(), strings.Repeat("b", 32)
			partialFixture(t, work, id, "")
			switch boundary {
			case "task_started":
				path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
				raw, _ := os.ReadFile(path)
				var event taskstore.Event
				json.Unmarshal(raw, &event)
				event.Sequence, event.Kind = 2, "started"
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				json.NewEncoder(f).Encode(event)
				f.Close()
			default:
				payloads := []map[string]any{{"event_type": "run_attempt_started"}}
				if strings.HasPrefix(boundary, "returned_") {
					runID := "old-run"
					if boundary == "returned_mismatch" {
						runID = "new-run"
					}
					payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "BUDGET_EXHAUSTED", "stop_code": "tool_limit", "result_run_id": runID})
				}
				regressionObservation(t, root, strings.Repeat("c", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
				if boundary == "committed_finish" {
					path := filepath.Join(root, strings.Repeat("c", 32), "events.jsonl")
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					at := time.Now()
					json.NewEncoder(f).Encode(Event{SchemaVersion: 1, AttemptID: strings.Repeat("c", 32), Sequence: 2, RecordedAt: &at,
						WorkspaceID: observe.WorkspaceID(work), TaskID: id, SourceTaskSequence: 1,
						EventType: "task_event_committed", Payload: map[string]any{"source_kind": "finished"}})
					f.Close()
				}
			}
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if (task.PartialReport != nil) != (boundary == "returned_match" || boundary == "committed_finish") {
				t.Fatalf("运行边界错误地展示或隐藏报告: boundary=%s partial=%+v", boundary, task.PartialReport)
			}
		})
	}
}
