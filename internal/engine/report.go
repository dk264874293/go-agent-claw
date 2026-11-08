package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// ReportSpec 仅声明一个新建 Markdown 报告，不能扩大工作区权限。
type ReportSpec struct {
	Version    int    `json:"version"`
	Format     string `json:"format"`
	TargetPath string `json:"target_path"`
	CreateOnly bool   `json:"create_only"`
}

// ReportState 将草稿、冻结渲染和文件效果分开；引用有效不代表结论正确。
type ReportState struct {
	Version            int                     `json:"version"`
	ReportID           string                  `json:"report_id"`
	OriginRunID        string                  `json:"origin_run_id"`
	Status             string                  `json:"status"`
	DraftArtifactID    string                  `json:"draft_artifact_id"`
	RenderedArtifactID string                  `json:"rendered_artifact_id,omitempty"`
	SourceVersions     map[string]string       `json:"source_versions,omitempty"`
	ScopeDependencies  []ReportScopeDependency `json:"scope_dependencies,omitempty"`
	OutputActionID     string                  `json:"output_action_id,omitempty"`
	OutputVersion      string                  `json:"output_version,omitempty"`
	ReferenceResult    string                  `json:"reference_result"`
	SemanticResult     string                  `json:"semantic_result"`
	Checked            AnalysisReport          `json:"checked"`
	Issues             []string                `json:"issues,omitempty"`
	ErrorCode          string                  `json:"error_code,omitempty"`
}

type ReportDelivery struct {
	ReportID           string   `json:"report_id"`
	OriginRunID        string   `json:"origin_run_id"`
	Status             string   `json:"status"`
	TargetPath         string   `json:"target_path"`
	OutputVersion      string   `json:"output_version,omitempty"`
	DraftArtifactID    string   `json:"draft_artifact_id"`
	RenderedArtifactID string   `json:"rendered_artifact_id,omitempty"`
	ReferenceResult    string   `json:"reference_result"`
	SemanticResult     string   `json:"semantic_result"`
	Text               string   `json:"text,omitempty"`
	Issues             []string `json:"issues,omitempty"`
}

// ArtifactRef 只从已确认回执产生。相对路径不证明文件当前存在。
type ArtifactRef struct {
	ID       string `json:"id"`
	ActionID string `json:"action_id"`
	Path     string `json:"path"`
	Version  string `json:"version"`
}

type reportError struct{ code, reason string }

func (e reportError) Error() string { return e.reason }
func (e reportError) Unwrap() error { return errBlocked }

// NormalizeReportSpec 填充首版固定值；目标范围和执行模式仍必须通过校验。
func NormalizeReportSpec(spec *TaskSpec) error {
	if spec.Report == nil {
		return nil
	}
	copy := *spec.Report
	if copy.Version == 0 {
		copy.Version = 1
	}
	if copy.Format == "" {
		copy.Format = "evidence_v1"
	}
	copy.CreateOnly = true
	spec.Report = &copy
	return ValidateReportSpec(*spec)
}

func ValidateReportSpec(spec TaskSpec) error {
	if spec.Report == nil {
		return nil
	}
	r := spec.Report
	if r.Version != 1 || r.Format != "evidence_v1" || !r.CreateOnly {
		return errors.New("报告配置仅支持 version=1、format=evidence_v1、create_only=true")
	}
	if spec.Mode != tools.ModeWorkspace || (spec.Kind != "" && spec.Kind != TaskChange) || spec.Analysis || spec.Plan || spec.VerifyCommand != "" || spec.Delegation != "" {
		return errors.New("报告仅支持 workspace + change，不能组合分析、规划、验收或委派配置")
	}
	if !validReportPath(r.TargetPath) {
		return errors.New("报告目标必须是规范的工作区相对 .md 文件路径，不能使用父级跳转")
	}
	return nil
}

func validReportPath(path string) bool {
	if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.ContainsAny(path, "\\\x00\r\n") || !strings.HasSuffix(path, ".md") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == ".claw" {
			return false
		}
	}
	return true
}

func validReportVersion(version string) bool {
	b, err := hex.DecodeString(version)
	return err == nil && len(b) == sha256.Size && version == strings.ToLower(version)
}

func validateReportState(s taskState) error {
	if err := ValidateReportSpec(s.Spec); err != nil {
		return err
	}
	if s.Report == nil {
		return nil
	}
	r := s.Report
	if s.Spec.Report == nil || r.Version != 1 || !analysisIDValid(r.ReportID) || !analysisIDValid(r.OriginRunID) || !analysisIDValid(r.DraftArtifactID) || r.SemanticResult != "not_run" {
		return errors.New("报告状态身份、版本或语义验收字段无效")
	}
	if r.ReferenceResult != "pass" && r.ReferenceResult != "partial" && r.ReferenceResult != "fail" && r.ReferenceResult != "stale" {
		return errors.New("报告引用校验状态无效")
	}
	switch r.Status {
	case "invalid":
		if r.ErrorCode == "" {
			return errors.New("无效报告缺少错误原因")
		}
	case "prepared", "published", "stale":
		if !analysisIDValid(r.RenderedArtifactID) || !validReportVersion(r.OutputVersion) || r.Checked.SchemaVersion != 1 {
			return errors.New("报告缺少有效冻结材料及产物版本")
		}
		if err := checkAnalysisStructure(r.Checked); err != nil {
			return fmt.Errorf("报告检查结果无效: %w", err)
		}
		core, expectedSources := 0, make(map[string]string)
		for _, f := range r.Checked.Findings {
			if f.Status != "evidence_valid" {
				return errors.New("报告包含未经引用检查的正式发现")
			}
			if f.Kind != "recommendation" {
				core++
			}
			for _, ref := range f.Evidence {
				if !analysisIDValid(ref.EvidenceID) || !analysisIDValid(ref.ActionID) || !analysisIDValid(ref.ArtifactID) || !validReportVersion(ref.Version) || r.SourceVersions[ref.Path] != ref.Version {
					return errors.New("报告发现与来源版本目录不一致")
				}
				expectedSources[ref.Path] = ref.Version
			}
		}
		if core == 0 || len(expectedSources) == 0 || len(expectedSources) != len(r.SourceVersions) {
			return errors.New("报告缺少核心发现或有效来源")
		}
		if (r.Status == "published" || r.Status == "stale") && !analysisIDValid(r.OutputActionID) {
			return errors.New("已发布报告缺少文件动作身份")
		}
	default:
		return errors.New("报告发布状态无效")
	}
	if len(r.Issues) > 192 {
		return errors.New("报告诊断超过上限")
	}
	for path, version := range r.SourceVersions {
		if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || !validReportVersion(version) || path == s.Spec.Report.TargetPath {
			return errors.New("报告来源路径或版本无效")
		}
	}
	for _, dep := range r.ScopeDependencies {
		if dep.ScopeID == "" || len(dep.ScopeID) > 128 || dep.ScopeRevision == "" || len(dep.ScopeRevision) > 128 || len(dep.SourceVersions) > 10000 {
			return errors.New("报告范围依赖身份或成员无效")
		}
		for path, version := range dep.SourceVersions {
			if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == s.Spec.Report.TargetPath || !validReportVersion(version) {
				return errors.New("报告范围依赖来源路径或版本无效")
			}
		}
	}
	if r.Status == "prepared" || r.Status == "published" || r.Status == "stale" {
		dependencyIDs := map[string]bool{}
		for _, dep := range r.ScopeDependencies {
			dependencyIDs[dep.ScopeID+":"+dep.ScopeRevision] = true
		}
		for _, finding := range r.Checked.Findings {
			if finding.ScopeRequirement == "manifest" && finding.ScopeID != "" && !dependencyIDs[finding.ScopeID+":"+finding.ScopeRevision] {
				return errors.New("报告范围发现缺少对应完整性依赖")
			}
		}
		if scope := s.Spec.ReadScope; scope != nil && scope.RequireComplete {
			selectors := append([]string(nil), scope.Paths...)
			sort.Strings(selectors)
			expectedID := coverageHash(struct {
				Task, Owner, Kind, Glob string
				Selectors               []string
			}{s.Spec.ID, "", "declared", scope.NameGlob, selectors})
			found := false
			for _, dep := range r.ScopeDependencies {
				found = found || dep.ScopeID == expectedID
			}
			if !found {
				return errors.New("完整阅读报告缺少声明范围依赖")
			}
		}
	}
	return nil
}

// checkReportTarget 在已发布报告恢复核对之后、首次付费请求之前进行只读预检。
func (r *runState) checkReportTarget(ctx context.Context) error {
	r.mu.Lock()
	if r.state.Spec.Report == nil {
		r.mu.Unlock()
		return nil
	}
	path := r.state.Spec.Report.TargetPath
	r.mu.Unlock()
	if _, err := tools.PreviewReportWrite(ctx, path, nil); err != nil {
		return reportError{"report_target_conflict", "报告目标预检失败：" + err.Error()}
	}
	return nil
}

// reportWritePreview 在现有 action_started 钩子内使用，禁止重新生成报告正文。
func (r *runState) reportWritePreview(ctx context.Context) (tools.FilePreview, error) {
	if r.qualityEnabled() {
		if err := r.checkQualityVersions(ctx); err != nil {
			return tools.FilePreview{}, err
		}
	}
	r.mu.Lock()
	spec, state := r.state.Spec.Report, r.state.Report
	if spec == nil || state == nil || state.Status != "prepared" {
		r.mu.Unlock()
		return tools.FilePreview{}, reportError{"report_invalid", "报告尚未冻结，不能执行写入"}
	}
	path, id, expected := spec.TargetPath, state.RenderedArtifactID, state.OutputVersion
	sources := make(map[string]string, len(state.SourceVersions))
	for p, v := range state.SourceVersions {
		sources[p] = v
	}
	r.mu.Unlock()
	if err := checkScopeDependencies(ctx, state.ScopeDependencies); err != nil {
		return tools.FilePreview{}, reportError{"report_invalid", err.Error()}
	}
	for p, version := range sources {
		current, err := tools.FileVersion(ctx, p)
		if err != nil || current != version {
			return tools.FilePreview{}, reportError{"report_invalid", "写入前报告来源版本已失效：" + p}
		}
	}
	body, err := r.readReportMaterial(id)
	if err != nil {
		return tools.FilePreview{}, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(body)) != expected {
		return tools.FilePreview{}, r.noteStorageError(fmt.Errorf("%w：报告冻结材料与预期摘要不符", errStorage))
	}
	return tools.PreviewReportWrite(ctx, path, body)
}

func (r *runState) readReportMaterial(id string) ([]byte, error) {
	if r.readArtifact == nil && r.readArtifactPage == nil {
		return nil, r.noteStorageError(fmt.Errorf("%w：报告材料读取不可用", errStorage))
	}
	var all []byte
	for offset := 0; ; {
		var b []byte
		var err error
		if r.readArtifactPage != nil {
			page, readErr := r.readArtifactPage(id, offset, 4000)
			b, err = page.Data, readErr
		} else {
			b, err = r.readArtifact(id, offset, 4000)
		}
		if err != nil {
			return nil, r.noteStorageError(fmt.Errorf("%w：读取报告冻结材料失败: %w", errStorage, err))
		}
		if len(b) == 0 {
			break
		}
		all = append(all, b...)
		if len(all) > 1024*1024 {
			return nil, r.noteStorageError(fmt.Errorf("%w：报告冻结材料超过1MiB", errStorage))
		}
		offset += len(b)
	}
	return all, nil
}

func (r *runState) reportFailure(fallback string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	code := "report_invalid"
	if r.state.Report != nil && r.state.Report.ErrorCode != "" {
		code = r.state.Report.ErrorCode
	}
	return reportError{code, fallback}
}

func (r *runState) reportDelivery() (*ReportDelivery, error) {
	r.mu.Lock()
	if r.state.Report == nil || r.state.Spec.Report == nil {
		r.mu.Unlock()
		return nil, nil
	}
	copy := *r.state.Report
	path := r.state.Spec.Report.TargetPath
	r.mu.Unlock()
	d := &ReportDelivery{ReportID: copy.ReportID, OriginRunID: copy.OriginRunID, Status: copy.Status, TargetPath: path, OutputVersion: copy.OutputVersion, DraftArtifactID: copy.DraftArtifactID, RenderedArtifactID: copy.RenderedArtifactID, ReferenceResult: copy.ReferenceResult, SemanticResult: copy.SemanticResult, Issues: append([]string(nil), copy.Issues...)}
	if copy.RenderedArtifactID != "" {
		body, err := r.readReportMaterial(copy.RenderedArtifactID)
		if err != nil {
			return d, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(body)) != copy.OutputVersion {
			return d, r.noteStorageError(fmt.Errorf("%w：报告产物材料摘要不符", errStorage))
		}
		d.Text = string(body)
	} else if copy.Checked.SchemaVersion == 1 {
		d.Text = RenderAnalysis(copy.Checked)
	}
	return d, nil
}

// completeReportWrite 在 action_result 事件已提交后绑定发布身份。
func (r *runState) completeReportWrite(ctx context.Context, actionID string, result schema.ToolResult) error {
	if result.Status != schema.ToolSucceeded || result.Effect != schema.EffectConfirmed {
		return r.reportFailure(result.Output)
	}
	if err := r.record("report_published", func(s *taskState) error {
		if s.Report == nil || s.Report.Status != "prepared" || s.Spec.Report == nil {
			return reportError{"report_invalid", "缺少报告冻结状态"}
		}
		s.Report.Status, s.Report.OutputActionID = "published", actionID
		if s.Quality != nil {
			s.Quality.Status = "published"
		}
		return nil
	}); err != nil {
		return err
	}
	return r.refreshPublishedReport(ctx)
}

func (r *runState) refreshPublishedReport(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.state.Report == nil || r.state.Spec.Report == nil || (r.state.Report.Status != "published" && r.state.Report.Status != "stale") {
		r.mu.Unlock()
		return nil
	}
	copy := *r.state.Report
	path := r.state.Spec.Report.TargetPath
	sources := make(map[string]string, len(copy.SourceVersions))
	for p, v := range copy.SourceVersions {
		sources[p] = v
	}
	r.mu.Unlock()
	if r.qualityEnabled() {
		r.mu.Lock()
		for p, v := range r.state.Quality.SourceVersions {
			sources[p] = v
		}
		r.mu.Unlock()
	}
	var issues []string
	if err := checkScopeDependencies(ctx, copy.ScopeDependencies); err != nil {
		issues = append(issues, err.Error())
	}
	version, err := tools.FileVersion(ctx, path)
	if err != nil || version != copy.OutputVersion {
		issues = append(issues, "报告产物当前版本与发布回执不符")
	}
	for p, expected := range sources {
		actual, err := tools.FileVersion(ctx, p)
		if err != nil || actual != expected {
			issues = append(issues, "来源当前版本已变化："+p)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(issues) == 0 {
		return nil
	}
	return r.record("report_stale", func(s *taskState) error {
		s.Report.Status, s.Report.ReferenceResult, s.Report.ErrorCode = "stale", "stale", "report_stale"
		if s.Quality != nil {
			s.Quality.Status = "stale"
			s.Quality.Unresolved = append(s.Quality.Unresolved, issues...)
			if len(s.Quality.Unresolved) > 192 {
				s.Quality.Unresolved = s.Quality.Unresolved[:192]
			}
		}
		s.Report.Issues = append(s.Report.Issues, issues...)
		if len(s.Report.Issues) > 192 {
			s.Report.Issues = s.Report.Issues[:192]
		}
		return nil
	})
}

// recoverReportPublication 只从已确认的结构化效果账本补记发布，不执行文件写入。
func (r *runState) recoverReportPublication(ctx context.Context) (bool, error) {
	r.mu.Lock()
	state, spec := r.state.Report, r.state.Spec.Report
	if state == nil || spec == nil {
		r.mu.Unlock()
		return false, nil
	}
	if state.Status == "published" || state.Status == "stale" {
		r.mu.Unlock()
		return true, r.refreshPublishedReport(ctx)
	}
	var match string
	if state.Status == "prepared" && r.state.EffectLedger != nil {
		for _, e := range r.state.EffectLedger.Effects {
			if e.Tool == "write_report" && e.Effect == schema.EffectConfirmed && e.File != nil && e.File.Path == spec.TargetPath && e.File.AfterVersion == state.OutputVersion {
				match = e.ActionID
			}
		}
	}
	r.mu.Unlock()
	if match == "" {
		return false, nil
	}
	err := r.completeReportWrite(ctx, match, schema.ToolResult{Status: schema.ToolSucceeded, Effect: schema.EffectConfirmed})
	return true, err
}

// reportSummaryResult 仅生成报告任务的确定性终态，FinalText 只保留模型正常终答语义。
func (r *runState) reportSummaryResult(result RunResult) (RunResult, error) {
	d, err := r.reportDelivery()
	result.Report, result.FinalText = d, ""
	if err != nil {
		return result, err
	}
	if d == nil || (d.Status != "published" && d.Status != "stale") {
		return result, reportError{"report_not_delivered", "报告尚未发布，不能作为已完成答复"}
	}
	result.Status, result.StopCode, result.Reason = NeedsVerification, "needs_verification", "报告已写入；引用已检查，尚未取得独立语义及任务验收证据"
	if d.Status == "stale" {
		result.StopCode, result.Reason = "report_stale", "报告已记录，但源或产物版本已变化，须核实；未重新写入"
	}
	return result, nil
}

func reportJSON(value any) []byte {
	b, _ := json.Marshal(value)
	return b
}
