package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func analysisContextFixture(t *testing.T) (context.Context, *runState, []schema.Message) {
	t.Helper()
	dir := t.TempDir()
	ctx := tools.WithScope(observe.WithAgent(context.Background(), "root", ""), tools.ExecutionScope{WorkDir: dir, Mode: tools.ModeReadOnly})
	r := &runState{state: taskState{Version: 1, Spec: TaskSpec{Analysis: true, Kind: TaskAnswer, Mode: tools.ModeReadOnly, Goal: "分析材料"}, Limits: Budget{}.defaults()}, runID: "run", started: time.Now()}
	r.analysisRuntime = analysisRuntime{sources: make(map[string]AnalysisEvidence), delivered: map[string]map[string]bool{"root": {}}, parents: map[string]string{"root": ""}}
	messages := []schema.Message{{Role: schema.RoleSystem, Content: "任务约束"}, {Role: schema.RoleUser, Content: "分析材料"}}
	for _, id := range []string{"E1", "E2"} {
		body := []byte(id + " 决定性事实\n" + strings.Repeat("材料原文。", 500))
		path := id + ".md"
		if err := os.WriteFile(filepath.Join(dir, path), body, 0600); err != nil {
			t.Fatal(err)
		}
		version, err := tools.FileVersion(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		read := schema.ReadReceipt{Path: path, Version: version, ByteEnd: int64(len(body)), FirstLine: 1, LastLine: 2, TotalBytes: int64(len(body)), AgentID: "root", ActionID: "action" + id, EvidenceID: id, ArtifactID: "artifact" + id}
		x := AnalysisEvidence{ID: id, ActionID: read.ActionID, AgentID: "root", RunID: "run", Receipt: read, ArtifactID: read.ArtifactID, Source: body}
		r.analysisRuntime.sources[id] = x
		ensureAnalysisState(&r.state).Evidence[id] = x
		messages = append(messages, schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "call" + id, Name: "read_file", Arguments: json.RawMessage(`{"path":"` + path + `"}`)}}}, schema.Message{Role: schema.RoleUser, ToolCallID: "call" + id, Content: string(body), ReadEvidenceID: id})
	}
	return ctx, r, messages
}

func TestAnalysisProjectionProtectsUnprocessedAndLatestBatch(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	eng := NewAgentEngine(nil, nil, EngineOptions{})
	for _, processed := range []bool{false, true} {
		t.Run(map[bool]string{false: "尚未处理", true: "有来源摘要"}[processed], func(t *testing.T) {
			if processed {
				r.state.Analysis.Processed["root"] = []string{"E1", "E2"}
				r.state.Analysis.Summaries["root"] = map[string]string{"E1": "已检查全文，保留决定性事实", "E2": "最近读取"}
				r.analysisRuntime.delivered["root"]["E1"] = true
				r.analysisRuntime.delivered["root"]["E2"] = true
			}
			prepared, delivered, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (prepared[3].Content != messages[3].Content) != processed {
				t.Fatal("未处理材料被折叠，或已处理副本没有移除")
			}
			if prepared[5].Content != messages[5].Content || !containsString(delivered, "E2") {
				t.Fatal("最新完整工具批次没有保留")
			}
			if processed && stats.folded != 1 {
				t.Fatalf("折叠数 %d", stats.folded)
			}
			if strings.Contains(messages[3].Content, "原文已在成功请求") {
				t.Fatal("Session原始材料被改写")
			}
		})
	}
}

func TestAnalysisProjectionStaleSummaryCannotFoldSource(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	r.state.Analysis.Processed["root"] = []string{"E1"}
	r.state.Analysis.Summaries["root"] = map[string]string{"E1": "旧摘要"}
	r.analysisRuntime.delivered["root"]["E1"] = true
	if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "E1.md"), []byte("新版"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[3].Content != messages[3].Content || stats.folded != 0 {
		t.Fatal("陈旧摘要导致正文折叠")
	}
	if strings.Contains(prepared[0].Content, "旧摘要") {
		t.Fatal("陈旧摘要继续进入上下文")
	}
}

func TestAnalysisHardBudgetDoesNotMaskUnprocessedBodies(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	_, _, _, err := NewAgentEngine(nil, nil, EngineOptions{MaxContextBytes: 1024}).prepareAnalysisRequest(ctx, r, messages, nil)
	if !errors.Is(err, errContextBudget) {
		t.Fatalf("应明确停止，不应截断材料: %v", err)
	}
}

func TestAnalysisHistoryDoesNotWindowAwayUnprocessedMaterials(t *testing.T) {
	s := ctxpkg.NewSession("test", t.TempDir())
	s.Append(schema.Message{Role: schema.RoleUser, Content: "旧任务"}, schema.Message{Role: schema.RoleAssistant, Content: "旧答复"}, schema.Message{Role: schema.RoleUser, Content: "当前任务"})
	for i := 0; i < 40; i++ {
		s.Append(schema.Message{Role: schema.RoleAssistant, Content: "记录"})
	}
	messages := analysisHistory(s)
	if len(messages) != 41 || messages[0].Content != "当前任务" {
		t.Fatalf("未保留本任务全部材料或混入旧任务: %d", len(messages))
	}
}

func TestAnalysisProcessedRequiresSuccessfulDelivery(t *testing.T) {
	ctx, r, _ := analysisContextFixture(t)
	text := `{"schema_version":1,"findings":[],"processed_reads":["E1"],"summaries":{"E1":"声明已处理"}}`
	if _, err := r.acceptAnalysis(ctx, text, false); err != nil {
		t.Fatal(err)
	}
	if len(r.state.Analysis.Processed["root"]) != 0 {
		t.Fatal("未完整交付的材料被标为已处理")
	}
	r.markAnalysisDelivered(ctx, []string{"E1"})
	if _, err := r.acceptAnalysis(ctx, text, false); err != nil {
		t.Fatal(err)
	}
	if !containsString(r.state.Analysis.Processed["root"], "E1") {
		t.Fatal("已交付且有摘要的材料没有保存处理声明")
	}
	if text := r.recordedPartial("模型额度耗尽", "").Text; !strings.Contains(text, "声明已处理") || !strings.Contains(text, "已记录文档摘要") {
		t.Fatal("预算停止遗漏了已保存的文档摘要")
	}
}

func TestAnalysisMergeLimitPreservesAcceptedNotes(t *testing.T) {
	ctx, r, _ := analysisContextFixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	report := AnalysisReport{SchemaVersion: 1}
	for i := 0; i < 64; i++ {
		report.Findings = append(report.Findings, AnalysisFinding{ID: fmt.Sprintf("F%d", i), Kind: "fact", Statement: "有效笔记", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}})
	}
	raw, _ := json.Marshal(report)
	if _, err := r.acceptAnalysis(ctx, string(raw), false); err != nil {
		t.Fatal(err)
	}
	next := report
	next.Findings = append([]AnalysisFinding(nil), report.Findings[:1]...)
	next.Findings[0].ID = "F65"
	raw, _ = json.Marshal(next)
	if _, err := r.acceptAnalysis(ctx, string(raw), false); err != nil {
		t.Fatal(err)
	}
	if len(r.state.Analysis.Reports["root"].Findings) != 64 || len(r.analysisRuntime.issues["root"]) == 0 {
		t.Fatal("合并超限清空了旧有效笔记，或没有显示缺口")
	}
}

func TestAnalysisAssistantProjectionPreservesOpaqueAndCallItems(t *testing.T) {
	opaque := json.RawMessage(`{"id":"rs1","type":"reasoning","encrypted_content":"opaque"}`)
	call := json.RawMessage(`{"id":"fc1","type":"function_call","call_id":"c1","name":"read_file","arguments":"{}"}`)
	message := schema.Message{Role: schema.RoleAssistant, Content: "旧分析JSON", ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read_file"}}, ResponseItems: []json.RawMessage{opaque, json.RawMessage(`{"id":"msg1","type":"message","role":"assistant","content":[{"type":"output_text","text":"旧分析JSON"}]}`), call}}
	projected := compactAnalysisAssistant(message)
	if len(projected.ResponseItems) != 2 || string(projected.ResponseItems[0]) != string(opaque) || string(projected.ResponseItems[1]) != string(call) || projected.ToolCalls[0].ID != "c1" {
		t.Fatal("笔记去重破坏了Responses调用或opaque项")
	}
	if len(message.ResponseItems) != 3 || message.Content != "旧分析JSON" {
		t.Fatal("投影改写原始响应")
	}
}
