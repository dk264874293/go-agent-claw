package engine

import (
	"strings"
	"testing"
)

func TestPartialReportFormattingKeepsFailureAndSource(t *testing.T) {
	for _, source := range []struct{ code, label string }{{"model", "模型收尾汇总"}, {"recorded", "已记录事实摘要"}} {
		t.Run(source.code, func(t *testing.T) {
			result := RunResult{TaskID: "分析任务", Status: BudgetExhausted, StopCode: "tool_limit", Reason: "工具额度耗尽",
				FinalText: "不合格的最终答复", PartialReport: &PartialReport{Text: "已读取三份文档，尚缺交叉核对。", Source: source.code, OriginRunID: "source-run"}}
			text := FormatRunResult(result)
			for _, want := range []string{"BUDGET_EXHAUSTED", "tool_limit", "部分分析／任务未完成", source.label, "source-run", result.PartialReport.Text} {
				if !strings.Contains(text, want) {
					t.Fatalf("部分分析缺少 %q: %s", want, text)
				}
			}
			if strings.Contains(text, result.FinalText) || result.ExitCode() != 3 {
				t.Fatal("部分分析改变终态或错误文字成为终答")
			}
		})
	}
}

func TestPartialReportStorageWarningAndSuccessfulResultIsolation(t *testing.T) {
	result := RunResult{Status: Failed, StopCode: "storage_error", PartialReport: &PartialReport{Text: "仅现场记录", Source: "recorded"}}
	text := FormatRunResult(result)
	if !strings.Contains(text, "任务记录未可靠保存") || !strings.Contains(text, "仅现场记录") || strings.Index(text, "任务记录未可靠保存") > strings.Index(text, "仅现场记录") {
		t.Fatal("存储失败的现场分析缺少前置说明", text)
	}
	result.Status, result.StopCode, result.FinalText = Answered, "answered", "本次正式答复"
	text = FormatRunResult(result)
	if strings.Contains(text, "仅现场记录") || !strings.Contains(text, "本次正式答复") {
		t.Fatal("旧部分分析污染正常完成结果", text)
	}
}

func TestPartialReportMarksHistoricalOrigin(t *testing.T) {
	result := RunResult{RunID: "current-run", Status: BudgetExhausted, StopCode: "tool_limit",
		PartialReport: &PartialReport{Text: "当时已读取指定文件。", Source: "model", OriginRunID: "old-run"}}
	text := FormatRunResult(result)
	if !strings.Contains(text, "历史来源，文件当前状态须重新核实") || !strings.Contains(text, "old-run") {
		t.Fatal("恢复复用的报告被展示为当前文件事实", text)
	}
}
