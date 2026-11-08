package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func memoryV2Fixture(t *testing.T) (context.Context, *runState, []schema.Message) {
	t.Helper()
	ctx, r, messages := analysisContextFixture(t)
	a := r.state.Analysis
	r.state.Spec.Analysis, r.state.Spec.ReadContextVersion = false, 2
	r.state.ReadMemory = &ReadMemoryState{Version: 1, Evidence: a.Evidence, Notes: a.Reports, ProcessedReads: a.Processed, Summaries: a.Summaries}
	r.state.Analysis = nil
	return ctx, r, messages
}

func saveMemoryReport(t *testing.T, ctx context.Context, r *runState, report AnalysisReport) *AnalysisReport {
	t.Helper()
	raw, _ := json.Marshal(report)
	checked, err := r.acceptAnalysis(ctx, string(raw), false)
	if err != nil || checked == nil {
		t.Fatalf("保存读取笔记失败: %+v %v", checked, err)
	}
	return checked
}

func TestDeferredReadRetainsSourceUntilLaterValidExplicitProcessing(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1", "E2"})
	eng := NewAgentEngine(nil, nil, EngineOptions{})
	deferred := AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "需与第二份材料对照默认值"}}}
	saveMemoryReport(t, ctx, r, deferred)
	prepared, _, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 0 || prepared[3].Content != messages[3].Content || !strings.Contains(prepared[0].Content, deferred.DeferredReads[0].Reason) {
		t.Fatalf("暂缓丢失或错误折叠: %+v %v", stats, err)
	}
	if err := validateReadMemoryState(r.state); err != nil {
		t.Fatal(err)
	}
	// 有摘要仍没有显式处理，旧暂缓须持续保留。
	saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, Summaries: map[string]string{"E1": "已初步核对范围，尚需对照"}})
	if report := r.analysisReport(ctx); len(report.DeferredReads) != 1 || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 || r.state.ReadMemory.Summaries["root"]["E1"] == "" {
		t.Fatal("单独摘要清除了暂缓或授予处理资格")
	}
	// 无有效摘要的新处理声明不能解除暂缓。
	saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{"E1"}})
	if len(r.analysisReport(ctx).DeferredReads) != 1 {
		t.Fatal("无有效笔记的processed解除暂缓")
	}
	saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{"E1", "E2"}, Summaries: map[string]string{"E1": "已核对默认值，条件完整", "E2": "最新批次已检查"}})
	if len(r.analysisReport(ctx).DeferredReads) != 0 {
		t.Fatal("后续有效显式处理未解除暂缓")
	}
	prepared, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 1 || prepared[3].Content == messages[3].Content || prepared[5].Content != messages[5].Content {
		t.Fatalf("后续处理折叠门槛或latest保护错误: %+v %v", stats, err)
	}
	// 重新暂缓须撤销已存processed，不能依赖已有摘要继续折叠。
	saveMemoryReport(t, ctx, r, deferred)
	prepared, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 0 || prepared[3].Content != messages[3].Content || containsString(r.state.ReadMemory.ProcessedReads["root"], "E1") {
		t.Fatalf("重新暂缓未撤销折叠资格: %+v %v", stats, err)
	}
}

func TestDeferredReadRejectsForeignUndeliveredAndStaleEvidence(t *testing.T) {
	for _, scenario := range []string{"未交付", "其他Agent", "旧运行", "源已改变"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, _ := memoryV2Fixture(t)
			if scenario != "未交付" {
				r.markAnalysisDelivered(ctx, []string{"E1"})
			}
			x := r.analysisRuntime.sources["E1"]
			switch scenario {
			case "其他Agent":
				x.AgentID = "child"
				r.analysisRuntime.parents["child"] = "root"
				r.analysisRuntime.delivered["child"] = map[string]bool{"E1": true}
			case "旧运行":
				x.RunID = "older"
			case "源已改变":
				if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "E1.md"), []byte("新版"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.analysisRuntime.sources["E1"] = x
			checked := saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "等待对照"}}})
			if len(checked.DeferredReads) != 0 {
				t.Fatal("不可用来源获得暂缓投影资格")
			}
		})
	}
}

func TestDeferredReadPersistenceDoesNotRestoreCurrentQualification(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "需核实的持久暂缓理由"}}})
	raw, _ := json.Marshal(r.state)
	var restored taskState
	if json.Unmarshal(raw, &restored) != nil || validateReadMemoryState(restored) != nil || len(restored.ReadMemory.Notes["root"].DeferredReads) != 1 || restored.ReadMemory.Version != 1 {
		t.Fatal("暂缓状态没有按存储v1往返")
	}
	resumed := &runState{state: restored, runID: "resumed-run"}
	prepared, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, resumed, messages, nil)
	if err != nil || stats.folded != 0 || stats.unprocessed != 0 || strings.Contains(prepared[0].Content, "需核实的持久暂缓理由") {
		t.Fatalf("恢复自动复活旧来源资格: %+v %v", stats, err)
	}
	// 源版本变动后，旧暂缓保留于账本但不能继续进入currentnotes。
	if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "E1.md"), []byte("新版"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, _, _, err = NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || strings.Contains(prepared[0].Content, "需核实的持久暂缓理由") {
		t.Fatal("失效暂缓继续进入当前笔记")
	}
}

func TestReadContextNewDefaultsAndHistoricResumeRemainDistinct(t *testing.T) {
	for _, input := range []int{0, 1, 2} {
		t.Run(string(rune('0'+input)), func(t *testing.T) {
			dir := t.TempDir()
			store, err := taskstore.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			eng := NewAgentEngine(nil, nil, EngineOptions{Mode: tools.ModeWorkspace})
			task, state, err := eng.openTask(store, dir, "s", TaskSpec{ID: taskstore.NewID(), Goal: "新建", ReadContextVersion: input})
			if err != nil {
				t.Fatal(err)
			}
			want := input
			if want == 0 {
				want = 2
			}
			if state.Spec.ReadContextVersion != want {
				t.Fatalf("新建策略错误: %d", state.Spec.ReadContextVersion)
			}
			// 构造已有日志，检查旧0/1恢复时忽略新任务默认策略且不升级。
			state.Spec.ReadContextVersion = input
			if _, err = task.Append("created", state); err != nil {
				t.Fatal(err)
			}
			if err = task.SaveSnapshot(state); err != nil {
				t.Fatal(err)
			}
			if err = task.Close(); err != nil {
				t.Fatal(err)
			}
			opened, resumed, err := eng.openTask(store, dir, "s", TaskSpec{ID: state.Spec.ID, Resume: true})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			if resumed.Spec.ReadContextVersion != input {
				t.Fatalf("历史策略被升级: %d -> %d", input, resumed.Spec.ReadContextVersion)
			}
		})
	}
}

func TestDeferredStorageFailureDoesNotMarkAcceptedMessage(t *testing.T) {
	ctx, r, _ := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	r.persist = func(string, taskState) error { return errors.New("模拟存储失败") }
	text := `{"schema_version":1,"findings":[],"deferred_reads":[{"evidence_id":"E1","reason":"等待核实"}]}`
	_, err := r.acceptAnalysis(ctx, text, false)
	if !errors.Is(err, errStorage) || r.analysisRuntime.accepted[observe.Identity(ctx).AgentID][analysisMessageKey(text)] {
		t.Fatal("持久化失败仍标记为已接受")
	}
}

func TestV2RequestProjectionRemovesConsumedLatestCarrierAndKeepsSources(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	carrierMessages, call := carrierProjectionFixture(t)
	messages = append(messages, carrierMessages...)
	messages = append(messages, schema.Message{Role: schema.RoleUser, ToolCallID: call.ID, Content: "工具业务结果"})
	eng := NewAgentEngine(nil, nil, EngineOptions{})
	for _, consume := range []bool{false, true} {
		if consume {
			r.markWorkNotesCarrierConsumed(ctx, call)
		}
		prepared, _, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
		if err != nil {
			t.Fatal(err)
		}
		if stats.folded != 0 || prepared[3].Content != messages[3].Content || prepared[5].Content != messages[5].Content || prepared[7].Content != messages[7].Content {
			t.Fatal("元数据投影误改原文或工具结果")
		}
		if consume && (stats.notesRemoved <= 0 || stats.notesRetained != 0) || !consume && (stats.notesRemoved != 0 || stats.notesRetained <= 0) {
			t.Fatalf("清理统计错误: %+v", stats)
		}
		if !strings.Contains(prepared[0].Content, `"citation_location":"E1.md:1-2"`) || !strings.Contains(prepared[0].Content, "_work_notes") {
			t.Fatal("证据可复制定位或v2工具提示缺失")
		}
		if !bytesContainsMetadata(messages[6].ToolCalls[0].Arguments) {
			t.Fatal("投影改写原始Session")
		}
	}
}

func bytesContainsMetadata(raw []byte) bool { return strings.Contains(string(raw), `"_work_notes"`) }

func TestV2ReadingProgressUsesOnlyShortEvidenceIDs(t *testing.T) {
	ctx, r, _ := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	encoded, count := readingProcessingProgress(r.analysisRuntime.sources, r.analysisRuntime.delivered["root"], nil, nil, "root", true)
	var progress struct {
		Reads []map[string]json.RawMessage `json:"reads"`
	}
	if json.Unmarshal(encoded, &progress) != nil || count != 1 || len(progress.Reads) != 1 || len(progress.Reads[0]) != 1 || string(progress.Reads[0]["id"]) != `"E1"` {
		t.Fatalf("进度块重复证据位置: %s", encoded)
	}
}

func TestFinalAnalysisDoesNotSilentlyClearDeferredReads(t *testing.T) {
	ctx, r, _ := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	saveMemoryReport(t, ctx, r, AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E1", Reason: "待对照"}}})
	final := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "已确认的片段", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}}}
	raw, _ := json.Marshal(final)
	checked, err := r.acceptAnalysis(ctx, string(raw), true)
	if err != nil || checked == nil || len(checked.DeferredReads) != 1 {
		t.Fatalf("终稿省略字段意外清空暂缓: %+v %v", checked, err)
	}
}
