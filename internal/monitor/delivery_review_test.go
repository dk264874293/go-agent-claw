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

func TestDeliveryReviewProjectionRedactsBoundsAndDoesNotShareState(t *testing.T) {
	secret := "delivery-review-private-api-key-123456789"
	t.Setenv("DELIVERY_VIEW_API_KEY", secret)
	result := &engine.DeliveryReviewSummary{Version: 1, Status: "completed", AdmissionCount: 1, OriginRunID: "old-run", Documents: 5, Addressed: 1, NotRelevant: 1, Unverified: 1, Undeclared: 2, ReadyToResolve: 1, TriggerReasons: []string{secret}}
	for i := 0; i < 66; i++ {
		result.Unresolved = append(result.Unresolved, engine.DeliveryReviewIssue{ID: "U1", Kind: "source_unverified", Reason: "<script>" + secret, Path: "资料.md", EvidenceIDs: []string{"E1"}, FindingIDs: []string{"F1"}, CheckIDs: []string{"C1"}})
	}
	x, task := &Index{}, Task{}
	x.projectDeliveryReview(&task, result)
	raw, _ := json.Marshal(task.DeliveryReview)
	if strings.Contains(string(raw), secret) || task.DeliveryReview.Omitted != 2 || len(task.DeliveryReview.Unresolved) != 64 || task.DeliveryReview.Documents != 5 || !strings.Contains(task.DeliveryReview.Unresolved[0].Reason, "<script>") {
		t.Fatalf("交付前处理投影脱敏/限额错误：%s", raw)
	}
	task.DeliveryReview.Unresolved[0].EvidenceIDs[0] = "修改"
	task.DeliveryReview.TriggerReasons[0] = "修改"
	if result.Unresolved[0].EvidenceIDs[0] != "E1" || result.TriggerReasons[0] != secret {
		t.Fatal("观测台投影修改了引擎状态")
	}
}

func TestDeliveryReviewContentComparisonProjectsOnlyMetadata(t *testing.T) {
	secret := "comparison-private-api-key-123456789"
	t.Setenv("COMPARISON_VIEW_API_KEY", secret)
	result := &engine.DeliveryReviewSummary{Version: 1, Status: "completed", AdmissionCount: 1, ContentComparisons: []engine.DeliveryReviewContentComparison{{Kind: "file", Path: secret + ".md", Status: "unchanged", CandidateSHA256: strings.Repeat("a", 64), ConfirmedSHA256: strings.Repeat("a", 64), ActionID: "action"}}}
	x, task := &Index{}, Task{}
	x.projectDeliveryReview(&task, result)
	raw, _ := json.Marshal(task.DeliveryReview)
	if strings.Contains(string(raw), secret) || len(task.DeliveryReview.ContentComparisons) != 1 || task.DeliveryReview.ContentComparisons[0].Status != "unchanged" {
		t.Fatalf("正文比较未投影或路径未脱敏: %s", raw)
	}
	task.DeliveryReview.ContentComparisons[0].Status = "changed"
	if result.ContentComparisons[0].Status != "unchanged" {
		t.Fatal("正文比较视图改写了原状态")
	}
}

func deliveryReviewViewFixture(t *testing.T, work, id string) {
	t.Helper()
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ev taskstore.Event
	var state map[string]any
	if json.Unmarshal(raw, &ev) != nil || json.Unmarshal(ev.Payload, &state) != nil {
		t.Fatal("夹具无效")
	}
	state["version"] = 3
	spec := state["spec"].(map[string]any)
	spec["delivery_review"] = engine.DeliveryReviewSpec{Version: 1}
	spec["read_context_version"] = 2
	summary := engine.DeliveryReviewSummary{Version: 1, Status: "not_run", Documents: 2, Undeclared: 2, Unresolved: []engine.DeliveryReviewIssue{{ID: "U1", Kind: "undeclared", Path: "资料.md", EvidenceIDs: []string{"E1"}, Reason: "尚未声明处置"}}}
	state["delivery_review"] = map[string]any{"version": 1, "status": "not_run", "admission_count": 0, "summary": summary}
	state["read_memory"] = engine.ReadMemoryState{Version: 1}
	state["last_result"].(map[string]any)["delivery_review"] = summary
	ev.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(ev)
	clock, _ := json.Marshal(taskstore.Event{Version: 1, Sequence: 2, Kind: "elapsed", Payload: json.RawMessage(`{"version":3,"elapsed":2000000000}`)})
	data := append(append(raw, '\n'), append(clock, '\n')...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorDeliveryReviewStateVersionAndAttemptBoundaries(t *testing.T) {
	for _, boundary := range []string{"history", "started", "mismatch", "match"} {
		t.Run(boundary, func(t *testing.T) {
			work, observations, id := t.TempDir(), t.TempDir(), strings.Repeat("a", 32)
			deliveryReviewViewFixture(t, work, id)
			if boundary != "history" {
				payloads := []map[string]any{{"event_type": "run_attempt_started"}}
				if boundary != "started" {
					run := "old-run"
					if boundary == "mismatch" {
						run = "new-run"
					}
					payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "BUDGET_EXHAUSTED", "stop_code": "model_limit", "result_run_id": run})
				}
				regressionObservation(t, observations, strings.Repeat("b", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
			}
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: observations})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if task.RecordQuality == "corrupt" || task.Usage.ModelCalls != 8 || task.Usage.ToolCalls != 3 || task.Usage.ElapsedMS != 2000 {
				t.Fatalf("版本3读取造成错误或零计数：%+v", task)
			}
			if (task.DeliveryReview != nil) != (boundary == "history" || boundary == "match") {
				t.Fatalf("旧处理摘要污染新尝试：%s %+v", boundary, task.DeliveryReview)
			}
		})
	}
}

func TestDeliveryReviewPolicy2CandidateArtifactsArePrivate(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, id, candidate := t.TempDir(), strings.Repeat("c", 32), strings.Repeat("d", 32)
	deliveryReviewViewFixture(t, work, id)
	dir := filepath.Join(work, ".claw/tasks", id, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, candidate), []byte("完整候选答复及工具意图"), 0600); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	x.addWorkspace(work)
	ref := x.addEvidence(work, ".claw/tasks/"+id+"/artifacts/"+candidate, "此前已登记的候选ID")
	x.mu.Unlock()
	x.Refresh()
	task := x.Tasks(observe.WorkspaceID(work), "", "")[0]
	if task.DeliveryReview == nil || len(task.Evidence) != 0 {
		t.Fatalf("策略2候选泄露或处理摘要缺失：%+v", task)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/evidence/"+ref.ID, nil)
	req.Header.Set("Cookie", cookie)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("旧映射暴露候选：%d", response.StatusCode)
	}
}

func TestDeliveryReviewPolicy2OnlyAllowsCommittedOriginalSources(t *testing.T) {
	root, taskID, sourceID, candidateID := t.TempDir(), strings.Repeat("e", 32), strings.Repeat("a", 32), strings.Repeat("b", 32)
	x := &Index{sources: map[string]*taskSource{}}
	key := stableID(observe.WorkspaceID(root) + "/" + taskID)
	x.sources[key] = &taskSource{view: engine.Inspection{Sequence: 1, Spec: engine.TaskSpec{ReadContextVersion: 2, DeliveryReview: &engine.DeliveryReviewSpec{Version: 1}}, ReadMemory: &engine.ReadMemoryState{Version: 1, Evidence: map[string]engine.AnalysisEvidence{"E1": {ID: "E1", ActionID: "action", ArtifactID: sourceID}}}}}
	if !x.localCheckArtifactVisible(root, taskID, sourceID) || x.localCheckArtifactVisible(root, taskID, candidateID) {
		t.Fatal("原文白名单和候选隔离错误")
	}
}
