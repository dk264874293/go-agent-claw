package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadingProgressOnlyListsOwnSuccessfulUnprocessedReads(t *testing.T) {
	valid := map[string]AnalysisEvidence{}
	for _, id := range []string{"E1", "E2", "E3", "E4", "E5"} {
		valid[id] = AnalysisEvidence{ID: id, AgentID: "root", Source: []byte("原文不能进入进度目录"), Receipt: schema.ReadReceipt{ByteStart: 10, ByteEnd: 20, FirstLine: 2, LastLine: 3, TotalBytes: 30}}
	}
	child := valid["E4"]
	child.AgentID = "child"
	valid["E4"] = child
	full := valid["E5"]
	full.Receipt.ByteStart, full.Receipt.ByteEnd = 0, 30
	valid["E5"] = full
	encoded, total := readingProcessingProgress(valid, map[string]bool{"E1": true, "E3": true, "E4": true, "E5": true}, map[string]bool{"E3": true}, []AnalysisReport{{Unverified: []string{"已有未解决项"}, UnverifiedDropped: 2}}, "root")
	var progress struct {
		Reads []struct {
			ID       string `json:"id"`
			Start    int64  `json:"byte_start"`
			Complete bool   `json:"receipt_has_complete_file"`
		} `json:"reads"`
		Unresolved int `json:"projected_unresolved_count"`
		Dropped    int `json:"recorded_unresolved_omitted"`
	}
	if err := json.Unmarshal(encoded, &progress); err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(progress.Reads) != 2 || progress.Reads[0].ID != "E1" || progress.Reads[1].ID != "E5" || progress.Reads[0].Complete || !progress.Reads[1].Complete || progress.Reads[0].Start != 10 {
		t.Fatalf("未交付、已处理、子Agent材料进入待处理目录，或范围失真: %s", encoded)
	}
	if progress.Unresolved != 1 || progress.Dropped != 2 || strings.Contains(string(encoded), "原文不能进入") || strings.Contains(string(encoded), "已有未解决项") {
		t.Fatalf("未解决项计数失真或重复注入正文: %s", encoded)
	}
}

func TestReadingProgressBoundPreservesTotalAndWholeEntries(t *testing.T) {
	valid, delivered := map[string]AnalysisEvidence{}, map[string]bool{}
	for i := 1; i <= 128; i++ {
		id := fmt.Sprintf("E%d", i)
		valid[id] = AnalysisEvidence{ID: id, AgentID: "root", Receipt: schema.ReadReceipt{ByteEnd: 10, TotalBytes: 10}}
		delivered[id] = true
	}
	encoded, total := readingProcessingProgress(valid, delivered, nil, nil, "root")
	var progress struct {
		Reads   []json.RawMessage `json:"reads"`
		Omitted int               `json:"omitted_reads"`
		Total   int               `json:"unprocessed_read_count"`
	}
	if err := json.Unmarshal(encoded, &progress); err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 || total != 128 || progress.Total != 128 || progress.Omitted == 0 || len(progress.Reads)+progress.Omitted != 128 {
		t.Fatalf("有界目录丢失条目计数: %s", encoded)
	}
}

func TestNoteFeedbackEligibilityAndSoftBudgetKeepBodies(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	eng := NewAgentEngine(nil, nil, EngineOptions{MaxContextBytes: 100000, AnalysisContextBytes: 4096})
	prepared, _, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.unprocessed != 0 || !stats.over || !strings.Contains(prepared[0].Content, "已超过软目标") || !strings.Contains(prepared[0].Content, "不为压缩新增模型调用") {
		t.Fatalf("新原文尚未成功交付就授予待处理资格，或软目标没有反馈: %+v", stats)
	}
	r.markAnalysisDelivered(ctx, []string{"E1"})
	prepared, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.unprocessed != 1 || stats.folded != 0 || stats.foldedSource != 0 {
		t.Fatalf("成功交付后的待处理或折叠计数错误: %+v", stats)
	}
	for _, index := range []int{3, 5} {
		if prepared[index].Content != messages[index].Content {
			t.Fatal("软目标反馈删除或截断了必要原文")
		}
	}
	if err := os.WriteFile(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "E1.md"), []byte("版本变化\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.unprocessed != 0 {
		t.Fatalf("陈旧证据仍保留待处理资格: %+v %v", stats, err)
	}
}

func TestFoldStatsRequireSuccessfulDeliveryAndProjectedNotes(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	r.state.Analysis.Processed["root"] = []string{"E1", "E2"}
	r.state.Analysis.Summaries["root"] = map[string]string{"E1": "有效来源摘要", "E2": "最新读取摘要"}
	eng := NewAgentEngine(nil, nil, EngineOptions{})
	prepared, _, stats, err := eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 0 || prepared[3].Content != messages[3].Content {
		t.Fatalf("没有成功交付记录却折叠了正文: %+v %v", stats, err)
	}
	r.markAnalysisDelivered(ctx, []string{"E1", "E2"})
	prepared, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 1 || stats.foldedSource != int64(len(r.analysisRuntime.sources["E1"].Source)) || prepared[5].Content != messages[5].Content {
		t.Fatalf("折叠字节或最新批次保护错误: %+v %v", stats, err)
	}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("E%d", i)
		x := r.analysisRuntime.sources["E1"]
		x.ID, x.Receipt.EvidenceID = id, id
		r.analysisRuntime.sources[id] = x
		r.state.Analysis.Summaries["root"][id] = strings.Repeat("长摘要", 300)
	}
	prepared, _, stats, err = eng.prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.omittedNotes == 0 || stats.folded != 0 || prepared[3].Content != messages[3].Content {
		t.Fatalf("笔记省略后原文仍被折叠，或省略计数缺失: %+v %v", stats, err)
	}
}

func TestOutputStylePreservesOldPolicyAndArtifactCompleteness(t *testing.T) {
	if outputStylePrompt(TaskSpec{}) != "" {
		t.Fatal("历史空配置被自动改成精简")
	}
	for _, style := range []string{"concise", "detailed"} {
		prompt := outputStylePrompt(TaskSpec{OutputStyle: style})
		if !strings.Contains(prompt, "不截断代码产物、业务数据或证据") {
			t.Fatalf("%s改变了产物完整性约束: %s", style, prompt)
		}
	}
}
