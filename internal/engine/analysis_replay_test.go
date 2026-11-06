package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type analysisQualityOracle struct {
	Cases []struct {
		ID      string `json:"id"`
		Sources []struct {
			Path  string `json:"path"`
			Line  int    `json:"line"`
			Quote string `json:"quote"`
		} `json:"sources"`
		Facts              []string `json:"facts"`
		SupportedInference string   `json:"supported_inference"`
		UnsupportedClaims  []string `json:"unsupported_claims"`
	} `json:"cases"`
}

func copyAnalysisQualityFixture(t *testing.T, dir string) (analysisFixtureManifest, analysisQualityOracle) {
	t.Helper()
	base := filepath.Join("testdata", "analysis_quality_v1")
	var fixture analysisFixtureManifest
	var oracle analysisQualityOracle
	for name, dst := range map[string]any{"manifest.json": &fixture, "oracle.json": &oracle} {
		raw, err := os.ReadFile(filepath.Join(base, name))
		if err != nil || json.Unmarshal(raw, dst) != nil {
			t.Fatalf("读取合成夹具失败 %s: %v", name, err)
		}
	}
	if !fixture.Synthetic || fixture.FileCount != 15 || len(fixture.Files) != 15 || fixture.TotalBytes != 87105 || len(oracle.Cases) != 3 {
		t.Fatal("准确性夹具的规模或脱敏声明错误")
	}
	for _, entry := range fixture.Files {
		raw, err := os.ReadFile(filepath.Join(base, "workspace", entry.Path))
		if err != nil || !filepath.IsLocal(entry.Path) || !utf8.Valid(raw) || len(raw) != entry.Bytes || fmt.Sprintf("%x", sha256.Sum256(raw)) != entry.SHA256 {
			t.Fatalf("夹具摘要或字符内容错误: %s (%v)", entry.Path, err)
		}
		path := filepath.Join(dir, entry.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range oracle.Cases {
		for _, source := range item.Sources {
			raw, err := os.ReadFile(filepath.Join(dir, source.Path))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(raw), "\n")
			if source.Line < 1 || source.Line > len(lines) || !strings.Contains(lines[source.Line-1], source.Quote) {
				t.Fatalf("oracle 引用行号不准确: %s %s:%d", item.ID, source.Path, source.Line)
			}
		}
	}
	return fixture, oracle
}

// 固定9ce成功任务的调用形状，保留复读和重复发现，避免为达降幅目标改变工具轨迹。
func frozenAnalysisTrajectory(t *testing.T, dir string, fixture analysisFixtureManifest) [][]schema.ToolCall {
	t.Helper()
	call := func(id, name string, args any) schema.ToolCall {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return schema.ToolCall{ID: id, Name: name, Arguments: raw}
	}
	trajectory := [][]schema.ToolCall{{call("find-1", "find_files", map[string]any{"name_glob": "*.md", "limit": 200})}}
	for batch := 0; batch < 3; batch++ {
		var calls []schema.ToolCall
		for _, file := range fixture.Files[batch*5 : (batch+1)*5] {
			calls = append(calls, call("read-"+strconv.Itoa(batch)+"-"+filepath.Base(file.Path), "read_file", map[string]any{"path": file.Path, "limit": 8000}))
		}
		trajectory = append(trajectory, calls)
	}
	var tails []schema.ToolCall
	for _, file := range fixture.Files {
		if file.Bytes <= 8000 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		offset := 8000
		for !utf8.Valid(raw[:offset]) {
			offset--
		}
		tails = append(tails, call("tail-"+filepath.Base(file.Path), "read_file", map[string]any{"path": file.Path, "offset": offset, "limit": 8000}))
	}
	search := func(id, query string) schema.ToolCall {
		return call(id, "search_text", map[string]any{"query": query, "name_glob": "*.md", "limit": 30})
	}
	trajectory = append(trajectory, append(tails, search("search-1", "status"), search("search-2", "score")))
	trajectory = append(trajectory, []schema.ToolCall{
		call("list-1", "list_dir", map[string]any{"path": "."}),
		search("search-3", "V1"), search("search-4", "V2"), search("search-5", "默认"), search("search-6", "测试"),
	})
	var rereads []schema.ToolCall
	for i, limit := range []int{5000, 2500, 2500, 2500} {
		rereads = append(rereads, call(fmt.Sprintf("reread-%d", i), "read_file", map[string]any{"path": fixture.Files[i].Path, "offset": 0, "limit": limit}))
	}
	trajectory = append(trajectory, append(rereads, call("find-2", "find_files", map[string]any{"name_glob": "*.md", "limit": 200})))
	var frozen struct {
		ModelCalls int                 `json:"model_calls"`
		ToolCalls  int                 `json:"tool_calls"`
		Rounds     [][]schema.ToolCall `json:"rounds"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "analysis_quality_v1", "trajectory.json"))
	if err != nil || json.Unmarshal(raw, &frozen) != nil || frozen.ModelCalls != 8 || frozen.ToolCalls != 30 || len(frozen.Rounds) != len(trajectory) {
		t.Fatalf("冻结轨迹记录无效: %v", err)
	}
	for i, round := range trajectory {
		if len(round) != len(frozen.Rounds[i]) {
			t.Fatalf("冻结第%d轮工具数改变", i+1)
		}
		for j, actual := range round {
			want := frozen.Rounds[i][j]
			var a, b any
			if json.Unmarshal(actual.Arguments, &a) != nil || json.Unmarshal(want.Arguments, &b) != nil {
				t.Fatal("冻结参数无效")
			}
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			if actual.ID != want.ID || actual.Name != want.Name || string(x) != string(y) {
				t.Fatalf("冻结第%d轮第%d个请求已改变", i+1, j+1)
			}
		}
	}
	return trajectory
}

func qualityReplayReport(ctx context.Context, oracle analysisQualityOracle) AnalysisReport {
	r := currentRun(ctx)
	r.mu.Lock()
	sources := make(map[string]AnalysisEvidence)
	for id, entry := range r.analysisRuntime.sources {
		sources[id] = entry
	}
	r.mu.Unlock()
	ids := sortedAnalysisIDs(sources)
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(ids[i], "E"))
		b, _ := strconv.Atoi(strings.TrimPrefix(ids[j], "E"))
		return a < b
	})
	report := AnalysisReport{SchemaVersion: 1, Summaries: make(map[string]string)}
	for _, id := range ids {
		report.ProcessedReads = append(report.ProcessedReads, id)
		report.Summaries[id] = "已检查本段合成课程材料，核心原文事实见发现；未执行任何示例脚本。"
	}
	for _, item := range oracle.Cases {
		finding := AnalysisFinding{ID: item.ID, Kind: "fact", Statement: strings.Join(item.Facts, "；"), Context: "仅限已读取的合成材料版本", Limitations: "未运行示例命令；引用有效不证明推断已经验收。"}
		for _, ref := range item.Sources {
			for _, id := range ids {
				entry := sources[id]
				if entry.Receipt.Path == ref.Path && entry.Receipt.FirstLine <= ref.Line && entry.Receipt.LastLine >= ref.Line && bytes.Contains(entry.Source, []byte(ref.Quote)) {
					finding.Evidence = append(finding.Evidence, AnalysisCitation{EvidenceID: id, StartLine: ref.Line, EndLine: ref.Line, Quote: ref.Quote})
					break
				}
			}
		}
		if len(finding.Evidence) != len(item.Sources) {
			continue
		}
		report.Findings = append(report.Findings, finding, AnalysisFinding{ID: item.ID + "-inference", Kind: "inference", Statement: item.SupportedInference, DependsOn: []string{item.ID}, Limitations: "分析推断，尚未实测。"})
		report.OverviewIDs = append(report.OverviewIDs, item.ID)
	}
	report.Unverified = []string{"示例脚本、迁移和插件兼容性未实际执行验证。"}
	return report
}

type replayMaterial struct {
	TotalBytes    int64  `json:"request_material_bytes"`
	ReadResults   int64  `json:"read_result_content_bytes"`
	SchemaBytes   int64  `json:"schema_bytes"`
	SourceBytes   *int64 `json:"observed_source_bytes"`
	NotesBytes    int64  `json:"observed_notes_bytes"`
	EvidenceBytes int64  `json:"observed_evidence_bytes"`
	FoldedReads   int64  `json:"folded_reads"`
	ModelCalls    int    `json:"model_calls"`
	ToolCalls     int    `json:"tool_calls"`
}

func TestAnalysisFrozenQualityReplayMaterialComparison(t *testing.T) {
	t.Setenv("CLAW_OBSERVE", "on")
	root := t.TempDir()
	var measured [2]replayMaterial
	var signatures [2][]string
	for arm, enabled := range []bool{false, true} {
		name := []string{"baseline", "analysis"}[arm]
		dir, observeDir := filepath.Join(root, name), filepath.Join(root, name+"-observe")
		fixture, oracle := copyAnalysisQualityFixture(t, dir)
		trajectory := frozenAnalysisTrajectory(t, dir, fixture)
		t.Setenv("CLAW_OBSERVE_DIR", observeDir)
		calls := 0
		model := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
			calls++
			if enabled && calls == 8 {
				// 脚本响应不会衡量模型推理，故另查真正送入最终请求的决定性原文，
				// 避免仅由测试读取运行缓存生成报告而掩盖上下文丢失。
				for _, item := range oracle.Cases {
					for _, source := range item.Sources {
						encoded, _ := json.Marshal(source.Quote)
						if len(messages) == 0 || !strings.Contains(messages[0].Content, string(encoded[1:len(encoded)-1])) {
							return nil, fmt.Errorf("最终请求丢失决定性原文 %s:%d", source.Path, source.Line)
						}
					}
				}
			}
			material, err := json.Marshal(struct {
				Messages []schema.Message
				Tools    []schema.ToolDefinition
			}{messages, defs})
			if err != nil {
				return nil, err
			}
			measured[arm].TotalBytes += int64(len(material))
			schemaJSON, _ := json.Marshal(defs)
			measured[arm].SchemaBytes += int64(len(schemaJSON))
			readIDs := make(map[string]bool)
			for _, msg := range messages {
				for _, tool := range msg.ToolCalls {
					readIDs[tool.ID] = tool.Name == "read_file"
				}
			}
			for _, msg := range messages {
				if readIDs[msg.ToolCallID] {
					measured[arm].ReadResults += int64(len(msg.Content))
				}
			}
			text := "继续按冻结轨迹读取和核对材料。"
			if enabled {
				report := qualityReplayReport(ctx, oracle)
				body, err := json.Marshal(report)
				if err != nil {
					return nil, err
				}
				text = string(body)
			}
			if calls > len(trajectory) {
				if !enabled {
					text = "材料区分V1与V2，需补齐迁移说明；score使用.get默认值；已有测试命令，但未实际执行。"
				}
				return reply(text)
			}
			for _, tool := range trajectory[calls-1] {
				signatures[arm] = append(signatures[arm], tool.Name+" "+string(tool.Arguments))
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: text, ToolCalls: trajectory[calls-1]}, nil
		})
		eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{MaxContextBytes: 300000, Budget: Budget{MaxModelCalls: 60, MaxToolCalls: 120, MaxTurns: 30}})
		spec := TaskSpec{Goal: "读取并分析当前文件夹的15份Markdown文档，引用原文并区分事实、版本迁移、默认行为与待实测结论。", Analysis: enabled}
		ctx, attempt := observe.StartFromEnv(context.Background(), eng.ObservationOptions(dir, "offline-quality-replay", spec))
		result, err := eng.Run(ctx, ctxpkg.NewSession("冻结质量轨迹", dir), spec, nil)
		attempt.Returned(ObservationResult(result, err))
		attempt.Close()
		if err != nil || result.Status != Answered || calls != 8 || result.Usage.ModelCalls != 8 || result.Usage.ToolCalls != 30 {
			t.Fatalf("%s 未保持冻结轨迹: result=%+v calls=%d err=%v", name, result, calls, err)
		}
		measured[arm].ModelCalls, measured[arm].ToolCalls = result.Usage.ModelCalls, result.Usage.ToolCalls
		if enabled {
			if result.Analysis == nil || len(result.Analysis.Findings) != 6 || len(result.Analysis.OverviewIDs) != 3 {
				t.Fatalf("压缩后核心标注事实或推断丢失: %+v", result.Analysis)
			}
			for _, finding := range result.Analysis.Findings {
				if finding.Status != "evidence_valid" || finding.Limitations == "" {
					t.Fatalf("未保持引用与未实测边界: %+v", finding)
				}
			}
		}
		paths, err := filepath.Glob(filepath.Join(observeDir, "*", "events.jsonl"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("缺少完整离线观测: %v %v", paths, err)
		}
		raw, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var event struct {
				EventType string `json:"event_type"`
				Payload   struct {
					Source   int64 `json:"source_bytes"`
					Notes    int64 `json:"analysis_bytes"`
					Evidence int64 `json:"evidence_bytes"`
					Folded   int64 `json:"folded_reads"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &event) == nil && event.EventType == "model_started" {
				if enabled {
					if measured[arm].SourceBytes == nil {
						measured[arm].SourceBytes = new(int64)
					}
					*measured[arm].SourceBytes += event.Payload.Source
				}
				measured[arm].NotesBytes += event.Payload.Notes
				measured[arm].EvidenceBytes += event.Payload.Evidence
				measured[arm].FoldedReads += event.Payload.Folded
			}
		}
	}
	baseline, candidate := measured[0], measured[1]
	if strings.Join(signatures[0], "\n") != strings.Join(signatures[1], "\n") || len(signatures[0]) != 30 {
		t.Fatal("对照工具轨迹不一致")
	}
	reduction := float64(baseline.TotalBytes-candidate.TotalBytes) / float64(baseline.TotalBytes)
	raw, _ := json.Marshal(struct {
		Baseline     replayMaterial `json:"baseline"`
		Analysis     replayMaterial `json:"analysis"`
		Reduction    float64        `json:"request_material_reduction"`
		GoalMet      bool           `json:"offline_goal_30_percent_met"`
		ActualTokens any            `json:"actual_tokens"`
	}{baseline, candidate, reduction, reduction >= .30, nil})
	t.Logf("冻结轨迹离线材料对照（不代表真实Token或语义收益）: %s", raw)
	if candidate.FoldedReads == 0 || candidate.ReadResults >= baseline.ReadResults {
		t.Fatal("没有在有效笔记后实际折叠已处理正文")
	}
	// v2 把 Progress 去重也应用到普通模式，此处现在是新模式之间的差异，
	// 不代表旧版→新版整任务消耗。保留全部原始统计和30%指示，真实对照仍单独执行30%门槛。
	if candidate.TotalBytes >= baseline.TotalBytes {
		t.Fatalf("相同轨迹下有效分析笔记未减少重复材料：%d >= %d", candidate.TotalBytes, baseline.TotalBytes)
	}
}
