package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReviewTaskHistoryFeedbackKeepsUnprocessedPair(t *testing.T) {
	s := ctxpkg.NewSession("review-history", t.TempDir())
	s.Append(schema.Message{Role: schema.RoleUser, Content: "前一个任务"}, schema.Message{Role: schema.RoleAssistant, Content: "仅作历史背景"})
	r := &runState{}
	r.bindTaskHistory(s)
	s.Append(schema.Message{Role: schema.RoleUser, Content: "本次任务"},
		schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{readCall("current-read", "source.md")}},
		schema.Message{Role: schema.RoleUser, ToolCallID: "current-read", ReadEvidenceID: "E1", Content: "仍未处理的决定性原文"},
		schema.Message{Role: schema.RoleUser, Content: "同任务的补充或验收反馈"})
	history := r.taskHistory(s)
	paired, source, feedback := false, false, false
	for _, m := range history {
		paired = paired || len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "current-read"
		source = source || m.ToolCallID == "current-read" && m.Content == "仍未处理的决定性原文"
		feedback = feedback || m.Content == "同任务的补充或验收反馈"
	}
	if !paired || !source || !feedback {
		t.Fatalf("任务内普通用户消息移动了读取边界: %+v", history)
	}
	if _, err := ctxpkg.CompactAnalysisWithin(history, nil, 300000); err != nil {
		t.Fatalf("任务内反馈导致工具配对断裂: %v", err)
	}
}

func TestReviewNewTaskDoesNotAdoptOldEvidenceAndKeepsFinalJSON(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "旧任务工具原文，不得伪装成本次读取\n")
	calls := 0
	final := `{"score":5,"message":"普通业务JSON终答"}`
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("old-read", "source.md"))
		case 2:
			return reply("上次已阅读，仅为历史背景")
		case 3:
			for _, m := range messages {
				if m.ReadEvidenceID != "" || strings.Contains(m.Content, "旧任务工具原文，不得伪装成本次读取") {
					t.Fatalf("旧工具正文进入新任务读取上下文: %+v", m)
				}
			}
			if entries := analysisIntegrationEvidence(ctx); len(entries) != 0 {
				t.Fatalf("新运行继承了旧读取资格: %+v", entries)
			}
			body, _ := json.Marshal(map[string]any{"work_notes": AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{"E1"}, Summaries: map[string]string{"E1": "伪造跨任务摘要"}}})
			return &schema.Message{Role: schema.RoleAssistant, Content: string(body), ToolCalls: []schema.ToolCall{{ID: "listing", Name: "list_dir", Arguments: json.RawMessage(`{"path":"."}`)}}}, nil
		default:
			return reply(final)
		}
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	session := ctxpkg.NewSession("shared-session", dir)
	first, err := eng.Run(context.Background(), session, TaskSpec{Goal: "读文档"}, nil)
	if err != nil || first.Status != NeedsVerification {
		t.Fatalf("首任务失败: %+v %v", first, err)
	}
	second, err := eng.Run(context.Background(), session, TaskSpec{Goal: "另一个任务，返回JSON"}, nil)
	if err != nil || second.Status != NeedsVerification || second.FinalText != final || calls != 4 {
		t.Fatalf("普通终答契约改变或增加模型调用: %+v %v calls=%d", second, err, calls)
	}
	state := loadAnalysisIntegrationState(t, dir, second.TaskID)
	if len(state.ReadMemory.Evidence) != 0 {
		t.Fatalf("旧证据被新运行持久化: %+v", state.ReadMemory)
	}
	for _, ids := range state.ReadMemory.ProcessedReads {
		if len(ids) != 0 {
			t.Fatal("伪造的历史processed获得资格")
		}
	}
	for _, summaries := range state.ReadMemory.Summaries {
		if len(summaries) != 0 {
			t.Fatal("伪造的历史摘要被接收")
		}
	}
}

func TestReviewOmittedNotesDoNotGrantReuseQualification(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	a := r.state.Analysis
	r.state.Spec.ReadContextVersion = 1
	r.state.ReadMemory = &ReadMemoryState{Version: 1, Evidence: a.Evidence, Notes: a.Reports, ProcessedReads: a.Processed, Summaries: a.Summaries}
	r.state.Analysis = nil
	r.state.ReadMemory.ProcessedReads["root"] = []string{"E1"}
	r.state.ReadMemory.Summaries["root"] = map[string]string{}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("E%d", i)
		if i > 2 {
			x := r.analysisRuntime.sources["E1"]
			x.ID, x.Receipt.EvidenceID, x.ActionID, x.Receipt.ActionID = id, id, "action"+id, "action"+id
			r.analysisRuntime.sources[id] = x
			r.state.ReadMemory.Evidence[id] = x
		}
		r.state.ReadMemory.Summaries["root"][id] = strings.Repeat("摘要", 300)
		r.analysisRuntime.delivered["root"][id] = true
	}
	x := r.analysisRuntime.sources["E1"]
	r.readReuse.records = map[string]readReuseRecord{x.ActionID: {Read: x.Receipt, Observation: messages[3].Content, Source: x.Source}}
	// E1的原文不在本请求中，只有20份来源摘要竞争16KiB笔记空间。
	request := append(append([]schema.Message{}, messages[:2]...), messages[4:]...)
	prepared, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.notes > 16*1024 {
		t.Fatalf("笔记投影超限: %d", stats.notes)
	}
	r.markReadExposure(ctx, prepared)
	r.mu.Lock()
	exposed := r.analysisRuntime.notesExposed["root"]["E1"]
	reusable := r.readReusableLocked("root", x.ActionID)
	r.mu.Unlock()
	if exposed || reusable {
		t.Fatalf("已省略且没有原文的笔记获得复读拒绝资格: exposed=%v reusable=%v", exposed, reusable)
	}
}

func TestReviewBudgetClosingKeepsUnprocessedSources(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 24; i++ {
		writeFixture(t, dir, fmt.Sprintf("source-%02d.md", i), fmt.Sprintf("收尾独有原文-%02d\n", i))
	}
	calls := 0
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls <= 2 {
			var batch []schema.ToolCall
			for i := (calls - 1) * 12; i < calls*12; i++ {
				batch = append(batch, readCall(fmt.Sprintf("source-%d", i), fmt.Sprintf("source-%02d.md", i)))
			}
			return modelCalls(batch...)
		}
		if modelStage(ctx) != "closing" {
			t.Fatal("额度已尽却继续了普通探索")
		}
		found := false
		for _, m := range messages {
			found = found || strings.Contains(m.Content, "收尾独有原文-00")
		}
		if !found {
			t.Fatal("通用读取记忆在收尾时又丢弃了未处理原文")
		}
		return reply("部分分析，尚未完成")
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxToolCalls: 24}})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("closing-review", dir), TaskSpec{Goal: "读取资料"}, nil)
	if err != nil || result.Status != BudgetExhausted || result.Usage.ToolCalls != 24 || result.Usage.ModelCalls != 3 || result.PartialReport == nil {
		t.Fatalf("收尾预算或部分交付异常: %+v %v calls=%d", result, err, calls)
	}
}

func TestReviewReadEvidenceCannotBypassChildSourceScope(t *testing.T) {
	artifact := strings.Repeat("a", 32)
	r := &runState{runID: "current-run", state: taskState{Spec: TaskSpec{ReadContextVersion: 1}, ReadMemory: &ReadMemoryState{Version: 1, Evidence: map[string]AnalysisEvidence{
		"E1": {ID: "E1", AgentID: "parent", RunID: "current-run", ArtifactID: artifact, Receipt: schema.ReadReceipt{Path: "private.md", AgentID: "parent"}},
	}}}}
	read := false
	r.readArtifactPage = func(string, int, int) (taskstore.ArtifactPage, error) {
		read = true
		return taskstore.ArtifactPage{Data: []byte("父任务私有原文"), EOF: true}, nil
	}
	ctx := context.WithValue(context.Background(), runContextKey{}, r)
	ctx = observe.WithAgent(ctx, "child", "parent")
	ctx = tools.WithScope(ctx, tools.ExecutionScope{Mode: tools.ModeReadOnly, AllowedPaths: []string{"public.md"}, WorkDir: t.TempDir()})
	_, err := (evidenceTool{}).Execute(ctx, json.RawMessage(`{"id":"`+artifact+`"}`))
	if err == nil || read {
		t.Fatalf("子任务通过原文artifact绕过路径权限: err=%v read=%v", err, read)
	}
}
