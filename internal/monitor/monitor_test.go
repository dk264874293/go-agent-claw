package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func fixture(t testing.TB, work, id string) {
	t.Helper()
	dir := filepath.Join(work, ".claw/tasks", id)
	os.MkdirAll(dir, 0700)
	state := map[string]any{"version": 1, "spec": map[string]any{"id": id, "goal": "检查任务", "kind": "change", "mode": "workspace"}, "limits": map[string]any{"MaxModelCalls": 8, "MaxToolCalls": 20}, "usage": map[string]any{"model_calls": 8, "tool_calls": 3}, "effect_ledger": map[string]any{"history_complete": true, "effects": []any{map[string]any{"action_id": "x", "tool": "bash", "status": "SUCCEEDED", "effect": "UNKNOWN"}}}, "last_result": map[string]any{"task_id": id, "run_id": "old-run", "status": "BUDGET_EXHAUSTED", "stop_code": "model_limit"}}
	raw, _ := json.Marshal(state)
	ev, _ := json.Marshal(taskstore.Event{Version: 1, Sequence: 1, Kind: "finished", Payload: raw})
	os.WriteFile(filepath.Join(dir, "events.jsonl"), append(ev, '\n'), 0600)
}
func TestIndexLegacyEffectsAndReadOnly(t *testing.T) {
	work := t.TempDir()
	id := strings.Repeat("a", 32)
	fixture(t, work, id)
	log := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	before, _ := os.ReadFile(log)
	x, e := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	tasks := x.Tasks("", "", "")
	if len(tasks) != 1 || tasks[0].EngineStatus != "BUDGET_EXHAUSTED" || len(tasks[0].Effects) != 1 {
		t.Fatalf("bad task %+v", tasks)
	}
	found := false
	for _, a := range tasks[0].Alerts {
		if a.Code == "unknown_effect" {
			found = true
		}
	}
	if !found {
		t.Fatal("成功Bash的未知效果消失")
	}
	for i := 0; i < 3; i++ {
		x.Refresh()
	}
	after, _ := os.ReadFile(log)
	if string(before) != string(after) {
		t.Fatal("索引修改源文件")
	}
	if len(x.Tasks("", "", "")[0].Effects) != 1 {
		t.Fatal("效果被重复累加")
	}
}
func TestObservationConcurrencyUsageAndGap(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	id := strings.Repeat("b", 32)
	fixture(t, work, id)
	attempt := strings.Repeat("c", 32)
	dir := filepath.Join(root, attempt)
	os.MkdirAll(dir, 0700)
	f, _ := os.Create(filepath.Join(dir, "events.jsonl"))
	enc := json.NewEncoder(f)
	now := time.Now()
	seq := uint64(0)
	emit := func(kind, model string, p map[string]any) {
		seq++
		enc.Encode(Event{SchemaVersion: 1, AttemptID: attempt, Sequence: seq, RecordedAt: &now, EventType: kind, WorkspaceID: observe.WorkspaceID(work), TaskID: id, AgentID: model, ModelCallID: model, Payload: p})
	}
	emit("run_attempt_started", "", map[string]any{"entry": "test"})
	emit("model_started", "m1", nil)
	emit("model_started", "m2", nil)
	emit("model_usage", "m1", map[string]any{"input_tokens": float64(2), "output_tokens": float64(3)})
	emit("model_usage", "m1", map[string]any{"input_tokens": float64(2), "output_tokens": float64(3)})
	f.Close()
	x, _ := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
	defer x.Close()
	a, ok := x.Attempt(attempt)
	if !ok || len(a.Activities) != 2 || a.Usage.KnownInput != 2 || a.Usage.UnknownTokens != 1 || a.Usage.InputTokens != nil {
		t.Fatalf("bad projection %+v", a)
	}
	f, _ = os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	enc = json.NewEncoder(f)
	seq++
	emit("run_returned", "", map[string]any{"status": "FAILED", "stop_code": "storage_error"})
	f.Close()
	x.Refresh()
	a, _ = x.Attempt(attempt)
	if a.RecordQuality != "incomplete" || a.Activity != "returned" {
		t.Fatalf("gap hidden %+v", a)
	}
}
func authenticated(t *testing.T) (*Index, *Server, *httptest.Server, string) {
	t.Helper()
	x, _ := NewIndex(Config{Workspaces: []string{t.TempDir()}, ObserveDir: t.TempDir()})
	s := NewServer(x, "")
	hs := httptest.NewUnstartedServer(s)
	s.host = hs.Listener.Addr().String()
	hs.Start()
	t.Cleanup(func() { hs.Close(); x.Close() })
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/session", strings.NewReader(`{"token":"`+s.token+`"}`))
	req.Header.Set("Origin", hs.URL)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	return x, s, hs, resp.Cookies()[0].String()
}
func TestHTTPAccessIsolationAndEvidence(t *testing.T) {
	x, _, hs, cookie := authenticated(t)
	resp, _ := http.Get(hs.URL + "/api/v1/tasks")
	if resp.StatusCode != 401 {
		t.Fatal("unauth", resp.Status)
	}
	resp.Body.Close()
	request := func(path, origin string) *http.Response {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Cookie", cookie)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := request("/api/v1/tasks", "https://evil.example")
	if r.StatusCode != 403 {
		t.Fatal("cross origin allowed")
	}
	r.Body.Close()
	r = request("/api/v1/tasks", "")
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
	r.Body.Close()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "test"), []byte("你好<script>alert(1)</script> sk-abcdefghijklmnopqrstuvw"), 0600)
	x.mu.Lock()
	ev := x.addEvidence(root, "test", "证据")
	x.mu.Unlock()
	r = request("/api/v1/evidence/"+ev.ID, "")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if strings.Contains(string(b), "sk-abcdefghijkl") || strings.Contains(string(b), "<script>") {
		t.Fatal("secret or unsafe JSON", string(b))
	}
	os.Symlink(filepath.Join(t.TempDir(), "secret"), filepath.Join(root, "outside"))
	x.mu.Lock()
	ev = x.addEvidence(root, "outside", "bad")
	x.mu.Unlock()
	r = request("/api/v1/evidence/"+ev.ID, "")
	if r.StatusCode != 404 {
		t.Fatal("escaped evidence")
	}
	r.Body.Close()
}
func TestSSEResetAndReplay(t *testing.T) {
	_, s, hs, cookie := authenticated(t)
	s.Notify()
	s.Notify()
	get := func(last string) string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", hs.URL+"/api/v1/stream", nil)
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Last-Event-ID", last)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return string(buf[:n])
	}
	if !strings.Contains(get("old:1"), "event: reset") {
		t.Fatal("missing reset")
	}
	if !strings.Contains(get(s.instance+":1"), "event: change") {
		t.Fatal("missing replay")
	}
	for i := 0; i < 1025; i++ {
		s.Notify()
	}
	if !strings.Contains(get(s.instance+":1"), "event: reset") {
		t.Fatal("expired cursor not reset")
	}
}
func TestRealEvaluationImport(t *testing.T) {
	base := os.Getenv("CLAW_MONITOR_EVAL_FIXTURE")
	if base == "" {
		t.Skip("真实样本由显式离线验收传入")
	}
	x, e := NewIndex(Config{EvalRoots: []string{base}, ObserveDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	ev := x.Evaluations()
	if len(ev) != 30 || len(x.Tasks("", "", "")) != 30 {
		t.Fatalf("样本未完整导入 %d %d", len(ev), len(x.Tasks("", "", "")))
	}
	pass := map[string]int{}
	for _, v := range ev {
		if v.CombinedPass {
			pass[v.Arm]++
		}
		task, ok := x.Task(stableID(observe.WorkspaceID(findWorkspace(x, v.TaskID)) + "/" + v.TaskID))
		if !ok || task.EngineStatus != v.EngineStatus {
			t.Fatal("引擎结果不一致", v.ID)
		}
	}
	if pass["baseline"] != 8 || pass["candidate"] != 15 {
		t.Fatal(pass)
	}
}
func findWorkspace(x *Index, task string) string {
	for _, t := range x.Tasks("", "", "") {
		if t.TaskID == task {
			return x.workspaces[t.WorkspaceID].Path
		}
	}
	return ""
}
func TestLargeIndexPerformance(t *testing.T) {
	if os.Getenv("CLAW_MONITOR_PERF") == "" {
		t.Skip("性能验收显式开启")
	}
	root := t.TempDir()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%032x", i+1)
		fixture(t, root, id)
		path := filepath.Join(root, ".claw/tasks", id, "events.jsonl")
		base, _ := os.ReadFile(path)
		var ev taskstore.Event
		json.Unmarshal(base, &ev)
		f, _ := os.Create(path)
		enc := json.NewEncoder(f)
		for n := 1; n <= 100; n++ {
			ev.Sequence = uint64(n)
			enc.Encode(ev)
		}
		f.Close()
	}
	start := time.Now()
	x, _ := NewIndex(Config{Workspaces: []string{root}, ObserveDir: t.TempDir()})
	defer x.Close()
	t.Log("1000任务/100000事件，初始索引", time.Since(start))
	times := []time.Duration{}
	for i := 0; i < 100; i++ {
		start = time.Now()
		list := x.Tasks("", "", "")
		if len(list) != 1000 {
			t.Fatal(len(list))
		}
		json.Marshal(list[:50])
		times = append(times, time.Since(start))
	}
	for i := range times {
		for j := i + 1; j < len(times); j++ {
			if times[j] < times[i] {
				times[i], times[j] = times[j], times[i]
			}
		}
	}
	t.Log("列表P95", times[94])
	if times[94] > 200*time.Millisecond {
		t.Fatal("列表P95超标")
	}
	_ = url.PathEscape
}
