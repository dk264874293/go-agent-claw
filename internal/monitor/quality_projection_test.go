package monitor

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func TestQualityProjectionIsBoundedAndDoesNotShareState(t *testing.T) {
	secret := "quality-private-api-key-123456789"
	t.Setenv("QUALITY_VIEW_API_KEY", secret)
	result := &engine.QualityDelivery{Version: 1, DraftID: "ticket", Status: "blocked", CheckResult: "partial", ReviewStatus: "completed", RevisionStatus: "admitted", Unresolved: []string{"<script>" + secret}, EvidenceIDs: []string{"private-artifact"}}
	x, task := &Index{}, Task{}
	x.projectQuality(&task, result)
	raw, _ := json.Marshal(task.Quality)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-artifact") || !strings.Contains(task.Quality.Unresolved[0], "<script>") {
		t.Fatalf("质量投影泄露材料身份或脱敏错误: %s", raw)
	}
	task.Quality.Unresolved[0] = "展示变更"
	if result.Unresolved[0] != "<script>"+secret {
		t.Fatal("展示投影修改了引擎状态")
	}
}

func qualityViewFixture(t *testing.T, work, id string) {
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
	state["version"] = 2
	spec := state["spec"].(map[string]any)
	spec["quality"] = map[string]any{"version": 1}
	spec["analysis"], spec["kind"], spec["mode"], spec["delegation"] = true, "answer", "readonly", "off"
	state["quality"] = map[string]any{"version": 1, "status": "exploring", "check_result": "not_run", "review_status": "not_run", "revision_status": "not_run"}
	state["last_result"].(map[string]any)["quality"] = state["quality"]
	event.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(event)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorQualityVersionAndAttemptBoundaries(t *testing.T) {
	for _, boundary := range []string{"history", "started", "mismatch", "match"} {
		t.Run(boundary, func(t *testing.T) {
			work, root, id := t.TempDir(), t.TempDir(), strings.Repeat("c", 32)
			qualityViewFixture(t, work, id)
			if boundary != "history" {
				payloads := []map[string]any{{"event_type": "run_attempt_started"}}
				if boundary != "started" {
					run := "old-run"
					if boundary == "mismatch" {
						run = "new-run"
					}
					payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "BUDGET_EXHAUSTED", "stop_code": "model_limit", "result_run_id": run})
				}
				regressionObservation(t, root, strings.Repeat("d", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
			}
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if task.Usage.ModelCalls != 8 || task.Usage.ToolCalls != 3 || task.RecordQuality == "corrupt" {
				t.Fatalf("质量状态未正确解释造成零计数: %+v", task)
			}
			if (task.Quality != nil) != (boundary == "history" || boundary == "match") {
				t.Fatalf("历史质量结果污染当前运行: %s %+v", boundary, task.Quality)
			}
		})
	}
}

func TestQualityTaskArtifactsAreNotExposedByEnumerationOrStaleID(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, id, artifact := t.TempDir(), strings.Repeat("a", 32), strings.Repeat("b", 32)
	qualityViewFixture(t, work, id)
	dir := filepath.Join(work, ".claw/tasks", id, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifact), []byte("private quality review inputs"), 0600); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	x.addWorkspace(work)
	// 模拟此前刷新已登记的ID；请求时必须再次检查当前质量策略。
	ref := x.addEvidence(work, ".claw/tasks/"+id+"/artifacts/"+artifact, "工具证据 "+artifact)
	x.mu.Unlock()
	x.Refresh()
	task := x.Tasks(observe.WorkspaceID(work), "", "")[0]
	if len(task.Evidence) != 0 || task.Quality == nil {
		t.Fatalf("质量私有材料被枚举或状态丢失: %+v", task)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/evidence/"+ref.ID, nil)
	req.Header.Set("Cookie", cookie)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("陈旧证据ID暴露了质量正文: %d", response.StatusCode)
	}
}
