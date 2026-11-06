package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type analysisFixtureManifest struct {
	Synthetic  bool `json:"synthetic"`
	FileCount  int  `json:"file_count"`
	TotalBytes int  `json:"total_bytes"`
	Files      []struct {
		Path   string `json:"path"`
		Bytes  int    `json:"bytes"`
		Depth  int    `json:"depth"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func copyAnalysisFixture(t *testing.T, dir string) analysisFixtureManifest {
	t.Helper()
	base := filepath.Join("testdata", "task_analysis_v1")
	encoded, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture analysisFixtureManifest
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	if !fixture.Synthetic || fixture.FileCount != 15 || len(fixture.Files) != 15 || fixture.TotalBytes != 87105 {
		t.Fatal("夹具规模或脱敏声明不符")
	}
	total := 0
	for _, entry := range fixture.Files {
		data, err := os.ReadFile(filepath.Join(base, "workspace", entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(entry.Path) || !utf8.Valid(data) || len(data) != entry.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != entry.SHA256 || len(strings.Split(entry.Path, "/")) != entry.Depth {
			t.Fatalf("夹具被意外修改: %s", entry.Path)
		}
		target := filepath.Join(dir, entry.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
		total += len(data)
	}
	if total != fixture.TotalBytes {
		t.Fatal("夹具总字节数错误")
	}
	return fixture
}

// 使用合成内容和脚本 Provider 重建历史 15 文档、5 子任务的共享预算结构，不衡量真实模型质量。
// 此历史夹具显式使用 workspace + answer 隔离新增只读委派策略；只读两子任务上限另有独立回归。
func TestTaskAnalysisFixtureCoverageReuseAndBudgetDelivery(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprintf("工具边界_%t", capped), func(t *testing.T) {
			dir := t.TempDir()
			fixture := copyAnalysisFixture(t, dir)
			totalReads := len(fixture.Files)
			for _, file := range fixture.Files {
				if file.Bytes > 8000 {
					totalReads++
				}
			}
			wantTools := totalReads + 5
			wantReports := 5
			if capped {
				wantReports = 4
			} // 最后一个子任务读取后已耗尽共享工具额度，留下覆盖和缺口。
			var requests atomic.Int32
			var rootRequests int
			provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
				requests.Add(1)
				identity := observe.Identity(ctx)
				if identity.ParentAgentID != "" {
					r := currentRun(ctx)
					reads := r.agentReads(identity.AgentID)
					var calls []schema.ToolCall
					var refs []tools.EvidenceRef
					for _, path := range tools.ScopeFromContext(ctx).AllowedPaths {
						var end, total int64
						version := ""
						for _, entry := range fixture.Files {
							if entry.Path == path {
								total, version = int64(entry.Bytes), entry.SHA256
							}
						}
						for _, coverage := range mergeReadCoverage(reads) {
							if coverage.Path == path && len(coverage.Ranges) > 0 && coverage.Ranges[0].Start == 0 {
								end = coverage.Ranges[0].End
							}
						}
						if end < total {
							args, _ := json.Marshal(map[string]any{"path": path, "offset": end, "limit": 8000})
							calls = append(calls, schema.ToolCall{ID: fmt.Sprintf("%s-%d", path, end), Name: "read_file", Arguments: args})
						} else {
							refs = append(refs, tools.EvidenceRef{Path: path, Line: 1, Version: version})
						}
					}
					if len(calls) > 0 {
						return modelCalls(calls...)
					}
					body, _ := json.Marshal(map[string]any{"findings": map[string]any{"summary": "已覆盖本组文档", "documents": len(refs)}, "evidence": refs, "unverified": []string{}})
					return reply(string(body))
				}
				rootRequests++
				if rootRequests == 1 {
					var batch []schema.ToolCall
					for i := 0; i < 5; i++ {
						var paths []string
						for _, entry := range fixture.Files[i*3 : i*3+3] {
							paths = append(paths, entry.Path)
						}
						args, _ := json.Marshal(map[string]any{"task_prompt": "分析本组文档，交付有引用的报告", "allowed_paths": paths, "max_turns": 6})
						batch = append(batch, schema.ToolCall{ID: fmt.Sprintf("delegate-%d", i), Name: "spawn_subagent", Arguments: args})
					}
					return modelCalls(batch...)
				}
				if n := strings.Count(messages[0].Content, "子报告摘要（可能节选）：") + strings.Count(messages[0].Content, "结构化子报告目录（read_report可按发现取详情）："); n != wantReports {
					return nil, fmt.Errorf("父任务没有取得全部有效报告：%d/%d，system_bytes=%d", n, wantReports, len(messages[0].Content))
				}
				for _, file := range fixture.Files {
					if !strings.Contains(messages[0].Content, file.Path) {
						return nil, fmt.Errorf("覆盖目录缺少 %s", file.Path)
					}
				}
				if capped && (modelStage(ctx) != "closing" || len(defs) != 0) {
					return nil, fmt.Errorf("工具耗尽后仍在探索")
				}
				return reply("已读取全部 15 份结构夹具，跨文档结论仍需独立核对。")
			})
			budget := Budget{MaxToolCalls: wantTools + 10, MaxModelCalls: 30}
			if capped {
				budget.MaxToolCalls = wantTools
			}
			registry := NewWorkspaceRegistry(dir)
			eng := NewAgentEngine(provider, registry, EngineOptions{Budget: budget, Mode: tools.ModeWorkspace})
			registry.Register(tools.NewSubagentTool(eng, NewExplorerRegistry(dir), nil))
			ctx := observe.WithAgent(context.Background(), "fixture-root", "")
			result, err := eng.Run(ctx, ctxpkg.NewSession("15文档脱敏结构", dir), TaskSpec{Goal: "分析当前项目的15份Markdown文档", Kind: TaskAnswer, Mode: tools.ModeWorkspace}, nil)
			if err != nil || result.Usage.ToolCalls != wantTools || result.Usage.ModelCalls != int(requests.Load()) {
				t.Fatalf("离线场景失败: %+v %v", result, err)
			}
			if capped {
				assertClosedBudget(t, result, err, int(requests.Load()), wantTools, "model")
			} else if result.Status != Answered || result.PartialReport != nil {
				t.Fatalf("正常交付错误: %+v", result)
			}
			// 独立从持久事件复核覆盖，不依赖模型声称的“全部读取”。
			store, err := taskstore.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task, err := store.OpenTask(result.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			defer task.Close()
			events, err := task.Events()
			if err != nil {
				t.Fatal(err)
			}
			var receipts []schema.ReadReceipt
			accepted := 0
			for _, event := range events {
				if event.Kind != "action_result" {
					continue
				}
				var state taskState
				if err := json.Unmarshal(event.Payload, &state); err != nil {
					t.Fatal(err)
				}
				action := state.LastAction
				if action == nil {
					t.Fatal("动作结果缺失")
				}
				if action.Tool == "spawn_subagent" && action.Result.Status == schema.ToolSucceeded {
					accepted++
				}
				if receipt := action.Result.Read; receipt != nil {
					if receipt.AgentID == "fixture-root" || receipt.AgentID == "" || receipt.ActionID != action.ID {
						t.Fatal("读取身份错误或父任务进行了不必要重读")
					}
					receipts = append(receipts, *receipt)
				}
			}
			coverage := mergeReadCoverage(receipts)
			if len(receipts) != totalReads || len(coverage) != 15 || accepted != wantReports {
				t.Fatalf("独立覆盖错误: reads=%d docs=%d reports=%d", len(receipts), len(coverage), accepted)
			}
			for _, item := range coverage {
				if len(item.Ranges) != 1 || item.Ranges[0].Start != 0 || item.Ranges[0].End != item.Total {
					t.Fatalf("存在未读区间: %+v", item)
				}
			}
			t.Logf("结构场景：15文档 / %d模型调用 / %d工具尝试 / 引擎终态%s；没有付费模型请求", requests.Load(), wantTools, result.Status)
		})
	}
}

func TestTaskAnalysisReportTemplates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "task_analysis_v1", "reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var templates struct {
		Reports []struct {
			Name     string          `json:"name"`
			Findings json.RawMessage `json:"findings"`
		} `json:"reports"`
		InvalidJSON string `json:"invalid_json"`
	}
	if err := json.Unmarshal(data, &templates); err != nil {
		t.Fatal(err)
	}
	for _, item := range templates.Reports {
		body, _ := json.Marshal(map[string]any{"findings": item.Findings, "evidence": []tools.EvidenceRef{}, "unverified": []string{}})
		_, err := decodeSubtaskReport(string(body))
		if (err != nil) != (item.Name == "invalid_findings") {
			t.Fatalf("报告夹具处理错误: %s %v", item.Name, err)
		}
	}
	if _, err := decodeSubtaskReport(templates.InvalidJSON); err == nil {
		t.Fatal("不完整JSON被接收")
	}
}
