package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestWorkspaceMemoryPersistsNotesWithoutChangingFinalContract(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V2 可选字段 score 默认 5\n")
	calls := 0
	model := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		for _, m := range messages {
			if m.Role == schema.RoleSystem && strings.Contains(m.Content, `"progress"`) {
				t.Fatal("Progress正文重复进入请求")
			}
		}
		switch calls {
		case 1:
			return modelCalls(readCall("first", "source.md"))
		case 2:
			e := analysisIntegrationEvidence(ctx)
			if len(e) != 1 {
				t.Fatalf("没有共享证据: %+v", e)
			}
			n := AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{e[0].ID}, Summaries: map[string]string{e[0].ID: "V2：score为可选，缺失默认5，不能断言缺失必报错"}}
			b, _ := json.Marshal(map[string]any{"work_notes": n})
			return &schema.Message{Role: schema.RoleAssistant, Content: string(b), ToolCalls: []schema.ToolCall{readCall("repeat", "source.md")}}, nil
		case 3:
			found := false
			for _, m := range messages {
				found = found || strings.Contains(m.Content, "duplicate_read")
			}
			if !found {
				t.Fatal("无目标重复读取未拒绝")
			}
			return modelCalls(schema.ToolCall{ID: "write", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.md","content":"结果"}`)})
		default:
			return reply("保持自然语言终答")
		}
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	run, err := eng.Run(context.Background(), ctxpkg.NewSession("memory", dir), TaskSpec{Goal: "生成报告"}, nil)
	if err != nil || run.Status != NeedsVerification || run.FinalText != "保持自然语言终答" || run.Usage.ToolCalls != 3 || calls != 4 {
		t.Fatalf("契约或计数错误: %+v %v", run, err)
	}
	s := loadAnalysisIntegrationState(t, dir, run.TaskID)
	if s.ReadMemory == nil || len(s.ReadMemory.Evidence) != 1 || s.Analysis != nil {
		t.Fatalf("证据重复或未持久化: %+v", s)
	}
	processed := 0
	for _, ids := range s.ReadMemory.ProcessedReads {
		processed += len(ids)
	}
	if processed != 1 {
		t.Fatal("有效摘要没有激活处理资格")
	}
}

func TestWorkspaceUnprocessedSourcesSurviveTwentyMessageWindow(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 24; i++ {
		writeFixture(t, dir, fmt.Sprintf("%02d.md", i), fmt.Sprintf("唯一原文-%02d\n", i))
	}
	calls := 0
	model := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls <= 2 {
			var batch []schema.ToolCall
			for i := (calls - 1) * 12; i < calls*12; i++ {
				batch = append(batch, readCall(fmt.Sprintf("r%d", i), fmt.Sprintf("%02d.md", i)))
			}
			return modelCalls(batch...)
		}
		all := ""
		for _, m := range messages {
			all += m.Content
		}
		for i := 0; i < 24; i++ {
			if !strings.Contains(all, fmt.Sprintf("唯一原文-%02d", i)) {
				t.Fatalf("第%d份未处理原文被窗口丢弃", i)
			}
		}
		return reply("已阅读")
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	run, err := eng.Run(context.Background(), ctxpkg.NewSession("window", dir), TaskSpec{Goal: "读取全部材料"}, nil)
	if err != nil || run.Status != NeedsVerification || run.Usage.ToolCalls != 24 {
		t.Fatalf("未处理阅读失败: %+v %v", run, err)
	}
}

func TestWorkspaceMemoryBashSameHashRevokesReuse(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "原文\n")
	calls := 0
	model := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read1", "a.md"))
		case 2:
			e := analysisIntegrationEvidence(ctx)
			b, _ := json.Marshal(map[string]any{"work_notes": AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{e[0].ID}, Summaries: map[string]string{e[0].ID: "原文已检查"}}})
			return &schema.Message{Role: schema.RoleAssistant, Content: string(b), ToolCalls: []schema.ToolCall{{ID: "bash", Name: "bash", Arguments: json.RawMessage(`{"command":"true"}`)}}}, nil
		case 3:
			return modelCalls(readCall("read2", "a.md"))
		default:
			return reply("已重新核实")
		}
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeTrusted})
	run, err := eng.Run(context.Background(), ctxpkg.NewSession("bash", dir), TaskSpec{Goal: "核实"}, nil)
	if err != nil || run.Status != NeedsVerification {
		t.Fatalf("执行失败: %+v %v", run, err)
	}
	s := loadAnalysisIntegrationState(t, dir, run.TaskID)
	if len(s.ReadMemory.Evidence) != 2 || len(s.ReadMemory.Invalidations) == 0 {
		t.Fatalf("相同hash错误恢复旧资格: %+v", s.ReadMemory)
	}
	if s.LastAction.Result.Status != schema.ToolSucceeded {
		t.Fatal("撤销后重新读取被错误拒绝")
	}
}

func TestReadMemoryVersionsAndPageResumeValidation(t *testing.T) {
	for _, s := range []taskState{{Spec: TaskSpec{ReadContextVersion: 4}}, {Spec: TaskSpec{ReadPageBytes: 9000}}, {Spec: TaskSpec{ReadContextVersion: 1}, ReadMemory: &ReadMemoryState{Version: 2}}} {
		if validateReadMemoryState(s) == nil {
			t.Fatalf("未知版本未拒绝: %+v", s)
		}
	}
	dir := t.TempDir()
	eng := NewAgentEngine(scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return reply("好")
	}), NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	sess := ctxpkg.NewSession("pages", dir)
	run, err := eng.Run(context.Background(), sess, TaskSpec{Goal: "答复", ReadPageBytes: 16000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background(), sess, TaskSpec{ID: run.TaskID, Resume: true, ReadPageBytes: 8000}, nil)
	if res.Status != Blocked || err != nil {
		t.Fatalf("恢复错误覆盖档位: %+v %v", res, err)
	}
}
