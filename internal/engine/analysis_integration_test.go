package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func analysisIntegrationEvidence(ctx context.Context) []AnalysisEvidence {
	r := currentRun(ctx)
	identity := observe.Identity(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []AnalysisEvidence
	for _, entry := range r.analysisRuntime.sources {
		if entry.AgentID == identity.AgentID || identity.ParentAgentID == "" {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func analysisIntegrationJSON(evidence []AnalysisEvidence) string {
	report := AnalysisReport{SchemaVersion: 1}
	for i, entry := range evidence {
		quote := strings.TrimSuffix(string(entry.Source), "\n")
		report.Findings = append(report.Findings, AnalysisFinding{ID: fmt.Sprintf("F%d", i+1), Kind: "fact", Statement: "材料有明确版本说明", Evidence: []AnalysisCitation{{EvidenceID: entry.ID, StartLine: entry.Receipt.FirstLine, EndLine: entry.Receipt.LastLine, Quote: quote}}, Context: "仅核对本次读取版本"})
	}
	data, _ := json.Marshal(report)
	return string(data)
}

func loadAnalysisIntegrationState(t *testing.T, dir, id string) taskState {
	t.Helper()
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.OpenTask(id)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	state, err := loadTaskState(task)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAnalysisIntegrationFinalDeliveryAndNoRepairCalls(t *testing.T) {
	for _, mode := range []string{"valid", "partial", "invalid", "malformed", "stale"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "V2 新格式\n")
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return modelCalls(readCall("source", "source.md"))
				}
				if calls != 2 {
					t.Fatalf("出现额外修复或评审调用：%d", calls)
				}
				if mode == "malformed" {
					return reply("分析完成，全部正确")
				}
				entries := analysisIntegrationEvidence(ctx)
				if len(entries) != 1 {
					t.Fatalf("没有本次原文证据：%+v", entries)
				}
				var report AnalysisReport
				_ = json.Unmarshal([]byte(analysisIntegrationJSON(entries)), &report)
				if mode == "partial" || mode == "invalid" {
					bad := report.Findings[0]
					bad.ID = "F_bad"
					bad.Statement = "无效依据的结论"
					bad.Evidence = append([]AnalysisCitation(nil), bad.Evidence...)
					bad.Evidence[0].Quote = "未读取的内容"
					if mode == "partial" {
						report.Findings = append(report.Findings, bad)
					} else {
						report.Findings = []AnalysisFinding{bad}
					}
				}
				if mode == "stale" {
					writeFixture(t, dir, "source.md", "V3 文件已变更\n")
				}
				data, _ := json.Marshal(report)
				return reply(string(data))
			})
			engine := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
			ctx := observe.WithAgent(context.Background(), "analysis-root", "")
			result, err := engine.Run(ctx, ctxpkg.NewSession("分析交付", dir), TaskSpec{Goal: "分析文档版本", Analysis: true}, nil)
			if err != nil || calls != 2 || result.Usage.ModelCalls != 2 || result.Usage.ToolCalls != 1 {
				t.Fatalf("计数或结果错误：%+v calls=%d err=%v", result, calls, err)
			}
			if mode == "valid" || mode == "partial" {
				if result.Status != Answered || result.Analysis == nil || len(result.Analysis.Findings) != 1 || !strings.Contains(result.FinalText, "source.md:1") || result.PartialReport != nil {
					t.Fatalf("有效报告未正确交付：%+v", result)
				}
				if mode == "partial" && (len(result.Analysis.Unverified) == 0 || !strings.Contains(result.FinalText, "待核实")) {
					t.Fatal("部分无效引用未降级")
				}
			} else if result.Status != Blocked || result.StopCode != "analysis_report_invalid" || result.ExitCode() != 2 || result.FinalText != "" || result.PartialReport == nil {
				t.Fatalf("无效报告误报完成或丢失部分交付：%+v", result)
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			if analysisStateOf(state) == nil || len(analysisStateOf(state).Evidence) != 1 || state.LastResult == nil || state.LastResult.Status != result.Status {
				t.Fatal("终态或证据没有正确落盘")
			}
			for _, entry := range analysisStateOf(state).Evidence {
				if len(entry.Source) != 0 || entry.ArtifactID == "" {
					t.Fatal("原文重复编码在状态或缺少材料引用")
				}
				data, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", result.TaskID, "artifacts", entry.ArtifactID))
				if err != nil || string(data) != "V2 新格式\n" {
					t.Fatalf("不可变原文材料不完整：%q %v", data, err)
				}
			}
		})
	}
}

func TestAnalysisIntegrationCriticalEvidenceStorageFailure(t *testing.T) {
	for _, kind := range []string{"artifact", "analysis_updated"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "V2 新格式\n")
			calls := 0
			provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					r := currentRun(ctx)
					r.mu.Lock()
					if kind == "artifact" {
						r.artifact = func(string, []byte) error { return errors.New("模拟磁盘不可写") }
					} else {
						persist := r.persist
						r.persist = func(k string, s taskState) error {
							if k == kind {
								return errors.New("模拟笔记事件提交失败")
							}
							return persist(k, s)
						}
					}
					r.mu.Unlock()
					return modelCalls(readCall("source", "source.md"))
				}
				return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
			})
			engine := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
			result, err := engine.Run(context.Background(), ctxpkg.NewSession("证据存储失败", dir), TaskSpec{Goal: "分析", Analysis: true}, nil)
			want := 1
			if kind == "analysis_updated" {
				want = 2
			}
			if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" || result.FinalText != "" || result.PartialReport == nil || calls != want {
				t.Fatalf("关键证据失败没有正确停止：%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestAnalysisIntegrationResumeDoesNotReuseOldRunEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "V2 新格式\n")
	var previousID string
	calls := 0
	provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("first", "source.md"))
		}
		entries := analysisIntegrationEvidence(ctx)
		previousID = entries[0].ID
		message, _ := modelCalls(readCall("second", "source.md"))
		message.Content = analysisIntegrationJSON(entries)
		return message, nil
	})
	ctx := observe.WithAgent(context.Background(), "analysis-root", "")
	engine := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 2}})
	first, err := engine.Run(ctx, ctxpkg.NewSession("分析恢复", dir), TaskSpec{Goal: "读取并分析", Analysis: true}, nil)
	if err != nil || first.Status != BudgetExhausted || first.Analysis == nil || len(first.Analysis.Findings) != 1 || calls != 2 {
		t.Fatalf("未建立预算停止的历史笔记：%+v %v", first, err)
	}
	resumeCalls := 0
	resumeProvider := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		resumeCalls++
		if len(analysisIntegrationEvidence(ctx)) != 0 {
			t.Fatal("恢复把旧原文提升为本次运行证据")
		}
		body := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "旧版本结论", Evidence: []AnalysisCitation{{EvidenceID: previousID, StartLine: 1, EndLine: 1, Quote: "V2 新格式"}}}}}
		data, _ := json.Marshal(body)
		return reply(string(data))
	})
	resumed := NewAgentEngine(resumeProvider, NewExplorerRegistry(dir), EngineOptions{Budget: Budget{MaxModelCalls: 4}})
	second, err := resumed.Run(ctx, ctxpkg.NewSession("分析恢复", dir), TaskSpec{ID: first.TaskID, Resume: true}, nil)
	if err != nil || second.Status != Blocked || second.StopCode != "analysis_report_invalid" || second.FinalText != "" || resumeCalls != 1 || second.Usage.ModelCalls != 3 || second.Usage.ToolCalls != 2 {
		t.Fatalf("旧笔记被当作当前证据或额外调用：%+v %v", second, err)
	}
	if second.RunID == first.RunID {
		t.Fatal("恢复没有新的运行身份")
	}
}

func TestAnalysisIntegrationConcurrentChildrenKeepEvidenceOwnership(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.md", "材料 A\n")
	writeFixture(t, dir, "b.md", "材料 B\n")
	var engine *AgentEngine
	var total atomic.Int32
	var mu sync.Mutex
	counts := make(map[string]int)
	ready := sync.WaitGroup{}
	ready.Add(2)
	provider := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		total.Add(1)
		identity := observe.Identity(ctx)
		if identity.ParentAgentID == "" {
			var wg sync.WaitGroup
			wg.Add(2)
			for _, name := range []string{"a.md", "b.md"} {
				go func(name string) {
					defer wg.Done()
					report, err := engine.RunSub(ctx, tools.SubtaskSpec{Goal: "分析 " + name, AllowedPaths: []string{name}}, NewExplorerRegistry(dir), nil)
					if err != nil || report.Status != "SUCCEEDED" {
						t.Errorf("子报告失败：%+v %v", report, err)
					}
				}(name)
			}
			wg.Wait()
			entries := analysisIntegrationEvidence(ctx)
			if len(entries) != 2 {
				t.Fatalf("父任务没有获得两份读取证据：%+v", entries)
			}
			return reply(analysisIntegrationJSON(entries))
		}
		mu.Lock()
		counts[identity.AgentID]++
		n := counts[identity.AgentID]
		mu.Unlock()
		if n == 1 {
			return modelCalls(readCall("child-read", tools.ScopeFromContext(ctx).AllowedPaths[0]))
		}
		ready.Done()
		ready.Wait()
		r := currentRun(ctx)
		r.mu.Lock()
		var foreign string
		for id, e := range r.analysisRuntime.sources {
			if e.AgentID != identity.AgentID {
				foreign = id
			}
		}
		r.mu.Unlock()
		if foreign == "" {
			t.Fatal("未实际形成并发子任务证据")
		}
		if _, err := r.analysisLookup(ctx, true)(foreign); err == nil {
			t.Error("兄弟 Agent 证据越权")
		}
		return reply(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))
	})
	engine = NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
	ctx := observe.WithAgent(context.Background(), "analysis-root", "")
	result, err := engine.Run(ctx, ctxpkg.NewSession("并发分析", dir), TaskSpec{Goal: "分析两份文档", Analysis: true, Delegation: "on"}, nil)
	if err != nil || result.Status != Answered || result.Analysis == nil || len(result.Analysis.Findings) != 2 || total.Load() != 5 || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 2 {
		t.Fatalf("并发证据复用或计数错误：%+v total=%d err=%v", result, total.Load(), err)
	}
}
