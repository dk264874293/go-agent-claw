package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestAnalysisReviewRepeatedCallIDsCannotFoldUnprocessedSource(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	// 兼容渠道可以跨轮复用 ToolCallID；两条观察必须保留各自的结构化来源身份。
	messages[2].ToolCalls[0].ID = "callE2"
	messages[3].ToolCallID = "callE2"
	r.analysisRuntime.delivered["root"]["E1"] = true
	r.analysisRuntime.delivered["root"]["E2"] = true
	r.state.Analysis.Processed["root"] = []string{"E2"}
	r.state.Analysis.Summaries["root"] = map[string]string{"E2": "只处理了第二份材料"}
	prepared, delivered, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[3].Content != messages[3].Content || stats.folded != 0 || !containsString(delivered, "E1") {
		t.Fatalf("未处理 E1 被重复调用ID映射为已处理E2：folded=%d delivered=%v", stats.folded, delivered)
	}
	if prepared[5].Content != messages[5].Content {
		t.Fatal("最新批次没有保持完整")
	}
	// 接口内的证据身份只服务请求投影，不混进模型/持久消息材料。
	encoded, err := json.Marshal(messages[3])
	if err != nil || strings.Contains(string(encoded), "ReadEvidenceID") || strings.Contains(string(encoded), "read_evidence_id") {
		t.Fatalf("请求内部定位信息意外暴露：%s / %v", encoded, err)
	}
}

func TestAnalysisReviewChildPromptHasOneReportContract(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V2 的格式说明。\n")
	registry := NewExplorerRegistry(dir)
	rootCalls, childCalls := 0, 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if observe.Identity(ctx).ParentAgentID != "" {
			childCalls++
			prompt := messages[0].Content
			if strings.Contains(prompt, "findings 必须是字符串") || strings.Contains(prompt, `"findings":"发现摘要"`) || !strings.Contains(prompt, `"findings":[`) || !strings.Contains(prompt, `"evidence_id":"E1"`) {
				t.Fatalf("分析子任务收到互斥格式，或缺少统一契约：%s", prompt)
			}
			if childCalls == 1 {
				return modelCalls(readCall("child-read", "source.md"))
			}
			return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
		}
		rootCalls++
		if rootCalls == 1 {
			return modelCalls(schema.ToolCall{ID: "delegate", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"分析source.md","allowed_paths":["source.md"]}`)})
		}
		return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
	})
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	registry.Register(tools.NewSubagentTool(engine, NewExplorerRegistry(dir), nil))
	ctx := observe.WithAgent(context.Background(), "review-root", "")
	result, err := engine.Run(ctx, ctxpkg.NewSession("子提示回归", dir), TaskSpec{Goal: "分析文档", Analysis: true, Delegation: "on"}, nil)
	if err != nil || result.Status != Answered || rootCalls != 2 || childCalls != 2 || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 2 {
		t.Fatalf("子任务格式回归或新增修复调用：%+v root=%d child=%d err=%v", result, rootCalls, childCalls, err)
	}
}

type analysisReviewReadRegistry struct {
	tools.Registry
	alter func(*schema.ToolResult)
}

func (r analysisReviewReadRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	result := r.Registry.Execute(ctx, call)
	if call.Name == "read_file" && result.Read != nil && result.Status == schema.ToolSucceeded {
		r.alter(&result)
	}
	return result
}

func TestAnalysisReviewLargeReadOutputReachesModelWithoutSecondTruncation(t *testing.T) {
	dir := t.TempDir()
	const fact = "V2 默认行为为 data.get(\"score\", 5)。"
	source := fact + "\n" + strings.Repeat("补充材料。", 400) + "文档末尾唯一标记。"
	writeFixture(t, dir, "source.md", source)
	registry := analysisReviewReadRegistry{Registry: NewExplorerRegistry(dir), alter: func(result *schema.ToolResult) {
		// 模拟长路径/元数据使编号读取整体超过通用工具截断阈值，原文字节仍低于8KiB。
		result.Output = strings.Repeat("元数据", 1600) + "\n" + result.Output
	}}
	calls := 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("source", "source.md"))
		}
		if calls != 2 {
			t.Fatalf("不完整读取后发生额外修复调用：%d", calls)
		}
		last := messages[len(messages)-1]
		if last.ToolCallID != "source" || len(last.Content) <= 12000 || !strings.HasSuffix(last.Content, source) || strings.Contains(last.Content, "完整结果引用：") {
			t.Fatal("完整读取原文被通用工具输出阈值二次截断")
		}
		entries := analysisIntegrationEvidence(ctx)
		if len(entries) != 1 || string(entries[0].Source) != source {
			t.Fatalf("结构化源字节没有完整保留：%+v", entries)
		}
		report := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "默认值是5", Evidence: []AnalysisCitation{{EvidenceID: entries[0].ID, StartLine: 1, EndLine: 1, Quote: fact}}}}}
		encoded, _ := json.Marshal(report)
		return reply(string(encoded))
	})
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	result, err := engine.Run(observe.WithAgent(context.Background(), "review-root", ""), ctxpkg.NewSession("完整读取回归", dir), TaskSpec{Goal: "分析默认行为", Analysis: true}, nil)
	if err != nil || result.Status != Answered || calls != 2 || result.Analysis == nil || len(result.Analysis.Findings) != 1 {
		t.Fatalf("长元数据读取未完整交付：%+v err=%v", result, err)
	}
}

func TestAnalysisReviewReadSourceMismatchStopsWithoutAnotherModelCall(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "不可缺字的原文。\n")
	registry := analysisReviewReadRegistry{Registry: NewExplorerRegistry(dir), alter: func(result *schema.ToolResult) {
		result.ReadContent = result.ReadContent[:len(result.ReadContent)-1]
	}}
	calls := 0
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls != 1 {
			t.Fatal("原文不完整后继续调用模型")
		}
		return modelCalls(readCall("source", "source.md"))
	})
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("原文不完整", dir), TaskSpec{Goal: "分析原文", Analysis: true}, nil)
	if err == nil || result.Status != Failed || result.StopCode != "storage_error" || calls != 1 || result.Usage.ModelCalls != 1 || result.FinalText != "" {
		t.Fatalf("不完整原文被当作完整证据或发生额外收费调用：%+v calls=%d err=%v", result, calls, err)
	}
}

func TestAnalysisReviewOverflowUpdateKeepsAcceptedNotes(t *testing.T) {
	ctx, r, _ := analysisContextFixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	makeFinding := func(id string) AnalysisFinding {
		return AnalysisFinding{ID: id, Kind: "fact", Statement: "有来源的有效事实", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "E1 决定性事实"}}}
	}
	initial := AnalysisReport{SchemaVersion: 1}
	for i := 0; i < 64; i++ {
		initial.Findings = append(initial.Findings, makeFinding(fmt.Sprintf("F%d", i)))
	}
	encoded, _ := json.Marshal(initial)
	if _, err := r.acceptAnalysis(ctx, string(encoded), false); err != nil {
		t.Fatal(err)
	}
	if report := r.analysisReport(ctx); report == nil || len(report.Findings) != 64 {
		t.Fatal("未建立合法的64条笔记边界")
	}
	next := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{makeFinding("F_new")}}
	encoded, _ = json.Marshal(next)
	if _, err := r.acceptAnalysis(ctx, string(encoded), false); err != nil {
		t.Fatal(err)
	}
	if report := r.analysisReport(ctx); report == nil || len(report.Findings) != 64 {
		t.Fatalf("合法中间更新合并超限时清空已有有效笔记：%+v", report)
	}
	if len(r.analysisRuntime.issues["root"]) == 0 {
		t.Fatal("笔记容量边界未明确告知模型")
	}
}

func TestAnalysisReviewDirectRunKeepsChildEvidencePrivate(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "材料A。\n")
	writeFixture(t, dir, "b.md", "材料B。\n")
	registry := NewExplorerRegistry(dir)
	rootID, rootCalls := "", 0
	childCalls := make(map[string]int)
	var callsMu sync.Mutex
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		identity := observe.Identity(ctx)
		if !tools.ScopeFromContext(ctx).DisableSubagent {
			rootCalls++
			if identity.AgentID == "" || identity.ParentAgentID != "" {
				t.Fatal("直接调用引擎未分配可区分父子的内部根身份")
			}
			rootID = identity.AgentID
			if rootCalls == 1 {
				return modelCalls(
					schema.ToolCall{ID: "delegate-a", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"分析a.md"}`)},
					schema.ToolCall{ID: "delegate-b", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"分析b.md"}`)},
				)
			}
			return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
		}
		if identity.ParentAgentID != rootID || identity.AgentID == rootID {
			t.Fatalf("没有入口观测上下文时子任务被误认为根任务：%+v root=%s", identity, rootID)
		}
		r := currentRun(ctx)
		r.mu.Lock()
		var foreign []string
		for id, entry := range r.analysisRuntime.sources {
			if entry.AgentID != identity.AgentID {
				foreign = append(foreign, id)
			}
		}
		r.mu.Unlock()
		for _, id := range foreign {
			if _, err := r.analysisLookup(ctx, false)(id); err == nil {
				t.Fatalf("无入口身份时兄弟证据进入子任务目录：%s", id)
			}
		}
		callsMu.Lock()
		childCalls[identity.AgentID]++
		n := childCalls[identity.AgentID]
		callsMu.Unlock()
		if n == 1 {
			name := "b.md"
			for _, message := range messages {
				if message.Role == schema.RoleUser && message.ToolCallID == "" && strings.Contains(message.Content, "分析a.md") {
					name = "a.md"
				}
			}
			return modelCalls(readCall("child-read", name))
		}
		return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
	})
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	registry.Register(tools.NewSubagentTool(engine, NewExplorerRegistry(dir), nil))
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("无入口身份", dir), TaskSpec{Goal: "分别分析两份材料", Analysis: true, Delegation: "on"}, nil)
	if err != nil || result.Status != Answered || rootCalls != 2 || len(childCalls) != 2 || result.Usage.ModelCalls != 6 || result.Usage.ToolCalls != 4 {
		t.Fatalf("无观测入口的父子身份或计数不正确：%+v root=%d children=%v err=%v", result, rootCalls, childCalls, err)
	}
}
