package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// 恢复仅核对质量票据冻结的完整性依赖，不复活普通读取目录或扫描新成员。
func (r *runState) qualityReadingGate(ctx context.Context, historical bool) error {
	if !historical {
		return r.checkReadingComplete(ctx)
	}
	scope := r.state.Spec.ReadScope
	if scope == nil || !scope.RequireComplete {
		return nil
	}
	selectors := append([]string(nil), scope.Paths...)
	sort.Strings(selectors)
	id := coverageHash(struct {
		Task, Owner, Kind, Glob string
		Selectors               []string
	}{r.state.Spec.ID, "", "declared", scope.NameGlob, selectors})
	for _, dep := range r.state.Quality.ScopeDependencies {
		if dep.ScopeID == id {
			return checkScopeDependencies(ctx, []ReportScopeDependency{dep})
		}
	}
	return reportError{"reading_incomplete", "质量票据没有可核对的声明范围完整性依据，不重新补读或补造覆盖"}
}
func (r *runState) finalQualityDependencies(ctx context.Context, report AnalysisReport, historical bool) []ReportScopeDependency {
	deps := append([]ReportScopeDependency(nil), r.state.Quality.ScopeDependencies...)
	if !historical {
		for _, f := range report.Findings {
			if f.ScopeRequirement == "manifest" {
				c := r.readingCoverage(ctx)
				if c != nil && c.Status == "complete" {
					deps = append(deps, dependencyFromCoverage(c))
				}
			}
			if f.ScopeRequirement == "file" {
				for _, ref := range f.Evidence {
					c := r.coverageForPaths(ctx, []string{ref.Path})
					if c != nil && c.Status == "complete" {
						deps = append(deps, dependencyFromCoverage(c))
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	var out []ReportScopeDependency
	for _, dep := range deps {
		key := dep.ScopeID + ":" + dep.ScopeRevision
		if !seen[key] {
			seen[key] = true
			out = append(out, dep)
		}
	}
	return out
}

// 已完成复验、尚未准备Pending的恢复仍需正常写工具准入；正文只从原票据重建。
func (r *runState) prepareRecoveredQualityReport(ctx context.Context) ([]byte, error) {
	q := r.state.Quality
	if q == nil || q.FinalCheckArtifactID == "" || q.RevisionStatus != "completed" {
		return nil, qualityError("quality_outcome_unknown", "没有已完成的质量提交票据")
	}
	if err := r.checkQualityVersions(ctx); err != nil {
		return nil, err
	}
	if err := r.qualityReadingGate(ctx, true); err != nil {
		return nil, err
	}
	raw, err := r.readQualityMaterial(q.FinalCheckArtifactID)
	if err != nil {
		return nil, err
	}
	var saved struct {
		Report      QualityDraft `json:"report"`
		DraftSHA256 string       `json:"draft_sha256"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.DraftSHA256 != q.DraftSHA256 {
		return nil, r.noteStorageError(fmt.Errorf("%w：最终质量票据摘要无效", errStorage))
	}
	checked, err := ValidateAnalysisReport(saved.Report.report(), func(id string) (AnalysisEvidence, error) { return r.qualityTicketEvidence(ctx, id) })
	if storageErr := r.storageError(); storageErr != nil {
		return nil, storageErr
	}
	if err != nil {
		return nil, qualityError("quality_not_ready", "质量票据来源或依赖已失效")
	}
	versions := map[string]string{}
	for _, f := range checked.Findings {
		for _, ref := range f.Evidence {
			versions[ref.Path] = ref.Version
		}
	}
	body := []byte("# 证据报告\n\n" + RenderAnalysis(checked) + "\n")
	preview, err := tools.PreviewReportWrite(ctx, r.state.Spec.Report.TargetPath, body)
	if err != nil {
		return nil, reportError{"report_target_conflict", err.Error()}
	}
	rendered, err := r.saveQualityMaterial(body)
	if err != nil {
		return nil, err
	}
	state := &ReportState{Version: 1, ReportID: newID(), OriginRunID: q.OriginRunID, Status: "prepared", DraftArtifactID: q.FinalCheckArtifactID, RenderedArtifactID: rendered, SourceVersions: versions, ScopeDependencies: q.ScopeDependencies, OutputVersion: preview.After, ReferenceResult: "pass", SemanticResult: "not_run", Checked: checked}
	if len(checked.Unverified) > 0 {
		state.ReferenceResult = "partial"
	}
	copy := r.state
	copy.Report = state
	if err := validateReportState(copy); err != nil {
		return nil, qualityError("quality_not_ready", "票据缺少可核对范围或发布信息："+err.Error())
	}
	if err := r.record("quality_report_reprepared", func(s *taskState) error {
		s.Report = state
		s.Quality.FrozenArtifactID = rendered
		s.Quality.FrozenSHA256 = preview.After
		return nil
	}); err != nil {
		return nil, err
	}
	return body, nil
}
