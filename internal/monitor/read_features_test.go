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

func TestReadFeaturesV4ProjectionAndUnknownMaterialFailClosed(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, id, artifact := t.TempDir(), strings.Repeat("a", 32), strings.Repeat("f", 32)
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
	spec := state["spec"].(map[string]any)
	spec["read_context_version"] = 4
	spec["read_features"] = engine.ReadFeatureSpec{Version: 1, ClaimFocus: true}
	state["read_memory"] = engine.ReadMemoryState{Version: 1}
	ev.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(ev)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, ".claw/tasks", id, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifact), []byte("未登记的完整核对材料"), 0600); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	x.addWorkspace(work)
	ref := x.addEvidence(work, ".claw/tasks/"+id+"/artifacts/"+artifact, "陈旧证据ID")
	x.mu.Unlock()
	x.Refresh()
	tasks := x.Tasks("", "", "")
	var task Task
	for _, v := range tasks {
		if v.TaskID == id {
			task = v
		}
	}
	if task.ReadContextVersion != 4 || task.ReadFeatures == nil || !task.ReadFeatures.ClaimFocus || task.ReadFeatures.LocalChecks || task.Usage.ModelCalls != 8 || task.RecordQuality == "corrupt" {
		t.Fatalf("协议4展示或用量解析错误: %+v", task)
	}
	for _, v := range task.Evidence {
		if v.ID == ref.ID {
			t.Fatal("关闭核对开关重开了未知材料枚举")
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/evidence/"+ref.ID, nil)
	req.Header.Set("Cookie", cookie)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("策略4未知材料未失败关闭: %d", response.StatusCode)
	}
}
