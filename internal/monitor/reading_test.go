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

func readingFixture(t *testing.T, work, id, secret string) {
	t.Helper()
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ev taskstore.Event
	_ = json.Unmarshal(raw, &ev)
	var state map[string]any
	_ = json.Unmarshal(ev.Payload, &state)
	state["spec"].(map[string]any)["kind"] = "answer"
	state["spec"].(map[string]any)["mode"] = "readonly"
	state["spec"].(map[string]any)["delegation"] = "auto"
	result := state["last_result"].(map[string]any)
	result["status"] = "ANSWERED"
	result["stop_code"] = "answered"
	result["final_text"] = "历史答复"
	result["reading"] = map[string]any{"status": "partial", "manifest_id": "manifest", "complete_files": 14, "total_files": 15, "files": []any{map[string]any{"path": "docs/" + secret + ".md", "version": "hash", "total_bytes": 14038, "read_bytes": 13685, "complete": false, "missing": []any{map[string]any{"start": 13685, "end": 14038}}}}}
	ev.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(ev)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadingProjectionKeepsCoverageSeparateAndRedactsPaths(t *testing.T) {
	work, id := t.TempDir(), strings.Repeat("a", 32)
	secret := "private-reading-credential-0123456789"
	t.Setenv("MONITOR_READING_API_KEY", secret)
	readingFixture(t, work, id, secret)
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.Reading == nil || task.Reading.Status != "partial" || task.Reading.CompleteFiles != 14 || task.Delegation != "auto" || task.AnalysisEnabled || task.Verification != nil || task.EngineStatus != "ANSWERED" {
		t.Fatalf("覆盖、模式与验收耦合或丢失: %+v", task)
	}
	if len(task.Reading.Files) != 1 || strings.Contains(task.Reading.Files[0].Path, secret) || !strings.Contains(task.Reading.Files[0].Path, "[REDACTED]") || task.Reading.Files[0].Missing[0].End-task.Reading.Files[0].Missing[0].Start != 353 {
		t.Fatal("缺口错算或路径没有脱敏")
	}
}

func TestReadingProjectionDoesNotReuseOldAttempt(t *testing.T) {
	for _, boundary := range []string{"attempt_started", "returned_mismatch", "returned_match"} {
		t.Run(boundary, func(t *testing.T) {
			work, root, id := t.TempDir(), t.TempDir(), strings.Repeat("b", 32)
			readingFixture(t, work, id, "")
			payloads := []map[string]any{{"event_type": "run_attempt_started"}}
			if strings.HasPrefix(boundary, "returned_") {
				run := "old-run"
				if boundary == "returned_mismatch" {
					run = "new-run"
				}
				payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "ANSWERED", "stop_code": "answered", "result_run_id": run})
			}
			regressionObservation(t, root, strings.Repeat("c", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if (task.Reading != nil) != (boundary == "returned_match") || (task.FinalText != "") != (boundary == "returned_match") {
				t.Fatal("旧覆盖或旧答复污染新运行")
			}
		})
	}
}
