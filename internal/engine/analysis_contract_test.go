package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func analysisTestEvidence(source string, firstLine int, byteStart int64) AnalysisEvidence {
	lines := strings.Count(source, "\n")
	if strings.HasSuffix(source, "\n") {
		lines--
	}
	return AnalysisEvidence{
		ID: "E1", ActionID: "action1", AgentID: "agent1", RunID: "run1", ArtifactID: "artifact1", Source: []byte(source),
		Receipt: schema.ReadReceipt{Path: "docs/a.md", Version: "sha256:version1", ByteStart: byteStart, ByteEnd: byteStart + int64(len(source)), TotalBytes: byteStart + int64(len(source)) + 20, FirstLine: firstLine, LastLine: firstLine + lines, ActionID: "action1", AgentID: "agent1"},
	}
}

func analysisTestReport(quote string) AnalysisReport {
	return AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "原文描述版本迁移", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 3, EndLine: 3, Quote: quote}}}}}
}

func TestAnalysisCitationUsesExactDeliveredBytes(t *testing.T) {
	evidence := analysisTestEvidence("前言\nV2 采用新格式。\n结尾\n", 2, 10)
	ref := AnalysisCitation{EvidenceID: "E1", StartLine: 3, EndLine: 3, Quote: "V2 采用新格式。", Path: "evil.md", Version: "伪造", ByteStart: 999, ArtifactID: "伪造", Status: "VERIFIED"}
	checked, err := ValidateAnalysisCitation(ref, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Path != "docs/a.md" || checked.Version != evidence.Receipt.Version || checked.ByteStart != 10+int64(len("前言\n")) || checked.ByteEnd != checked.ByteStart+int64(len(ref.Quote)) || checked.ArtifactID != "artifact1" || checked.Status != "evidence_valid" {
		t.Fatalf("引用未按实际原文重建：%+v", checked)
	}
	for _, test := range []struct {
		name   string
		mutate func(*AnalysisCitation, *AnalysisEvidence)
	}{
		{"错行", func(c *AnalysisCitation, e *AnalysisEvidence) { c.StartLine = 2; c.EndLine = 2 }},
		{"错原文", func(c *AnalysisCitation, e *AnalysisEvidence) { c.Quote = "V1 采用新格式。" }},
		{"不存在的证据ID", func(c *AnalysisCitation, e *AnalysisEvidence) { c.EvidenceID = "E2" }},
		{"字节缺口", func(c *AnalysisCitation, e *AnalysisEvidence) { e.Receipt.ByteEnd++ }},
		{"非UTF8原文", func(c *AnalysisCitation, e *AnalysisEvidence) { e.Source[0] = 0xff }},
		{"缺不可变材料", func(c *AnalysisCitation, e *AnalysisEvidence) { e.ArtifactID = "" }},
		{"错误动作归属", func(c *AnalysisCitation, e *AnalysisEvidence) { e.Receipt.ActionID = "other" }},
		{"路径越界", func(c *AnalysisCitation, e *AnalysisEvidence) { e.Receipt.Path = "../a.md" }},
		{"空摘录", func(c *AnalysisCitation, e *AnalysisEvidence) { c.Quote = " \n " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := ref
			e := evidence
			e.Source = append([]byte(nil), e.Source...)
			test.mutate(&c, &e)
			if _, err := ValidateAnalysisCitation(c, e); err == nil {
				t.Fatal("无效引用被接受")
			}
		})
	}
}

func TestAnalysisCitationPartialLineAndAmbiguity(t *testing.T) {
	evidence := analysisTestEvidence("中间已读取的中文片段", 7, 30)
	for _, quote := range []string{"行首未读中间已读取的中文片段", "中间已读取的中文片段行尾未读"} {
		if _, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: "E1", StartLine: 7, EndLine: 7, Quote: quote}, evidence); err == nil {
			t.Fatal("半行扩展到未交付内容")
		}
	}
	if _, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: "E1", StartLine: 7, EndLine: 7, Quote: "已读取的中文"}, evidence); err != nil {
		t.Fatal(err)
	}
	evidence = analysisTestEvidence("相同文字\n相同文字\n", 1, 0)
	if _, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: "E1", StartLine: 1, EndLine: 2, Quote: "相同文字"}, evidence); err == nil {
		t.Fatal("歧义摘录被接受")
	}
	checked, err := ValidateAnalysisCitation(AnalysisCitation{EvidenceID: "E1", StartLine: 2, EndLine: 2, Quote: "相同文字\n"}, evidence)
	if err != nil || checked.StartLine != 2 || checked.EndLine != 2 {
		t.Fatalf("明确行范围应消除歧义：%+v %v", checked, err)
	}
}

func TestAnalysisReportDowngradesInvalidFindingsAndDependencies(t *testing.T) {
	evidence := analysisTestEvidence("前言\nV2 采用新格式。\n结尾\n", 2, 0)
	lookup := func(id string) (AnalysisEvidence, error) {
		if id != "E1" {
			return AnalysisEvidence{}, errors.New("没有读取或当前版本已失效")
		}
		return evidence, nil
	}
	report := analysisTestReport("V2 采用新格式。")
	report.Findings = append(report.Findings,
		AnalysisFinding{ID: "F2", Kind: "fact", Statement: "错误原文结论", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 3, EndLine: 3, Quote: "不存在"}}},
		AnalysisFinding{ID: "F3", Kind: "inference", Statement: "基于错误结论", DependsOn: []string{"F2"}},
		AnalysisFinding{ID: "F4", Kind: "inference", Statement: "属于版本迁移缺口", DependsOn: []string{"F1"}, Context: "V1→V2", Limitations: "仅静态核对"},
		AnalysisFinding{ID: "F5", Kind: "recommendation", Statement: "补充迁移说明", DependsOn: []string{"F4"}},
		AnalysisFinding{ID: "F6", Kind: "unverified", Statement: "未执行兼容性测试"},
	)
	report.OverviewIDs = []string{"F1", "F3", "F4", "F1"}
	report.ProcessedReads = []string{"E1", "E1", "E2"}
	report.Summaries = map[string]string{"E1": "已阅读材料的版本背景", "E2": "凭空声明"}
	checked, err := ValidateAnalysisReport(report, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(checked.Findings) != 3 || len(checked.Unverified) != 4 || strings.Join(checked.OverviewIDs, ",") != "F1,F4" || strings.Join(checked.ProcessedReads, ",") != "E1" || len(checked.Summaries) != 1 {
		t.Fatalf("降级或声明筛选不正确：%+v", checked)
	}
	for _, finding := range checked.Findings {
		if finding.Status != "evidence_valid" {
			t.Fatal("未设置机械校验状态")
		}
	}
	text := RenderAnalysis(checked)
	for _, want := range []string{"材料事实", "分析推断", "建议", "待核实", "版本迁移缺口", "docs/a.md:3", "未经过独立语义验收"} {
		if !strings.Contains(text, want) {
			t.Fatalf("统一展示缺少 %s", want)
		}
	}
}

func TestAnalysisReportCyclesAndAllInvalidAreBlocked(t *testing.T) {
	report := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{
		{ID: "F1", Kind: "inference", Statement: "循环一", DependsOn: []string{"F2"}},
		{ID: "F2", Kind: "inference", Statement: "循环二", DependsOn: []string{"F1"}},
		{ID: "F3", Kind: "fact", Statement: "没有原文依据"},
	}}
	checked, err := ValidateAnalysisReport(report, nil)
	if !errors.Is(err, errAnalysisReportInvalid) || len(checked.Findings) != 0 || len(checked.Unverified) != 3 {
		t.Fatalf("无依据报告被接受：%+v %v", checked, err)
	}
	data, _ := json.Marshal(checked)
	if _, err := ParseAnalysisReport(string(data)); err != nil {
		t.Fatalf("降级后报告必须可持久化读取：%v", err)
	}
}

func TestAnalysisReportSummaryOnlyIntermediateRetainsProcessed(t *testing.T) {
	evidence := analysisTestEvidence("材料无相关内容", 1, 0)
	report := AnalysisReport{SchemaVersion: 1, ProcessedReads: []string{"E1", "E2"}, Summaries: map[string]string{"E1": "已检查本片段，未发现所问的版本说明"}}
	checked, err := ValidateAnalysisReport(report, func(id string) (AnalysisEvidence, error) {
		if id == "E1" {
			return evidence, nil
		}
		return AnalysisEvidence{}, errors.New("无读取")
	})
	if !errors.Is(err, errAnalysisReportInvalid) || len(checked.ProcessedReads) != 1 || checked.Summaries["E1"] == "" {
		t.Fatalf("中间摘要应保留而不能冒充最终报告：%+v %v", checked, err)
	}
	report.Summaries = nil
	checked, _ = ValidateAnalysisReport(report, func(id string) (AnalysisEvidence, error) { return evidence, nil })
	if len(checked.ProcessedReads) != 0 {
		t.Fatal("无摘要/发现的处理声明可以折叠原文")
	}
}

func TestAnalysisReportParserAndUntrustedRendering(t *testing.T) {
	valid := `{"schema_version":1,"findings":[{"id":"F1","kind":"fact","statement":"<script>alert(1)</script> [点击](javascript:x)","evidence":[{"evidence_id":"E1","start_line":3,"end_line":3,"quote":"V2 采用新格式。"}]}]}`
	for _, text := range []string{valid, "```json\n" + valid + "\n```", "```\n" + valid + "\n```"} {
		if _, err := ParseAnalysisReport(text); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"普通成功文本", "null", "[]", `{"schema_version":2}`, valid + valid, `{"schema_version":1,"findings":[{"id":"F1","kind":"fact","statement":"a"},{"id":"F1","kind":"fact","statement":"b"}]}`, "```json\n" + valid} {
		if _, err := ParseAnalysisReport(text); !errors.Is(err, errAnalysisReportInvalid) {
			t.Fatalf("错误结构被接受：%q %v", text, err)
		}
	}
	report, _ := ParseAnalysisReport(valid)
	checked, err := ValidateAnalysisReport(report, func(string) (AnalysisEvidence, error) {
		return analysisTestEvidence("前言\nV2 采用新格式。\n结尾\n", 2, 0), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	text := RenderAnalysis(checked)
	if strings.Contains(text, "<script>") || strings.Contains(text, "[点击](javascript:") {
		t.Fatal("把不可信结论当作HTML或Markdown链接")
	}
	state := &AnalysisState{Version: 1, Evidence: map[string]AnalysisEvidence{"E1": analysisTestEvidence("片段", 1, 0)}, Reports: map[string]AnalysisReport{"agent": checked}}
	if err := ValidateAnalysisState(state); err != nil {
		t.Fatal(err)
	}
	state.Version = 2
	if err := ValidateAnalysisState(state); err == nil {
		t.Fatal("接受未知状态版本")
	}
}

func TestAnalysisReportGeneratedDiagnosticsStayPersistable(t *testing.T) {
	report := analysisTestReport("V2 采用新格式。")
	for i := 0; i < 192; i++ {
		report.Unverified = append(report.Unverified, fmt.Sprintf("已有待核实项 %d", i))
	}
	for i := 0; i < 63; i++ {
		finding := report.Findings[0]
		finding.ID = fmt.Sprintf("bad%d", i)
		finding.Statement = strings.Repeat("文", 1333)
		finding.Evidence = append([]AnalysisCitation(nil), finding.Evidence...)
		finding.Evidence[0].Quote = "错摘录"
		report.Findings = append(report.Findings, finding)
	}
	for i := 0; i < 64; i++ {
		report.OverviewIDs = append(report.OverviewIDs, fmt.Sprintf("missing%d", i))
	}
	evidence := analysisTestEvidence("前言\nV2 采用新格式。\n结尾\n", 2, 0)
	checked, err := ValidateAnalysisReport(report, func(string) (AnalysisEvidence, error) { return evidence, nil })
	if err != nil || len(checked.Unverified) != 192 || checked.UnverifiedDropped != 127 {
		t.Fatalf("未有界保存诊断与缺口计数：count=%d dropped=%d err=%v", len(checked.Unverified), checked.UnverifiedDropped, err)
	}
	data, _ := json.Marshal(checked)
	if _, err := ParseAnalysisReport(string(data)); err != nil {
		t.Fatalf("校验结果不能往返读取：%v", err)
	}
	if err := ValidateAnalysisState(&AnalysisState{Version: 1, Reports: map[string]AnalysisReport{"root": checked}}); err != nil {
		t.Fatalf("自身结果不满足持久状态约束：%v", err)
	}
	if !strings.Contains(RenderAnalysis(checked), "127 项待核实内容") {
		t.Fatal("没有展示未展开项与记录缺口")
	}
	report.Unverified = nil
	report.OverviewIDs = nil
	checked, err = ValidateAnalysisReport(report, func(string) (AnalysisEvidence, error) { return evidence, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range checked.Unverified {
		if len(item) > 4000 {
			t.Fatal("生成诊断超出单条持久上限")
		}
	}
	data, _ = json.Marshal(checked)
	if _, err := ParseAnalysisReport(string(data)); err != nil {
		t.Fatalf("长结论降级后不可读取：%v", err)
	}
}
