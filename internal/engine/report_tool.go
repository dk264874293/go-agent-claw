package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const reportPrompt = `你在执行显式证据报告任务。只调查材料并生成指定的新Markdown文件，不修改原始资料。
重要结论必须使用本次证据ID、精确行范围和短原文摘录。事实、推断、建议、待核实分开。每条结论填写context说明版本背景及适用条件。
不要重复写一份长篇报告：中间使用work_notes保存短笔记；最终将完整结构化报告一次提交write_report。write_report必须是该响应唯一工具调用，提交后由引擎直接收尾，不需要再回复一轮。
参数直接采用schema_version/findings/overview_ids/unverified报告对象。例如{"schema_version":1,"findings":[{"id":"F1","kind":"fact","statement":"材料事实","context":"适用版本和条件","evidence":[{"evidence_id":"E1","start_line":3,"end_line":3,"quote":"准确原文"}]}],"overview_ids":["F1"]}。
每条发现可增加claim_type：general、comparison、migration、absence、runtime。comparison必须引用不同文件的两端，conditions填version_context和applicable_scope。migration必须填old_read_path/new_read_path/default_behavior/sync_changes。absence必须声明file或manifest的scope_requirement；manifest必须填写当前覆盖目录中的scope_id和scope_revision，仅证明该固定资料集合。未实际执行的运行效果应直接列unverified，静态代码只能证明风险，不能证明运行已经失败。
先区分V1/V2及练习目标，再判断是否冲突；字段迁移核对.get()默认值与调用条件；引用有效不证明结论正确。不要编造路径、版本、行号或运行结果。目标路径由引擎配置，不在参数中另选。`

// reportClaimMeta 是报告专用声明，不改变已有严格分析响应格式。
type reportClaimMeta struct {
	ID         string           `json:"id"`
	ClaimType  string           `json:"claim_type"`
	Conditions reportConditions `json:"conditions"`
}
type reportConditions struct {
	VersionContext  string `json:"version_context"`
	ApplicableScope string `json:"applicable_scope"`
	OldReadPath     string `json:"old_read_path"`
	NewReadPath     string `json:"new_read_path"`
	DefaultBehavior string `json:"default_behavior"`
	SyncChanges     string `json:"sync_changes"`
}

type reportTool struct{}

func (reportTool) Name() string { return "write_report" }
func (reportTool) Definition() schema.ToolDefinition {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	citation := map[string]any{"type": "object", "required": []string{"evidence_id", "start_line", "end_line", "quote"}, "properties": map[string]any{
		"evidence_id": text("本次成功读取的短证据ID"), "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}, "quote": text("实际行范围中的准确短原文，最多1024字节"),
	}}
	properties := map[string]any{
		"id": text("稳定发现ID，例如F1"), "kind": map[string]any{"type": "string", "enum": []string{"fact", "inference", "recommendation", "unverified"}},
		"statement": text("简短结论，不复述正文"), "context": text("必填：适用版本和执行条件；未知则明确未知"), "limitations": text("未验证边界"),
		"evidence": map[string]any{"type": "array", "items": citation}, "depends_on": map[string]any{"type": "array", "items": text("已有发现ID")},
		"scope_requirement": map[string]any{"type": "string", "enum": []string{"fragment", "file", "manifest"}},
		"scope_id":          text("manifest声明必填：当前覆盖目录的范围身份"), "scope_revision": text("manifest声明必填：当前覆盖目录的范围修订"),
		"claim_type": map[string]any{"type": "string", "enum": []string{"general", "comparison", "migration", "absence", "runtime"}},
		"conditions": map[string]any{"type": "object", "properties": map[string]any{
			"version_context": text("比较两端的版本背景"), "applicable_scope": text("共同适用范围"), "old_read_path": text("原字段读取路径"), "new_read_path": text("新字段读取路径"), "default_behavior": text("缺字段和默认值行为"), "sync_changes": text("必须同步修改的读取/校验/测试"),
		}},
	}
	return schema.ToolDefinition{Name: "write_report", Description: "终局提交完整证据报告，程序校验并只创建配置的Markdown文件。必须为本响应唯一工具；成功后不再调用模型复述。无效核心报告阻断，未核实项须明确保留。", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"schema_version", "findings"}, "properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "enum": []int{1}},
			"findings":       map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "object", "required": []string{"id", "kind", "statement", "context"}, "properties": properties}},
			"overview_ids":   map[string]any{"type": "array", "items": text("已接受的核心发现ID")}, "unverified": map[string]any{"type": "array", "items": text("待核实事项")},
			"processed_reads": map[string]any{"type": "array", "items": text("已处理的证据ID")}, "summaries": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
	}}
}

func (reportTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	r := currentRun(ctx)
	if r == nil || r.state.Spec.Report == nil || !tools.ScopeFromContext(ctx).ReportOnly || tools.ScopeFromContext(ctx).Mode != tools.ModeWorkspace {
		return "", reportError{"report_invalid", "当前任务没有显式报告写入权限"}
	}
	var body []byte
	var err error
	if r.qualityEnabled() {
		ctx = context.WithValue(ctx, qualityPublicationKey{}, true)
		if err = r.checkQualityVersions(ctx); err == nil {
			if q := r.state.Quality; q.FrozenArtifactID != "" && r.state.Report != nil && r.state.Report.Status == "prepared" {
				body, err = r.readReportMaterial(q.FrozenArtifactID)
				if err == nil && fmt.Sprintf("%x", sha256.Sum256(body)) != q.FrozenSHA256 {
					err = r.noteStorageError(fmt.Errorf("%w：质量冻结材料摘要不符", errStorage))
				}
			} else if q := r.state.Quality; q.OriginRunID != r.runID && q.FinalCheckArtifactID != "" {
				body, err = r.prepareRecoveredQualityReport(ctx)
			} else {
				var draft QualityDraft
				draft, err = r.prepareQualitySubmission(ctx, raw)
				if err == nil {
					var checkedRaw []byte
					checkedRaw, err = json.Marshal(draft)
					// checks是核验入口字段，不传入普通报告写入契约。
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(checkedRaw, &fields)
					delete(fields, "checks")
					checkedRaw, _ = json.Marshal(fields)
					body, err = r.prepareReport(ctx, checkedRaw)
					if err == nil {
						err = r.freezeQualityBytes(body)
					}
				}
			}
		}
	} else {
		body, err = r.prepareReport(ctx, raw)
	}
	if err != nil {
		if errors.Is(err, errStorage) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return "", err
		}
		code := "report_invalid"
		var detail reportError
		if errors.As(err, &detail) {
			code = detail.code
		}
		return "", tools.NewRejectedError("报告校验拒绝", code, err)
	}
	r.mu.Lock()
	path := r.state.Spec.Report.TargetPath
	r.mu.Unlock()
	output, err := tools.WriteReportFile(ctx, path, body)
	if err != nil {
		return output, err
	}
	return output, nil
}

func (r *runState) prepareReport(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	r.mu.Lock()
	spec := r.state.Spec
	r.mu.Unlock()
	if err := ValidateReportSpec(spec); err != nil || spec.Report == nil {
		return nil, reportError{"report_invalid", fmt.Sprintf("报告配置无效：%v", err)}
	}
	if r.artifact == nil {
		return nil, r.noteStorageError(fmt.Errorf("%w：报告草稿存储不可用", errStorage))
	}
	draft := newID()
	if err := r.artifact(draft, raw); err != nil {
		return nil, r.noteStorageError(fmt.Errorf("%w：保存报告草稿失败: %w", errStorage, err))
	}
	saved := &ReportState{Version: 1, ReportID: newID(), OriginRunID: r.runID, Status: "invalid", DraftArtifactID: draft, ReferenceResult: "fail", SemanticResult: "not_run", ErrorCode: "report_invalid"}
	if err := r.record("report_draft", func(s *taskState) error { s.Report = saved; return nil }); err != nil {
		return nil, err
	}
	fail := func(code string, err error) ([]byte, error) {
		if persistErr := r.record("report_rejected", func(s *taskState) error {
			s.Report.Status, s.Report.ErrorCode = "invalid", code
			s.Report.Issues = append(s.Report.Issues, boundedAnalysisText(err.Error(), 4000))
			return nil
		}); persistErr != nil {
			return nil, persistErr
		}
		return nil, reportError{code, err.Error()}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fail("report_invalid", err)
	}
	for field := range fields {
		switch field {
		case "schema_version", "findings", "overview_ids", "unverified", "unverified_dropped", "processed_reads", "summaries":
		default:
			return fail("report_invalid", fmt.Errorf("报告参数不接受 %s；目标路径由任务配置确定", field))
		}
	}
	report, err := ParseAnalysisReport(string(raw))
	if err != nil {
		return fail("report_invalid", err)
	}
	var extra struct {
		Findings []reportClaimMeta `json:"findings"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return fail("report_invalid", err)
	}
	meta := make(map[string]reportClaimMeta)
	for _, f := range extra.Findings {
		meta[f.ID] = f
	}
	lookup := r.analysisLookup(ctx, true)
	for i := range report.Findings {
		f := &report.Findings[i]
		claim := meta[f.ID]
		reason := reportClaimIssue(*f, claim, lookup)
		context := f.Context
		if reason == "" && claim.ClaimType == "comparison" {
			context += "；比较版本：" + claim.Conditions.VersionContext + "；共同范围：" + claim.Conditions.ApplicableScope
		}
		if reason == "" && claim.ClaimType == "migration" {
			context += "；原读取路径：" + claim.Conditions.OldReadPath + "；新读取路径：" + claim.Conditions.NewReadPath + "；默认行为：" + claim.Conditions.DefaultBehavior + "；同步修改：" + claim.Conditions.SyncChanges
		}
		if len(context) > 2000 {
			reason = "版本和迁移条件总计超过2000字节，请简化，不能截断关键条件"
		}
		if reason != "" {
			f.Kind = "unverified"
			appendAnalysisUnverified(&report, f.ID+"（"+reason+"）："+f.Statement)
		} else {
			f.Context = context
		}
	}
	checked, checkErr := ValidateAnalysisReport(report, lookup)
	var dependencies []ReportScopeDependency
	dependenciesByFinding := map[string][]ReportScopeDependency{}
	filtered := checked.Findings[:0]
	for _, f := range checked.Findings {
		complete := true
		var findingDeps []ReportScopeDependency
		if f.ScopeRequirement == "file" {
			complete = len(f.Evidence) > 0
			for _, ref := range f.Evidence {
				coverage := r.coverageForPaths(ctx, []string{ref.Path})
				complete = complete && coverage != nil && coverage.Status == "complete"
				if coverage != nil && coverage.Status == "complete" {
					findingDeps = append(findingDeps, dependencyFromCoverage(coverage))
				}
			}
		}
		if f.ScopeRequirement == "manifest" {
			coverage := r.readingCoverage(ctx)
			complete = coverage != nil && coverage.Status == "complete" && f.ScopeID != "" && f.ScopeID == coverage.ScopeID && f.ScopeRevision == coverage.ScopeRevision
			if complete {
				findingDeps = append(findingDeps, dependencyFromCoverage(coverage))
			}
		}
		if complete {
			filtered = append(filtered, f)
			dependenciesByFinding[f.ID] = findingDeps
		} else {
			appendAnalysisUnverified(&checked, f.ID+"：所需范围存在读取缺口，不能沿用为确定结论。"+f.Statement)
		}
	}
	checked.Findings = filtered
	checked, checkErr = ValidateAnalysisReport(checked, lookup)
	if err := r.record("report_checked", func(s *taskState) error { s.Report.Checked = checked; return nil }); err != nil {
		return nil, err
	}
	if checkErr != nil {
		return fail("report_invalid", checkErr)
	}
	for _, finding := range checked.Findings {
		dependencies = append(dependencies, dependenciesByFinding[finding.ID]...)
	}
	if err := r.checkReadingComplete(ctx); err != nil {
		return fail("reading_incomplete", err)
	}
	if spec.ReadScope != nil && spec.ReadScope.RequireComplete {
		dependencies = append(dependencies, dependencyFromCoverage(r.readingCoverage(ctx)))
	}
	// 发现被依赖闭合校验移除后，不保留其范围依赖。重复依赖只保存一次。
	seenDeps := map[string]bool{}
	uniqueDeps := dependencies[:0]
	for _, dep := range dependencies {
		key := dep.ScopeID + ":" + dep.ScopeRevision
		if !seenDeps[key] {
			seenDeps[key] = true
			uniqueDeps = append(uniqueDeps, dep)
		}
	}
	dependencies = uniqueDeps
	if err := checkScopeDependencies(ctx, dependencies); err != nil {
		return fail("report_invalid", err)
	}
	versions := make(map[string]string)
	for _, f := range checked.Findings {
		for _, ref := range f.Evidence {
			if ref.Path == spec.Report.TargetPath {
				return fail("report_invalid", fmt.Errorf("报告目标不能覆盖证据原文"))
			}
			versions[ref.Path] = ref.Version
		}
	}
	// 再次核对源版本，然后只从这份冻结正文进行预览、执行与恢复。
	for path, version := range versions {
		actual, err := tools.FileVersion(ctx, path)
		if err != nil || actual != version {
			return fail("report_invalid", fmt.Errorf("报告来源版本已失效：%s", path))
		}
	}
	body := []byte("# 证据报告\n\n" + RenderAnalysis(checked) + "\n")
	preview, err := tools.PreviewReportWrite(ctx, spec.Report.TargetPath, body)
	if err != nil {
		return fail("report_target_conflict", err)
	}
	rendered := newID()
	if err := r.artifact(rendered, body); err != nil {
		return nil, r.noteStorageError(fmt.Errorf("%w：保存报告冻结材料失败: %w", errStorage, err))
	}
	if preview.After != fmt.Sprintf("%x", sha256.Sum256(body)) {
		return nil, r.noteStorageError(fmt.Errorf("%w：报告预览与冻结字节不符", errStorage))
	}
	if err := r.record("report_prepared", func(s *taskState) error {
		s.Report.Status, s.Report.ErrorCode = "prepared", ""
		s.Report.RenderedArtifactID, s.Report.OutputVersion = rendered, preview.After
		s.Report.SourceVersions = versions
		s.Report.ScopeDependencies = dependencies
		s.Report.ReferenceResult = "pass"
		if len(checked.Findings) < len(report.Findings) {
			s.Report.ReferenceResult = "partial"
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return body, nil
}

// rejectReportSubmission 保存调度前被拒绝的整批原始材料，不能形成孤立的材料ID。
func (r *runState) rejectReportSubmission(_ context.Context, raw []byte, reason string) error {
	if r.artifact == nil {
		return r.noteStorageError(fmt.Errorf("%w：报告草稿存储不可用", errStorage))
	}
	id := newID()
	if err := r.artifact(id, raw); err != nil {
		return r.noteStorageError(fmt.Errorf("%w：保存非法报告提交失败: %w", errStorage, err))
	}
	if err := r.record("report_rejected", func(s *taskState) error {
		s.Report = &ReportState{Version: 1, ReportID: newID(), OriginRunID: r.runID, Status: "invalid", DraftArtifactID: id, ReferenceResult: "fail", SemanticResult: "not_run", ErrorCode: "report_invalid", Issues: []string{boundedAnalysisText(reason, 4000)}}
		return nil
	}); err != nil {
		return err
	}
	return reportError{"report_invalid", reason}
}

func reportClaimIssue(f AnalysisFinding, meta reportClaimMeta, lookup func(string) (AnalysisEvidence, error)) string {
	if f.Kind == "unverified" {
		return "声明为待核实"
	}
	if strings.TrimSpace(f.Context) == "" {
		return "缺少版本背景和适用条件"
	}
	switch meta.ClaimType {
	case "", "general":
	case "comparison":
		paths := make(map[string]bool)
		for _, ref := range f.Evidence {
			if e, err := lookup(ref.EvidenceID); err == nil {
				paths[e.Receipt.Path] = true
			}
		}
		if len(paths) < 2 || strings.TrimSpace(meta.Conditions.VersionContext) == "" || strings.TrimSpace(meta.Conditions.ApplicableScope) == "" {
			return "跨文档比较缺少两端来源、版本背景或共同范围"
		}
	case "migration":
		c := meta.Conditions
		if strings.TrimSpace(c.OldReadPath) == "" || strings.TrimSpace(c.NewReadPath) == "" || strings.TrimSpace(c.DefaultBehavior) == "" || strings.TrimSpace(c.SyncChanges) == "" {
			return "字段迁移缺少旧/新读取路径、默认行为或同步修改项"
		}
	case "absence":
		if f.ScopeRequirement != "file" && f.ScopeRequirement != "manifest" {
			return "否定结论没有声明所需完整覆盖范围"
		}
	case "runtime":
		return "报告首版没有执行运行验证，运行效果只能列为待实测"
	default:
		return "未知结论声明类型"
	}
	return ""
}
