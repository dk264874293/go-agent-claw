package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
)

type QualityFinding struct {
	AnalysisFinding
	ClaimType  string           `json:"claim_type,omitempty"`
	Conditions reportConditions `json:"conditions,omitempty"`
}
type QualityDraft struct {
	SchemaVersion int              `json:"schema_version"`
	Findings      []QualityFinding `json:"findings"`
	OverviewIDs   []string         `json:"overview_ids,omitempty"`
	Unverified    []string         `json:"unverified,omitempty"`
	Checks        []quality.Check  `json:"checks,omitempty"`
}
type QualityCorrection struct {
	Operation string          `json:"operation"`
	FindingID string          `json:"finding_id"`
	Finding   *QualityFinding `json:"finding,omitempty"`
	Checks    []quality.Check `json:"checks,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}
type QualitySubmission struct {
	DraftID     string              `json:"draft_id"`
	Corrections []QualityCorrection `json:"corrections"`
}
type QualityReviewFinding struct {
	FindingID   string          `json:"finding_id"`
	Verdict     string          `json:"verdict"`
	Reason      string          `json:"reason"`
	Replacement *QualityFinding `json:"replacement,omitempty"`
	Checks      []quality.Check `json:"checks,omitempty"`
}
type QualityReview struct {
	SchemaVersion int                    `json:"schema_version"`
	Findings      []QualityReviewFinding `json:"findings"`
}
type qualityBundle struct {
	Draft       QualityDraft      `json:"draft"`
	Checked     AnalysisReport    `json:"checked"`
	Receipts    []quality.Receipt `json:"receipts"`
	HighRiskIDs []string          `json:"high_risk_ids"`
	Hashes      map[string]string `json:"finding_hashes"`
}

func strictQualityJSON(raw []byte, v any) error {
	if len(raw) > 256*1024 {
		return qualityError("quality_contract_invalid", "质量协议超过256KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return qualityError("quality_contract_invalid", "质量JSON无效："+err.Error())
	}
	if dec.Decode(new(any)) != io.EOF {
		return qualityError("quality_contract_invalid", "质量协议必须为单个完整JSON对象")
	}
	return nil
}
func qualityHash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (d QualityDraft) report() AnalysisReport {
	r := AnalysisReport{SchemaVersion: d.SchemaVersion, OverviewIDs: d.OverviewIDs, Unverified: d.Unverified}
	for _, f := range d.Findings {
		r.Findings = append(r.Findings, f.AnalysisFinding)
	}
	return r
}
func (r *runState) loadQualityBundle() (qualityBundle, error) {
	r.mu.Lock()
	q := r.state.Quality
	id := ""
	if q != nil {
		id = q.CheckedArtifactID
	}
	r.mu.Unlock()
	var b qualityBundle
	if id == "" {
		return b, qualityError("quality_outcome_unknown", "草稿核验缺少完成回执，不能重新核验")
	}
	raw, err := r.readQualityMaterial(id)
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal(raw, &b); err != nil {
		return b, r.noteStorageError(fmt.Errorf("%w：质量检查材料损坏", errStorage))
	}
	return b, nil
}
func (r *runState) checkQualityDraft(ctx context.Context, raw json.RawMessage) (string, error) {
	r.mu.Lock()
	q := r.state.Quality
	allowed := q != nil && q.Status == "exploring" && q.DraftID == ""
	r.mu.Unlock()
	if !allowed {
		return "", qualityError("quality_contract_invalid", "每个质量任务只允许一次草稿核验，登记后关闭探索")
	}
	id, err := r.saveQualityMaterial(raw)
	if err != nil {
		return "", err
	}
	if err = r.record("quality_draft", func(s *taskState) error {
		q := s.Quality
		if q == nil || q.DraftID != "" {
			return qualityError("quality_contract_invalid", "草稿已登记")
		}
		q.DraftID = newID()
		q.OriginRunID = r.runID
		q.AgentID = observe.Identity(ctx).AgentID
		q.DraftArtifactID = id
		q.DraftSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		q.Status = "checking"
		return nil
	}); err != nil {
		return "", err
	}
	var d QualityDraft
	if err = strictQualityJSON(raw, &d); err != nil {
		return "", r.rejectQuality(err)
	}
	if err = checkAnalysisStructure(d.report()); err != nil {
		return "", r.rejectQuality(qualityError("quality_contract_invalid", err.Error()))
	}
	if len(d.Findings) == 0 || len(d.Checks) > 64 {
		return "", r.rejectQuality(qualityError("quality_contract_invalid", "草稿须有发现，检查最多64项"))
	}
	findingIDs := map[string]bool{}
	for _, f := range d.Findings {
		findingIDs[f.ID] = true
	}
	for _, c := range d.Checks {
		if !findingIDs[c.FindingID] {
			return "", r.rejectQuality(qualityError("quality_contract_invalid", "检查未绑定当前草稿发现"))
		}
	}
	lookup := r.analysisLookup(ctx, true)
	original := d.report()
	for i, f := range d.Findings {
		if reason := reportClaimIssue(f.AnalysisFinding, reportClaimMeta{ID: f.ID, ClaimType: f.ClaimType, Conditions: f.Conditions}, lookup); reason != "" {
			original.Findings[i].Kind = "unverified"
			appendAnalysisUnverified(&original, f.ID+"："+reason)
		}
	}
	checked, _ := ValidateAnalysisReport(original, lookup)
	checked = r.qualityScopeFilter(ctx, checked)
	receipts := quality.Run(d.Checks, r.qualitySourceLookup(ctx, false))
	if err := r.storageError(); err != nil {
		return "", err
	}
	bundle := qualityBundle{Draft: d, Checked: checked, Receipts: receipts, Hashes: map[string]string{}}
	versions := map[string]string{}
	for _, f := range checked.Findings {
		for _, ref := range f.Evidence {
			versions[ref.Path] = ref.Version
		}
	}
	for _, c := range receipts {
		for _, source := range c.Sources {
			versions[source.Path] = source.Version
		}
	}
	for _, f := range d.Findings {
		bundle.Hashes[f.ID] = qualityHash(f)
		if qualityRisk(f, receipts) {
			bundle.HighRiskIDs = append(bundle.HighRiskIDs, f.ID)
		}
	}
	evidenceHashes := map[string]string{}
	for _, id := range draftQualityEvidenceIDs(d) {
		if entry, err := lookup(id); err == nil {
			evidenceHashes[id] = fmt.Sprintf("%x", sha256Sum(entry.Source))
			versions[entry.Receipt.Path] = entry.Receipt.Version
		}
	}
	checkedID, err := r.saveQualityJSON(bundle)
	if err != nil {
		return "", err
	}
	result := "pass"
	for _, c := range receipts {
		if c.Result != "pass" || c.PremiseResult != "pass" {
			result = "partial"
		}
	}
	if len(checked.Findings) == 0 {
		result = "fail"
	}
	var deps []ReportScopeDependency
	coverage := r.readingCoverage(ctx)
	if r.state.Spec.ReadScope != nil && r.state.Spec.ReadScope.RequireComplete && coverage != nil && coverage.Status == "complete" {
		deps = append(deps, dependencyFromCoverage(coverage))
	}
	for _, f := range checked.Findings {
		if f.ScopeRequirement == "manifest" && coverage != nil && coverage.Status == "complete" {
			deps = append(deps, dependencyFromCoverage(coverage))
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
	err = r.record("quality_checked", func(s *taskState) error {
		q := s.Quality
		q.CheckedArtifactID = checkedID
		q.SourceVersions = versions
		q.EvidenceHashes = evidenceHashes
		q.ScopeDependencies = deps
		q.Status = "checked"
		q.CheckResult = result
		return nil
	})
	if err != nil {
		return "", err
	}
	out := struct {
		DraftID    string `json:"draft_id"`
		Status     string `json:"status"`
		Checks     int    `json:"checks,omitempty"`
		Risk       int    `json:"risk_items,omitempty"`
		Unverified int    `json:"unverified,omitempty"`
		Hint       string `json:"hint"`
	}{DraftID: r.state.Quality.DraftID, Status: result, Checks: len(receipts), Risk: len(bundle.HighRiskIDs), Unverified: len(checked.Unverified), Hint: "探索已关闭；引擎将按风险复核并提供一次修订/提交机会。检查通过不等于语义验收。"}
	var issues []string
	for _, receipt := range receipts {
		if receipt.Result != "pass" || receipt.PremiseResult != "pass" {
			issues = append(issues, boundedAnalysisText(receipt.FindingID+"："+quality.RenderReceipt(receipt)+" "+receipt.Reason, 350))
		}
	}
	issues = append(issues, checked.Unverified...)
	omitted := max(0, len(issues)-4)
	if len(issues) > 4 {
		issues = issues[:4]
	}
	for i := range issues {
		issues[i] = boundedAnalysisText(issues[i], 350)
	}
	data, _ := json.Marshal(map[string]any{"draft_id": out.DraftID, "status": out.Status, "checks": out.Checks, "risk_items": out.Risk, "unverified": out.Unverified, "hint": out.Hint, "issues": issues, "issues_omitted": omitted})
	return string(data), nil
}
func (r *runState) rejectQuality(err error) error {
	code := "quality_contract_invalid"
	if detail, ok := err.(reportError); ok {
		code = detail.code
	}
	if saved := r.record("quality_rejected", func(s *taskState) error {
		if s.Quality != nil {
			s.Quality.Status = "blocked"
			s.Quality.ErrorCode = code
			s.Quality.Unresolved = append(s.Quality.Unresolved, boundedAnalysisText(err.Error(), 1000))
		}
		return nil
	}); saved != nil {
		return saved
	}
	return err
}
func qualityRisk(f QualityFinding, checks []quality.Receipt) bool {
	if f.Kind != "fact" || f.ClaimType != "" && f.ClaimType != "general" {
		return true
	}
	hasCheck := false
	for _, c := range checks {
		if c.FindingID == f.ID {
			hasCheck = true
			if c.Result != "pass" || c.PremiseResult != "pass" {
				return true
			}
		}
	}
	if hasCheck {
		return false
	}
	// 唯有原样转述能免复核；引用存在不证明改写后的语义。
	return len(f.Evidence) != 1 || strings.TrimSpace(f.Statement) != strings.TrimSpace(f.Evidence[0].Quote)
}
func (r *runState) qualityScopeFilter(ctx context.Context, report AnalysisReport) AnalysisReport {
	var kept []AnalysisFinding
	for _, f := range report.Findings {
		complete := true
		if f.ScopeRequirement == "manifest" {
			c := r.readingCoverage(ctx)
			complete = c != nil && c.Status == "complete" && f.ScopeID == c.ScopeID && f.ScopeRevision == c.ScopeRevision
		}
		if f.ScopeRequirement == "file" {
			complete = len(f.Evidence) > 0
			for _, ref := range f.Evidence {
				c := r.coverageForPaths(ctx, []string{ref.Path})
				complete = complete && c != nil && c.Status == "complete"
			}
		}
		if complete {
			kept = append(kept, f)
		} else {
			appendAnalysisUnverified(&report, f.ID+"：所需范围读取不完整或修订不匹配")
		}
	}
	report.Findings = kept
	return report
}

func draftQualityEvidenceIDs(d QualityDraft) []string {
	ids := map[string]bool{}
	for _, f := range d.Findings {
		for _, ref := range f.Evidence {
			ids[ref.EvidenceID] = true
		}
	}
	for _, c := range d.Checks {
		for _, id := range c.Subject.EvidenceIDs {
			ids[id] = true
		}
		for _, b := range c.Bindings {
			if b.Source != nil {
				for _, id := range b.Source.EvidenceIDs {
					ids[id] = true
				}
			}
		}
	}
	return sortedQualityIDs(ids)
}
