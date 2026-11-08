package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

const analysisSchemaVersion = 1

var errAnalysisReportInvalid = errors.New("分析报告没有有效的核心结论或结构不合法")

// AnalysisReport 同时用于同轮分析笔记和最终报告，不包含模型私密思维过程。
// Status 仅说明引用的机械校验结果，不代表结论经过独立语义验收。
type AnalysisReport struct {
	SchemaVersion     int               `json:"schema_version"`
	Findings          []AnalysisFinding `json:"findings"`
	Unverified        []string          `json:"unverified,omitempty"`
	UnverifiedDropped int               `json:"unverified_dropped,omitempty"`
	OverviewIDs       []string          `json:"overview_ids,omitempty"`
	ProcessedReads    []string          `json:"processed_reads,omitempty"`
	DeferredReads     []DeferredRead    `json:"deferred_reads,omitempty"`
	Summaries         map[string]string `json:"summaries,omitempty"`
}

// DeferredRead 记录仍需处理的读取，不授予正文折叠或复读拒绝资格。
type DeferredRead struct {
	EvidenceID string             `json:"evidence_id"`
	Reason     string             `json:"reason"`
	WaitFor    *DeferredReadRange `json:"wait_for,omitempty"`
}

// DeferredReadRange 是源文件绝对字节的半开区间，仅描述等待材料，不授予处理资格。
type DeferredReadRange struct {
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
}

type AnalysisFinding struct {
	ScopeRequirement string             `json:"scope_requirement,omitempty"`
	ScopeID          string             `json:"scope_id,omitempty"`
	ScopeRevision    string             `json:"scope_revision,omitempty"`
	ID               string             `json:"id"`
	Kind             string             `json:"kind"`
	Statement        string             `json:"statement"`
	Evidence         []AnalysisCitation `json:"evidence,omitempty"`
	DependsOn        []string           `json:"depends_on,omitempty"`
	Context          string             `json:"context,omitempty"`
	Limitations      string             `json:"limitations,omitempty"`
	Status           string             `json:"status,omitempty"`
}

// AnalysisCitation 的展示位置和版本由引擎覆盖；不信任模型自报的路径和状态。
type AnalysisCitation struct {
	EvidenceID string `json:"evidence_id"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Quote      string `json:"quote"`
	Path       string `json:"path,omitempty"`
	Version    string `json:"version,omitempty"`
	ByteStart  int64  `json:"byte_start,omitempty"`
	ByteEnd    int64  `json:"byte_end,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	ActionID   string `json:"action_id,omitempty"`
	Status     string `json:"status,omitempty"`
}

// AnalysisEvidence 保存动作当时的结构化原文依据。Source 仅在本次运行中使用，
// 不随状态反复编码；落盘原文由 ArtifactID 指向现有不可变材料。
type AnalysisEvidence struct {
	ID         string             `json:"id"`
	ActionID   string             `json:"action_id"`
	AgentID    string             `json:"agent_id,omitempty"`
	RunID      string             `json:"run_id"`
	Receipt    schema.ReadReceipt `json:"receipt"`
	ArtifactID string             `json:"artifact_id"`
	Source     []byte             `json:"-"`
}

// AnalysisState 是可选扩展。旧任务缺少该字段时，不补造分析笔记或读取依据。
type AnalysisState struct {
	Version   int                          `json:"version"`
	Evidence  map[string]AnalysisEvidence  `json:"evidence,omitempty"`
	Reports   map[string]AnalysisReport    `json:"reports,omitempty"`
	Processed map[string][]string          `json:"processed,omitempty"`
	Summaries map[string]map[string]string `json:"summaries,omitempty"`
}

// ParseAnalysisReport 只接受一个有版本的 JSON 对象，确定性兼容完整代码围栏。
// 解析失败不会触发模型修复，也不会把自由文本当作已经校验的分析。
func ParseAnalysisReport(text string) (AnalysisReport, error) {
	var report AnalysisReport
	if len(text) > 256*1024 || !utf8.ValidString(text) {
		return report, fmt.Errorf("%w：报告超过 256 KiB 或不是有效 UTF-8", errAnalysisReportInvalid)
	}
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```json\n") || strings.HasPrefix(body, "```\n") {
		if !strings.HasSuffix(body, "```") {
			return report, fmt.Errorf("%w：JSON 代码围栏不完整", errAnalysisReportInvalid)
		}
		body = strings.TrimSpace(strings.TrimSuffix(body[strings.IndexByte(body, '\n')+1:], "```"))
	}
	if len(body) == 0 || body[0] != '{' {
		return report, fmt.Errorf("%w：请交付单个 JSON 对象", errAnalysisReportInvalid)
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return report, fmt.Errorf("%w：%v", errAnalysisReportInvalid, err)
	}
	if err := checkAnalysisStructure(report); err != nil {
		return report, fmt.Errorf("%w：%v", errAnalysisReportInvalid, err)
	}
	return report, nil
}

func checkAnalysisStructure(report AnalysisReport) error {
	if report.SchemaVersion != analysisSchemaVersion {
		return fmt.Errorf("schema_version 必须是 %d", analysisSchemaVersion)
	}
	if report.UnverifiedDropped < 0 {
		return errors.New("未展开待核实项计数不能为负数")
	}
	if len(report.Findings) > 64 || len(report.Unverified) > 192 || len(report.ProcessedReads) > 128 || len(report.DeferredReads) > 128 || len(report.Summaries) > 128 || len(report.OverviewIDs) > 64 {
		return errors.New("发现最多 64 条，待核实项最多 192 条，读取及摘要最多 128 条")
	}
	seen := make(map[string]bool)
	for _, f := range report.Findings {
		if f.ScopeRequirement != "" && f.ScopeRequirement != "fragment" && f.ScopeRequirement != "file" && f.ScopeRequirement != "manifest" {
			return fmt.Errorf("发现 %s 的 scope_requirement 必须为 fragment/file/manifest", f.ID)
		}
		if !analysisIDValid(f.ID) || seen[f.ID] || strings.TrimSpace(f.Statement) == "" || len(f.Statement) > 4000 {
			return errors.New("发现 ID 必须有效且唯一，statement 必须非空且不超过 4000 字节")
		}
		seen[f.ID] = true
		if len(f.Evidence) > 8 || len(f.DependsOn) > 16 || len(f.Context) > 2000 || len(f.Limitations) > 2000 {
			return fmt.Errorf("发现 %s 的证据、依赖或说明超过上限", f.ID)
		}
		for _, ref := range f.Evidence {
			if len(ref.Quote) > 1024 {
				return fmt.Errorf("发现 %s 的单条原文摘录最多 1024 字节", f.ID)
			}
		}
	}
	for _, s := range report.Unverified {
		if len(s) > 4000 {
			return errors.New("单条待核实项最多 4000 字节")
		}
	}
	for id, summary := range report.Summaries {
		if !analysisIDValid(id) || strings.TrimSpace(summary) == "" || len(summary) > 2000 {
			return errors.New("摘要必须关联有效证据 ID，且非空、不超过 2000 字节")
		}
	}
	deferredIDs := make(map[string]bool)
	for _, read := range report.DeferredReads {
		if !analysisIDValid(read.EvidenceID) || deferredIDs[read.EvidenceID] || strings.TrimSpace(read.Reason) == "" || len(read.Reason) > 2000 || !utf8.ValidString(read.Reason) {
			return errors.New("暂缓读取须关联唯一有效证据 ID，理由非空且不超过 2000 字节")
		}
		deferredIDs[read.EvidenceID] = true
	}
	return nil
}

func analysisIDValid(id string) bool {
	if len(id) == 0 || len(id) > 96 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

// ValidateAnalysisReport 对逐条发现校验，任何一个无效依据都会使该发现降级，
// 而不是删除错误引用后保留原结论。lookup 由运行层检查权限、当前运行和文件版本。
func ValidateAnalysisReport(report AnalysisReport, lookup func(string) (AnalysisEvidence, error)) (AnalysisReport, error) {
	if err := checkAnalysisStructure(report); err != nil {
		return AnalysisReport{SchemaVersion: analysisSchemaVersion, Unverified: []string{err.Error()}}, fmt.Errorf("%w：%v", errAnalysisReportInvalid, err)
	}
	result := AnalysisReport{SchemaVersion: analysisSchemaVersion, Unverified: append([]string(nil), report.Unverified...), UnverifiedDropped: report.UnverifiedDropped}
	byID := make(map[string]AnalysisFinding)
	for _, finding := range report.Findings {
		byID[finding.ID] = finding
	}
	validated := make(map[string]AnalysisFinding)
	failed := make(map[string]string)
	visiting := make(map[string]bool)
	var check func(string) bool
	check = func(id string) bool {
		if _, ok := validated[id]; ok {
			return true
		}
		if _, ok := failed[id]; ok {
			return false
		}
		finding, ok := byID[id]
		if !ok {
			failed[id] = "依赖的发现不存在"
			return false
		}
		if visiting[id] {
			failed[id] = "发现依赖形成循环"
			return false
		}
		visiting[id] = true
		defer delete(visiting, id)
		if finding.Kind != "fact" && finding.Kind != "inference" && finding.Kind != "recommendation" {
			failed[id] = "声明为待核实或发现类型无效"
			return false
		}
		if len(finding.Evidence) == 0 && (finding.Kind == "fact" || len(finding.DependsOn) == 0) {
			failed[id] = "缺少直接原文依据或有效发现依赖"
			return false
		}
		refs := make([]AnalysisCitation, 0, len(finding.Evidence))
		for _, ref := range finding.Evidence {
			if lookup == nil {
				failed[id] = "原文证据索引不可用"
				return false
			}
			evidence, err := lookup(ref.EvidenceID)
			if err != nil {
				failed[id] = fmt.Sprintf("证据 %s 不可用：%v", ref.EvidenceID, err)
				return false
			}
			checked, err := ValidateAnalysisCitation(ref, evidence)
			if err != nil {
				failed[id] = fmt.Sprintf("证据 %s 未通过：%v", ref.EvidenceID, err)
				return false
			}
			refs = append(refs, checked)
		}
		for _, dependency := range finding.DependsOn {
			if !check(dependency) {
				failed[id] = "依赖 " + dependency + " 无有效依据"
				return false
			}
		}
		finding.Evidence, finding.Status = refs, "evidence_valid"
		validated[id] = finding
		return true
	}
	core := 0
	used := make(map[string]bool)
	for _, finding := range report.Findings {
		if check(finding.ID) {
			f := validated[finding.ID]
			result.Findings = append(result.Findings, f)
			if f.Kind != "recommendation" {
				core++
			}
			for _, ref := range f.Evidence {
				used[ref.EvidenceID] = true
			}
		} else {
			appendAnalysisUnverified(&result, finding.ID+"（"+failed[finding.ID]+"）："+finding.Statement)
		}
	}
	for _, id := range uniqueAnalysisIDs(report.OverviewIDs) {
		if _, ok := validated[id]; ok {
			result.OverviewIDs = append(result.OverviewIDs, id)
		} else {
			appendAnalysisUnverified(&result, "概览引用 "+id+" 无有效发现，已移除")
		}
	}
	// 处理声明只有在证据可用且伴随有效发现或摘要时才能成为折叠候选。
	// 单独保存摘要不等于已经处理，仍须显式 processed_reads 才能折叠。
	for id, summary := range report.Summaries {
		if lookup == nil {
			continue
		}
		evidence, err := lookup(id)
		if err == nil && evidence.ID == id && int64(len(evidence.Source)) == evidence.Receipt.ByteEnd-evidence.Receipt.ByteStart && utf8.Valid(evidence.Source) {
			if result.Summaries == nil {
				result.Summaries = make(map[string]string)
			}
			result.Summaries[id] = summary
		}
	}
	for _, id := range uniqueAnalysisIDs(report.ProcessedReads) {
		if lookup == nil {
			continue
		}
		evidence, err := lookup(id)
		if err != nil || evidence.ID != id || int64(len(evidence.Source)) != evidence.Receipt.ByteEnd-evidence.Receipt.ByteStart || !utf8.Valid(evidence.Source) {
			continue
		}
		summary := report.Summaries[id]
		if !used[id] && strings.TrimSpace(summary) == "" {
			continue
		}
		result.ProcessedReads = append(result.ProcessedReads, id)
		if summary != "" {
			if result.Summaries == nil {
				result.Summaries = make(map[string]string)
			}
			result.Summaries[id] = summary
		}
	}
	for _, read := range report.DeferredReads {
		if lookup == nil {
			continue
		}
		evidence, err := lookup(read.EvidenceID)
		if err == nil && evidence.ID == read.EvidenceID && int64(len(evidence.Source)) == evidence.Receipt.ByteEnd-evidence.Receipt.ByteStart && utf8.Valid(evidence.Source) {
			result.DeferredReads = append(result.DeferredReads, read)
		}
	}
	if core == 0 {
		return result, errAnalysisReportInvalid
	}
	return result, nil
}

// 校验自身生成的诊断也必须满足持久化边界，超出部分保留明确计数。
func appendAnalysisUnverified(report *AnalysisReport, text string) {
	if len(report.Unverified) >= 192 {
		if report.UnverifiedDropped < int(^uint(0)>>1) {
			report.UnverifiedDropped++
		}
		return
	}
	report.Unverified = append(report.Unverified, boundedAnalysisText(text, 4000))
}

func boundedAnalysisText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= limit {
		return text
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:max(0, end)] + "…"
}

func uniqueAnalysisIDs(ids []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

// RenderAnalysis 只渲染校验后的发现。文件链接由上层基于登记根或证据 ID 建立，
// 此处不把模型字符串解释为 URL、HTML 或额外 Markdown 内容。
func RenderAnalysis(report AnalysisReport) string {
	var out strings.Builder
	out.WriteString("分析报告\n\n引用已核对原文位置、读取范围和版本；分析结论未经过独立语义验收。\n")
	if len(report.OverviewIDs) > 0 {
		out.WriteString("\n概览：" + strings.Join(report.OverviewIDs, "、") + "。\n")
	}
	labels := map[string]string{"fact": "材料事实", "inference": "分析推断", "recommendation": "建议"}
	for _, kind := range []string{"fact", "inference", "recommendation"} {
		var selected []AnalysisFinding
		for _, finding := range report.Findings {
			if finding.Kind == kind && finding.Status == "evidence_valid" {
				selected = append(selected, finding)
			}
		}
		if len(selected) == 0 {
			continue
		}
		out.WriteString("\n" + labels[kind] + "\n")
		for _, finding := range selected {
			out.WriteString("\n- " + escapeAnalysisText(finding.ID) + "：" + escapeAnalysisText(finding.Statement) + "\n")
			if finding.Context != "" {
				out.WriteString("  适用背景：" + escapeAnalysisText(finding.Context) + "\n")
			}
			if len(finding.DependsOn) > 0 {
				out.WriteString("  依据发现：" + strings.Join(finding.DependsOn, "、") + "\n")
			}
			for _, ref := range finding.Evidence {
				out.WriteString(fmt.Sprintf("  引用：%s:%d", escapeAnalysisText(ref.Path), ref.StartLine))
				if ref.EndLine != ref.StartLine {
					fmt.Fprintf(&out, "-%d", ref.EndLine)
				}
				fmt.Fprintf(&out, "（%s；版本 %s）\n", escapeAnalysisText(ref.EvidenceID), escapeAnalysisText(ref.Version))
				out.WriteString("  原文：" + escapeAnalysisText(ref.Quote) + "\n")
			}
			if finding.Limitations != "" {
				out.WriteString("  限制：" + escapeAnalysisText(finding.Limitations) + "\n")
			}
		}
	}
	if len(report.Unverified) > 0 {
		out.WriteString("\n待核实\n\n")
		for _, item := range report.Unverified {
			out.WriteString("- " + escapeAnalysisText(item) + "\n")
		}
	}
	if report.UnverifiedDropped > 0 {
		fmt.Fprintf(&out, "\n另有 %d 项待核实内容因数量上限未展开，报告存在记录缺口。\n", report.UnverifiedDropped)
	}
	return strings.TrimSpace(out.String())
}

func escapeAnalysisText(text string) string {
	return strings.NewReplacer("\\", "\\\\", "<", "&lt;", ">", "&gt;", "[", "\\[", "]", "\\]", "`", "\\`", "*", "\\*", "_", "\\_").Replace(text)
}

// sortedAnalysisEvidenceIDs 便于生成稳定的请求目录及离线快照。
func sortedAnalysisEvidenceIDs(evidence map[string]AnalysisEvidence) []string {
	ids := make([]string, 0, len(evidence))
	for id := range evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
