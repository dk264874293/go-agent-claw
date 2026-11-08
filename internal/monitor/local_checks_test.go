package monitor

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func TestLocalChecksProjectionOnlyMetadataAndNoSharedMutation(t *testing.T) {
	secret := "local-private-api-key-12345678"
	t.Setenv("LOCAL_CHECK_API_KEY", secret)
	x, task := &Index{}, Task{}
	checks := []engine.LocalCheckSummary{{ID: "check", OriginRunID: "run", ReceiptArtifactID: "private-receipt", Result: "fail", PremiseResult: "pass", Reason: "<script>" + secret}}
	x.projectLocalChecks(&task, checks)
	raw, _ := json.Marshal(task.LocalChecks)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-receipt") || task.LocalChecks[0].Result != "fail" {
		t.Fatalf("局部核对正文或凭据泄露: %s", raw)
	}
	task.LocalChecks[0].Reason = "展示修改"
	if checks[0].Reason != "<script>"+secret {
		t.Fatal("展示修改了持久核对记录")
	}
}

func TestLocalCheckPreparedArtifactsHiddenIncludingStaleIDs(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, id, artifact := t.TempDir(), strings.Repeat("a", 32), strings.Repeat("b", 32)
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ev taskstore.Event
	var state map[string]any
	if json.Unmarshal(raw, &ev) != nil || json.Unmarshal(ev.Payload, &state) != nil {
		t.Fatal("测试事件无效")
	}
	checks := []engine.LocalCheckSummary{{ID: "check", OriginRunID: "old-run", ReceiptArtifactID: artifact, Kind: "json", Result: "not_run", PremiseResult: "not_run", Reason: "receipt_not_confirmed"}}
	state["local_checks"] = checks
	state["last_result"].(map[string]any)["local_checks"] = checks
	ev.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(ev)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, ".claw/tasks", id, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifact), []byte("private checker expression and inputs"), 0600); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	x.addWorkspace(work)
	ref := x.addEvidence(work, ".claw/tasks/"+id+"/artifacts/"+artifact, "工具证据 "+artifact)
	x.mu.Unlock()
	x.Refresh()
	task := x.Tasks("", "", "")[0]
	if len(task.LocalChecks) != 1 || task.LocalChecks[0].Result != "not_run" {
		t.Fatalf("待确认核对未保留: %+v", task.LocalChecks)
	}
	for _, e := range task.Evidence {
		if e.ID == ref.ID {
			t.Fatal("核对材料被枚举")
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/evidence/"+ref.ID, nil)
	req.Header.Set("Cookie", cookie)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("陈旧证据ID暴露私有核对材料: %d", response.StatusCode)
	}
}

func TestLocalCheckPrivateArtifactFailsClosedBeforeIndexRefresh(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, id, artifact := t.TempDir(), strings.Repeat("d", 32), strings.Repeat("e", 32)
	fixture(t, work, id)
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ev taskstore.Event
	var state map[string]any
	if json.Unmarshal(raw, &ev) != nil || json.Unmarshal(ev.Payload, &state) != nil {
		t.Fatal("测试状态无效")
	}
	state["spec"].(map[string]any)["read_context_version"] = 3
	ev.Payload, _ = json.Marshal(state)
	first, _ := json.Marshal(ev)
	if err := os.WriteFile(path, append(first, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	x.addWorkspace(work)
	ref := x.addEvidence(work, ".claw/tasks/"+id+"/artifacts/"+artifact, "陈旧材料ID")
	x.mu.Unlock()
	x.Refresh()
	// Index仍是旧状态：执行进程随后登记prepared并写入文件，不触发Refresh。
	state["local_checks"] = []engine.LocalCheckSummary{{ID: "K1", ReceiptArtifactID: artifact, Result: "not_run", Reason: "receipt_not_confirmed"}}
	ev.Sequence++
	ev.Kind = "local_checks_prepared"
	ev.Payload, _ = json.Marshal(state)
	next, _ := json.Marshal(ev)
	if err := os.WriteFile(path, append(append(first, '\n'), append(next, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, ".claw/tasks", id, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifact), []byte("private checker input"), 0600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/evidence/"+ref.ID, nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("旧索引在prepared窗口泄露核对材料: %d", resp.StatusCode)
	}
}
