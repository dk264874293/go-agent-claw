package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type candidateProgress struct {
	Previous      []string `json:"previously_delivered"`
	Current       []string `json:"delivered_this_request"`
	Total         int      `json:"unprocessed_read_count"`
	PreviousCount int      `json:"previously_delivered_count"`
	CurrentCount  int      `json:"delivered_this_request_count"`
	Omitted       int      `json:"omitted_reads"`
}

func requestCandidateProgress(t *testing.T, messages []schema.Message) candidateProgress {
	t.Helper()
	for _, message := range messages {
		if message.Role != schema.RoleSystem {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			if !strings.HasPrefix(line, "读取处理进度（previously_delivered") {
				continue
			}
			var result candidateProgress
			start := strings.Index(line, "：")
			if start < 0 || json.Unmarshal([]byte(line[start+len("："):]), &result) != nil {
				t.Fatalf("候选目录不是完整JSON: %s", line)
			}
			return result
		}
	}
	t.Fatal("普通v2请求缺少双候选目录")
	return candidateProgress{}
}

func TestWorkNotesCandidatesIncludeCurrentRequestWithoutPrematureQualification(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	eng := NewAgentEngine(nil, nil, EngineOptions{})
	prepared, delivered, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	progress := requestCandidateProgress(t, prepared)
	if len(progress.Previous) != 0 || strings.Join(progress.Current, ",") != "E1,E2" || progress.Total != 2 || stats.unprocessed != 2 || stats.notesThisRequest != 2 || stats.notesPreviouslyDelivered != 0 {
		t.Fatalf("首轮正文没有同响应处理入口: %+v %+v", progress, stats)
	}
	if len(r.analysisRuntime.delivered["root"]) != 0 || len(r.state.ReadMemory.ProcessedReads["root"]) != 0 || stats.folded != 0 {
		t.Fatal("候选展示提前授予交付、处理或折叠资格")
	}
	// 失败的模型响应不能使目录候选获得处理资格。
	call := carrierCall("before-success", map[string]any{"schema_version": 1, "processed_reads": []string{"E1"}, "summaries": map[string]string{"E1": "有内容也还未成功交付"}})
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{call})
	if err != nil || batch.valid || batch.response.ProcessedSources != 0 {
		t.Fatalf("未成功交付的本次候选被采纳: %+v %v", batch, err)
	}
	r.markAnalysisDelivered(ctx, delivered)
	batch, err = r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("after-success", map[string]any{"schema_version": 1, "processed_reads": []string{"E1"}, "summaries": map[string]string{"E1": "来源正文已进入本次成功请求"}})})
	if err != nil || !batch.valid || batch.response.ProcessedSources != 1 || !containsString(r.state.ReadMemory.ProcessedReads["root"], "E1") {
		t.Fatalf("成功响应的最小摘要载体未被采纳: %+v %v", batch, err)
	}
}

func TestWorkNotesCandidatesUseOnlyPresentOwnFullSourcesAndBoundDirectory(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	r.markAnalysisDelivered(ctx, []string{"E1"})
	prepared, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	progress := requestCandidateProgress(t, prepared)
	if strings.Join(progress.Previous, ",") != "E1" || strings.Join(progress.Current, ",") != "E2" || stats.notesPreviouslyDelivered != 1 || stats.notesThisRequest != 1 {
		t.Fatalf("历史与本次候选没有互斥去重: %+v %+v", progress, stats)
	}
	// 有结构化证据但消息不在请求中，不能声称本次交付。
	prepared, _, stats, err = NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages[:4], nil)
	if err != nil || stats.notesThisRequest != 0 || requestCandidateProgress(t, prepared).Total != 1 {
		t.Fatalf("未实际交付消息获得本次候选: %+v %v", stats, err)
	}
	valid := map[string]AnalysisEvidence{}
	var current []string
	for i := 0; i < 120; i++ {
		id := strings.Repeat("x", 90) + string(rune('A'+i/26)) + string(rune('A'+i%26))
		x := r.analysisRuntime.sources["E1"]
		x.ID = id
		valid[id], current = x, append(current, id, id)
	}
	foreign := r.analysisRuntime.sources["E2"]
	foreign.AgentID = "child"
	valid["foreign"] = foreign
	current = append(current, "foreign")
	short := r.analysisRuntime.sources["E2"]
	short.Source = short.Source[:1]
	valid["partial"] = short
	current = append(current, "partial")
	encoded, previous, now := workNotesProcessingProgress(valid, nil, current, nil, nil, "root")
	if json.Unmarshal(encoded, &progress) != nil || len(encoded) > 4*1024 || previous != 0 || now != 120 || progress.Total != 120 || progress.Omitted == 0 || len(progress.Current)+progress.Omitted != 120 {
		t.Fatalf("候选范围、去重或完整条目限额错误: count=%d/%d bytes=%d %+v", previous, now, len(encoded), progress)
	}
}

func TestWorkNotesSchemaSummaryDoesNotRequireEmptyFindings(t *testing.T) {
	_, r := carrierFixture(t)
	definition := schema.ToolDefinition{Name: "read_file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}
	projected := r.withWorkNotesCarrier(definition)
	carrier := projected.InputSchema.(map[string]any)["properties"].(map[string]any)["_work_notes"].(map[string]any)
	if required := carrier["required"].([]string); len(required) != 1 || required[0] != "schema_version" {
		t.Fatalf("最小摘要仍须填空findings: %+v", required)
	}
	if !strings.Contains(workNotesCarrierPrompt, "没有候选时省略载体") || strings.Contains(workNotesCarrierPrompt, `"findings":[]`) {
		t.Fatal("最小示例仍诱导空更新")
	}
}

func TestWorkNotesStructuredReceiptAndResponseCountsReflectValidChanges(t *testing.T) {
	ctx, r := carrierFixture(t)
	valid := carrierCall("valid", carrierSummary("E1", "实际材料中的范围与条件"))
	duplicate := valid
	duplicate.ID = "duplicate"
	deferred := carrierCall("defer", AnalysisReport{SchemaVersion: 1, DeferredReads: []DeferredRead{{EvidenceID: "E2", Reason: "需要核对版本条件"}}})
	invalid := carrierCall("invalid", carrierSummary("E99", "不是当前来源"))
	noCarrier := readCall("no-notes", "E2.md")
	batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{valid, duplicate, deferred, invalid, noCarrier})
	if err != nil || batch.response.Submitted != 4 || batch.response.Accepted != 2 || batch.response.Duplicates != 1 || batch.response.Rejected != 1 || batch.response.ProcessedSources != 1 || batch.response.DeferredSources != 1 || batch.response.Outcome != "accepted" {
		t.Fatalf("载体或唯一有效来源统计错误: %+v %v", batch.response, err)
	}
	for i, receipt := range batch.receipts[:4] {
		var detail workNotesCarrierReceipt
		if len(receipt) > 512 || json.Unmarshal([]byte(strings.TrimPrefix(receipt, "工作笔记回执：")), &detail) != nil || strings.Contains(receipt, "实际材料") {
			t.Fatalf("回执不完整、超限或泄露摘要: %s", receipt)
		}
		if i == 0 && (strings.Join(detail.Processed, ",") != "E1" || detail.Status != "accepted") || i == 3 && (strings.Join(detail.Unprocessed, ",") != "E99" || detail.Status != "rejected") {
			t.Fatalf("回执没有明确实际处理与缺口: %+v", detail)
		}
	}
	batch, err = r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("same-source-again", carrierSummary("E1", "更新摘要但不是新处理来源"))})
	if err != nil || batch.response.ProcessedSources != 0 {
		t.Fatalf("旧处理状态误计新增来源: %+v %v", batch.response, err)
	}
	var ids []string
	for i := 0; i < 80; i++ {
		ids = append(ids, strings.Repeat("证据", 20))
	}
	receipt := boundWorkNotesReceipt(workNotesCarrierReceipt{Status: "accepted", Reason: "accepted", Accepted: ids, Processed: ids})
	var detail workNotesCarrierReceipt
	if len(receipt) > 512 || json.Unmarshal([]byte(strings.TrimPrefix(receipt, "工作笔记回执：")), &detail) != nil || detail.Omitted == 0 {
		t.Fatalf("大回执没有按完整条目省略: %s", receipt)
	}
}

func TestWorkNotesAdaptiveProviderAdoptsFirstExposureAndActuallyFolds(t *testing.T) {
	dir := t.TempDir()
	paths := []string{"first.md", "second.md", "third.md"}
	for _, path := range paths {
		writeFixture(t, dir, path, "明确事实："+path+"属于当前冻结版本，结论仅限静态阅读。\n"+strings.Repeat("来源正文保留。", 100))
	}
	requests, nextRead, written, folds := 0, 0, false, 0
	model := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		progress := requestCandidateProgress(t, messages)
		for _, message := range messages {
			if message.RequestRepresentation == "folded_marker" {
				folds++
			}
		}
		if written {
			return reply("已完成实际来源汇总")
		}
		parameters := map[string]any{}
		tool := "read_file"
		if nextRead < len(paths) {
			parameters["path"] = paths[nextRead]
			nextRead++
		} else {
			tool, written = "write_file", true
			parameters["path"], parameters["content"] = "result.md", "# 已核对三份材料\n"
		}
		candidates := append(append([]string(nil), progress.Previous...), progress.Current...)
		if len(candidates) > 0 {
			summaries := map[string]string{}
			for _, id := range candidates {
				for _, message := range messages {
					if message.ReadEvidenceID != id || message.RequestRepresentation == "folded_marker" {
						continue
					}
					for _, line := range strings.Split(message.Content, "\n") {
						if strings.HasPrefix(line, "明确事实：") {
							summaries[id] = line
						}
					}
				}
				if summaries[id] == "" {
					t.Fatalf("目录列出的候选没有实际完整正文: %s", id)
				}
			}
			// 从实际请求目录取ID与事实，不依赖预制的E1/E2或引擎内部材料。
			parameters["_work_notes"] = map[string]any{"schema_version": 1, "processed_reads": candidates, "summaries": summaries}
		} else if requests == 1 && !strings.Contains(messages[0].Content, "本轮没有可记的读取候选") {
			t.Fatal("发现读取前没有省略空笔记的提示")
		}
		raw, _ := json.Marshal(parameters)
		return modelCalls(schema.ToolCall{ID: paths[min(nextRead, len(paths))-1] + tool, Name: tool, Arguments: raw})
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("动态笔记", dir), TaskSpec{Goal: "汇总三份来源且只新增result.md", Mode: tools.ModeWorkspace, Kind: TaskChange}, nil)
	if err != nil || result.Status != NeedsVerification || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 4 || requests != 5 || folds == 0 {
		t.Fatalf("自适应笔记未实际折叠或额外调用: %+v requests=%d folds=%d %v", result, requests, folds, err)
	}
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	processed, summaries := 0, 0
	for _, ids := range state.ReadMemory.ProcessedReads {
		processed += len(ids)
	}
	for _, items := range state.ReadMemory.Summaries {
		summaries += len(items)
	}
	if processed != 3 || summaries != 3 {
		t.Fatalf("没有保存实际核对的三个来源: %+v", state.ReadMemory)
	}
}

func TestWorkNotesFailedFirstExposureNeverGrantsProcessing(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "尚未成功交付的材料\n")
	requests := 0
	model := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		if requests == 1 {
			return modelCalls(readCall("read", "source.md"))
		}
		if progress := requestCandidateProgress(t, messages); len(progress.Current) != 1 || progress.Total != 1 {
			t.Fatalf("首次交付请求没有候选: %+v", progress)
		}
		return nil, errors.New("离线注入模型失败")
	})
	result, _ := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("交付失败", dir), TaskSpec{Goal: "读取来源", Mode: tools.ModeWorkspace, Kind: TaskChange}, nil)
	state := loadAnalysisIntegrationState(t, dir, result.TaskID)
	if result.Status != Failed || result.Usage.ModelCalls != 2 || result.Usage.ToolCalls != 1 || len(state.ReadMemory.ProcessedReads) != 0 || len(state.ReadMemory.Summaries) != 0 {
		t.Fatalf("失败请求授予处理或发生额外调用: %+v %+v", result, state.ReadMemory)
	}
	if _, err := os.Stat(filepath.Join(dir, "source.md")); err != nil {
		t.Fatal(err)
	}
}
