package monitor

import (
	"crypto/sha256"
	"encoding/hex"
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

func writeReportFixture(t *testing.T, work, id, secret string) {
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
	spec := state["spec"].(map[string]any)
	spec["read_page_bytes"] = 16000
	spec["report"] = map[string]any{"version": 1, "format": "evidence_v1", "target_path": "report.md", "create_only": true}
	result := state["last_result"].(map[string]any)
	result["status"], result["stop_code"] = "NEEDS_VERIFICATION", "needs_verification"
	result["report"] = map[string]any{"report_id": strings.Repeat("a", 32), "origin_run_id": "old-run", "status": "published", "target_path": "report.md", "text": "不可信报告<script>" + secret, "issues": []string{"待实测" + secret}, "reference_result": "pass", "semantic_result": "not_run"}
	event.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(event)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceReportProjectionRedactsAndSeparatesVerification(t *testing.T) {
	work, id := t.TempDir(), strings.Repeat("b", 32)
	secret := "report-private-credential-123456789"
	t.Setenv("REPORT_VIEW_API_KEY", secret)
	writeReportFixture(t, work, id, secret)
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	task := x.Tasks("", "", "")[0]
	if task.Report == nil || task.Report.SemanticResult != "not_run" || task.Verification != nil || task.ReadPageBytes != 16000 {
		t.Fatalf("报告状态被误标或档位丢失: %+v", task)
	}
	raw, _ := json.Marshal(task.Report)
	if strings.Contains(string(raw), secret) || !strings.Contains(task.Report.Text, "<script>") {
		t.Fatalf("脱敏或纯文本投影错误: %s", raw)
	}
}

func TestEvidenceReportHiddenAcrossAttemptBoundaries(t *testing.T) {
	for _, boundary := range []string{"started", "mismatch", "match"} {
		t.Run(boundary, func(t *testing.T) {
			work, root, id := t.TempDir(), t.TempDir(), strings.Repeat("c", 32)
			writeReportFixture(t, work, id, "")
			payloads := []map[string]any{{"event_type": "run_attempt_started"}}
			if boundary != "started" {
				run := "old-run"
				if boundary == "mismatch" {
					run = "new-run"
				}
				payloads = append(payloads, map[string]any{"event_type": "run_returned", "status": "NEEDS_VERIFICATION", "stop_code": "needs_verification", "result_run_id": run})
			}
			regressionObservation(t, root, strings.Repeat("d", 32), observe.WorkspaceID(work), id, time.Now(), payloads...)
			x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			task := x.Tasks("", "", "")[0]
			if (task.Report != nil) != (boundary == "match") {
				t.Fatalf("旧报告污染本次运行: %s %+v", boundary, task.Report)
			}
		})
	}
}

func TestReadMemoryViewContainsCountsWithoutNotes(t *testing.T) {
	var task Task
	x := &Index{}
	x.projectReadMemory(&task, &engine.ReadMemoryState{Version: 1, Notes: map[string]engine.AnalysisReport{"agent": {Findings: []engine.AnalysisFinding{{Statement: "不能复制到观测流的业务正文"}}}}, ProcessedReads: map[string][]string{"agent": {"E1"}}, Invalidations: []engine.ReadInvalidation{{Reason: "版本变化"}}})
	raw, _ := json.Marshal(task.ReadMemory)
	if task.ReadMemory.NoteCount != 1 || task.ReadMemory.Processed != 1 || task.ReadMemory.Invalidations != 1 || strings.Contains(string(raw), "业务正文") {
		t.Fatalf("读取记忆摘要投影错误: %s", raw)
	}
}

func TestArtifactEvidenceChecksVersionAndRoot(t *testing.T) {
	x, _, hs, cookie := authenticated(t)
	root := t.TempDir()
	content := []byte("已确认产物\n")
	sum := sha256.Sum256(content)
	version := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(root, "report.md"), content, 0600); err != nil {
		t.Fatal(err)
	}
	var task Task
	x.mu.Lock()
	x.projectArtifact(&task, root, engine.ArtifactRef{ActionID: strings.Repeat("a", 32), Path: "report.md", Version: version})
	x.mu.Unlock()
	if len(task.Artifacts) != 1 || task.Artifacts[0].EvidenceID == "" {
		t.Fatalf("没有受控产物引用: %+v", task)
	}
	request := func(id string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/api/v1/evidence/"+id, nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	id := task.Artifacts[0].EvidenceID
	if status := request(id); status != 200 {
		t.Fatalf("有效产物不可读: %d", status)
	}
	os.WriteFile(filepath.Join(root, "report.md"), []byte("已经变化"), 0600)
	if status := request(id); status != 409 {
		t.Fatalf("改后内容冒充历史产物: %d", status)
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, content, 0600)
	os.Remove(filepath.Join(root, "report.md"))
	os.Symlink(outside, filepath.Join(root, "report.md"))
	if status := request(id); status != 404 {
		t.Fatalf("产物符号链接越界: %d", status)
	}
	x.mu.Lock()
	x.projectArtifact(&task, root, engine.ArtifactRef{Path: "../secret.md", Version: version})
	x.projectArtifact(&task, root, engine.ArtifactRef{Path: "/tmp/secret.md", Version: version})
	x.mu.Unlock()
	if len(task.Artifacts) != 1 {
		t.Fatalf("接受了越界路径: %+v", task.Artifacts)
	}
}
