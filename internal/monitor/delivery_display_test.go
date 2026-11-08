package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func TestFinalTextDisplayKeepsRawAndTrustedArtifactEvidence(t *testing.T) {
	work, tid := t.TempDir(), strings.Repeat("a", 32)
	fixture(t, work, tid)
	path := filepath.Join(work, ".claw/tasks", tid, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event taskstore.Event
	var state map[string]any
	if json.Unmarshal(raw, &event) != nil || json.Unmarshal(event.Payload, &state) != nil {
		t.Fatal("任务夹具无效")
	}
	content := []byte("确认保存的文档")
	sum := sha256.Sum256(content)
	version := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(work, "汇总.md"), content, 0600); err != nil {
		t.Fatal(err)
	}
	text := "已生成 [汇总.md](/wrong/工作 区/汇总.md)。"
	delivery := &engine.Delivery{HistoryComplete: true, Effects: []engine.EffectReceipt{{
		ActionID: "write", Tool: "write_file", Status: schema.ToolSucceeded, Effect: schema.EffectConfirmed,
		File: &engine.FileEffect{Path: "汇总.md", BeforeVersion: "missing", AfterVersion: version},
	}}}
	state["effect_ledger"] = delivery
	state["last_result"] = engine.RunResult{
		TaskID: tid, RunID: "old-run", Status: engine.NeedsVerification, FinalText: text, Delivery: delivery,
		Artifacts: []engine.ArtifactRef{{ActionID: "write", Path: "汇总.md", Version: version}},
	}
	event.Payload, _ = json.Marshal(state)
	raw, _ = json.Marshal(event)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	observationRoot := t.TempDir()
	index, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: observationRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	for i := 0; i < 2; i++ {
		task := index.Tasks("", "", "")[0]
		if task.FinalText != text || task.FinalTextDisplay != "已生成 汇总.md。" {
			t.Fatalf("原文或展示投影错误：raw=%q display=%q", task.FinalText, task.FinalTextDisplay)
		}
		if len(task.Artifacts) != 1 || task.Artifacts[0].EvidenceID != stableID(canonical(work)+"\x00汇总.md\x00"+version) {
			t.Fatalf("可信产物ID被展示处理改变：%+v", task.Artifacts)
		}
		index.Refresh()
	}
	regressionObservation(t, observationRoot, strings.Repeat("c", 32), observe.WorkspaceID(work), tid, time.Now(),
		map[string]any{"event_type": "run_attempt_started"},
		map[string]any{"event_type": "run_returned", "status": "ANSWERED", "result_run_id": "different-run"})
	index.Refresh()
	if task := index.Tasks("", "", "")[0]; task.FinalText != "" || task.FinalTextDisplay != "" {
		t.Fatal("不匹配尝试边界仍展示历史终答")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(append(raw, '\n')) {
		t.Fatal("纯展示投影修改了源记录")
	}
}
