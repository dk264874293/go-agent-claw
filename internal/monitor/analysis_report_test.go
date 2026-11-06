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

func analysisReportFixture(t *testing.T, work, id, secret string) {
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
	result["status"], result["stop_code"] = "ANSWERED", "answered"
	result["final_text"] = "历史分析报告，只属于 old-run。"
	result["analysis"] = map[string]any{
		"schema_version": 1,
		"findings": []any{map[string]any{
			"id": "f1", "kind": "fact", "statement": "材料事实" + secret, "context": "版本背景" + secret,
			"limitations": "尚未执行脚本" + secret, "status": "evidence_valid",
			"evidence": []any{map[string]any{"evidence_id": "E1", "path": "docs/rules.md", "version": "version", "start_line": 2, "end_line": 2, "quote": "摘录" + secret}},
		}},
		"unverified": []string{"待实测" + secret},
	}
	event.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(event)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAnalysisReportProjectionRedactsAllStructuredText(t *testing.T) {
	work, id := t.TempDir(), strings.Repeat("d", 32)
	secret := "analysis-report-private-credential-123456789"
	t.Setenv("MONITOR_ANALYSIS_TEST_API_KEY", secret)
	analysisReportFixture(t, work, id, secret)
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.Analysis == nil || len(task.Analysis.Findings) != 1 || task.EngineStatus != "ANSWERED" || task.Verification != nil {
		t.Fatalf("引用报告丢失或被当成引擎验收: %+v", task)
	}
	raw, _ := json.Marshal(task.Analysis)
	if strings.Contains(string(raw), secret) || strings.Count(string(raw), "[REDACTED]") < 5 {
		t.Fatalf("结构化引用字段未完整脱敏: %s", raw)
	}
}

func TestAnalysisReportProjectionDoesNotLeakAcrossAttempts(t *testing.T) {
	for _, boundary := range []string{"attempt_started", "returned_mismatch", "returned_match"} {
		t.Run(boundary, func(t *testing.T) {
			work, root, id := t.TempDir(), t.TempDir(), strings.Repeat("e", 32)
			analysisReportFixture(t, work, id, "")
			payloads := []map[string]any{{"event_type": "run_attempt_started"}}
			if strings.HasPrefix(boundary, "returned_") {
				runID := "old-run"
				if boundary == "returned_mismatch" {
					runID = "new-run"
				}
				payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "ANSWERED", "stop_code": "answered", "result_run_id": runID})
			}
			regressionObservation(t, root, strings.Repeat("f", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if (task.Analysis != nil) != (boundary == "returned_match") {
				t.Fatalf("本次运行错误地使用旧报告: boundary=%s analysis=%+v", boundary, task.Analysis)
			}
			if (task.FinalText != "") != (boundary == "returned_match") {
				t.Fatalf("结构化报告隐藏后仍展示旧运行正文: boundary=%s final=%s", boundary, task.FinalText)
			}
		})
	}
}
