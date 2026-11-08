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
	if task.Reading.ScopeKind != "legacy" || task.ReadScope != nil || task.OutputStyle != "" {
		t.Fatalf("为旧记录补造了任务来源或输出选择: %+v", task)
	}
}

func TestDeclaredReadingProjectionRetainsPolicyAndScopeIdentity(t *testing.T) {
	work, id := t.TempDir(), strings.Repeat("e", 32)
	secret := "source-policy-credential-123456789"
	t.Setenv("MONITOR_SOURCE_POLICY_API_KEY", secret)
	readingFixture(t, work, id, secret)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ev taskstore.Event
	var state map[string]any
	_ = json.Unmarshal(before, &ev)
	_ = json.Unmarshal(ev.Payload, &state)
	spec := state["spec"].(map[string]any)
	spec["output_style"] = "detailed"
	spec["read_scope"] = map[string]any{"version": 1, "paths": []string{"docs/" + secret}, "name_glob": "*.md", "require_complete": true}
	reading := state["last_result"].(map[string]any)["reading"].(map[string]any)
	reading["scope_kind"], reading["scope_id"], reading["scope_revision"] = "declared", "scope-1", "revision-1"
	reading["manifest_ids"] = []string{"first", "second"}
	reading["unresolved_paths"] = []string{"docs/" + secret}
	ev.Payload, _ = json.Marshal(state)
	raw, _ := json.Marshal(ev)
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.ReadScope == nil || !task.ReadScope.RequireComplete || task.OutputStyle != "detailed" || task.Reading == nil || task.Reading.ScopeKind != "declared" || task.Reading.ScopeID != "scope-1" || task.Reading.ScopeRevision != "revision-1" || len(task.Reading.ManifestIDs) != 2 {
		t.Fatalf("来源身份或读取策略丢失: %+v", task)
	}
	projected, _ := json.Marshal(task)
	if strings.Contains(string(projected), secret) || !strings.Contains(task.ReadScope.Paths[0], "[REDACTED]") || !strings.Contains(task.Reading.UnresolvedPaths[0], "[REDACTED]") {
		t.Fatalf("资料配置或未解析目录泄漏凭据: %s", projected)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("投影修改了原始事件")
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
