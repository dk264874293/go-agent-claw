package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
)

func sha256Sum(raw []byte) [32]byte { return sha256.Sum256(raw) }
func (r *runState) prepareQualitySubmission(ctx context.Context, raw []byte) (QualityDraft, error) {
	var sub QualitySubmission
	if err := strictQualityJSON(raw, &sub); err != nil {
		return QualityDraft{}, r.rejectQuality(err)
	}
	r.mu.Lock()
	q := *r.state.Quality
	r.mu.Unlock()
	if sub.DraftID == "" {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "提交缺少必填draft_id；必须使用已登记草稿的准确身份，不自动补填或增加修复请求"))
	}
	if sub.DraftID != q.DraftID {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "提交draft_id与已登记草稿不匹配"))
	}
	if q.RevisionStatus != "completed" {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "唯一作者修订尚无完成回执"))
	}
	if sub.Corrections == nil || len(sub.Corrections) > 64 {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "corrections必须为数组，最多64项；无修订请提交空数组"))
	}
	if err := r.checkQualityVersions(ctx); err != nil {
		return QualityDraft{}, err
	}
	b, err := r.loadQualityBundle()
	if err != nil {
		return QualityDraft{}, err
	}
	review := map[string]QualityReviewFinding{}
	if q.ReviewStatus == "completed" {
		data, err := r.readQualityMaterial(q.ReviewArtifactID)
		if err != nil {
			return QualityDraft{}, err
		}
		var msg struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(data, &msg) != nil {
			return QualityDraft{}, qualityError("quality_outcome_unknown", "复核材料损坏")
		}
		parsed, err := parseQualityReview([]byte(msg.Content), b.HighRiskIDs, b.Draft)
		if err != nil {
			return QualityDraft{}, qualityError("quality_outcome_unknown", "复核材料无有效回执")
		}
		for _, f := range parsed.Findings {
			review[f.FindingID] = f
		}
	}
	original := map[string]QualityFinding{}
	all := map[string]QualityFinding{}
	order := []string{}
	for _, f := range b.Draft.Findings {
		original[f.ID] = f
		all[f.ID] = f
		order = append(order, f.ID)
	}
	checks := append([]quality.Check(nil), b.Draft.Checks...)
	downgrade := map[string]string{}
	changed := map[string]bool{}
	seen := map[string]bool{}
	for _, c := range sub.Corrections {
		if !analysisIDValid(c.FindingID) || seen[c.FindingID] {
			return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "修订身份无效或重复"))
		}
		seen[c.FindingID] = true
		_, exists := all[c.FindingID]
		switch c.Operation {
		case "unverified":
			if !exists || c.Finding != nil || len(c.Checks) > 0 || strings.TrimSpace(c.Reason) == "" {
				return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "待核实修订必须指向已有发现并说明原因"))
			}
			downgrade[c.FindingID] = boundedAnalysisText(c.Reason, 2000)
		case "replace", "add":
			if c.Finding == nil || c.Finding.ID != c.FindingID || exists != (c.Operation == "replace") {
				return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "新增/替换修订的发现身份无效"))
			}
			if err := checkAnalysisStructure(AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{c.Finding.AnalysisFinding}}); err != nil {
				return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", err.Error()))
			}
			all[c.FindingID] = *c.Finding
			changed[c.FindingID] = qualityHash(*c.Finding) != b.Hashes[c.FindingID]
			if !exists {
				order = append(order, c.FindingID)
			}
			if len(c.Checks) > 0 {
				filtered := checks[:0]
				for _, check := range checks {
					if check.FindingID != c.FindingID {
						filtered = append(filtered, check)
					}
				}
				checks = filtered
				for _, check := range c.Checks {
					if check.FindingID != c.FindingID {
						return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "修订检查未绑定对应发现"))
					}
				}
				checks = append(checks, c.Checks...)
			}
		default:
			return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "未知修订操作"))
		}
	}
	if len(all) > 64 || len(checks) > 64 {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "修订超过64条发现或检查"))
	}
	historical := q.OriginRunID != r.runID
	lookup := r.analysisLookup(ctx, true)
	if historical {
		lookup = func(id string) (AnalysisEvidence, error) { return r.qualityTicketEvidence(ctx, id) }
	}
	receipts := quality.Run(checks, r.qualitySourceLookup(ctx, historical))
	if err := r.storageError(); err != nil {
		return QualityDraft{}, err
	}
	result := QualityDraft{SchemaVersion: 1, OverviewIDs: b.Draft.OverviewIDs, Unverified: append([]string(nil), b.Checked.Unverified...)}
	// 原有checked哈希是来源验证的回执，不将新发现自动继承旧模型复核。
	for _, id := range order {
		f := all[id]
		f.Evidence = append([]AnalysisCitation(nil), f.Evidence...)
		reason := downgrade[id]
		for i, ref := range f.Evidence {
			entry, err := lookup(ref.EvidenceID)
			if err != nil {
				reason = "修订引用来源不可用"
				break
			}
			checkedRef, err := ValidateAnalysisCitation(ref, entry)
			if err != nil {
				reason = "修订引用未通过位置/摘录校验"
				break
			}
			f.Evidence[i] = checkedRef
		}
		if !qualityChecksBound(f.AnalysisFinding, receipts) {
			reason = "数值/JSON核验主体没有由该发现自己的实际引用锚定"
		}

		local, hasChecks, checksGood := true, false, true
		for _, c := range receipts {
			if c.FindingID == id {
				hasChecks = true
				local = local && c.PremiseResult == "pass"
				checksGood = checksGood && c.Result == "pass"
			}
		}
		local = local && hasChecks && checksGood
		originalChecks := findingQualityChecks(b.Draft.Checks, id)
		currentChecks := findingQualityChecks(checks, id)
		checksChanged := qualityHash(originalChecks) != qualityHash(currentChecks)
		changed[id] = changed[id] || checksChanged
		if reason == "" && !checksGood {
			reason = "本地检查存在反例、错误或来源不足"
		}
		risk := qualityRisk(f, receipts)
		if reason == "" && changed[id] {
			accepted := local || !risk
			if item, ok := review[id]; ok && item.Verdict == "revise" && item.Replacement != nil && qualityHash(*item.Replacement) == qualityHash(all[id]) && (!checksChanged || qualityHash(item.Checks) == qualityHash(currentChecks)) {
				accepted = true
			}
			if !accepted {
				reason = "修订/新增语义没有对应复核或可证明的本地依据"
			}
		} else if reason == "" && risk {
			item, ok := review[id]
			if !ok || item.Verdict != "supported" {
				reason = "高风险发现尚未获得有效复核支持"
			}
		}
		if reason == "" {
			if issue := reportClaimIssue(f.AnalysisFinding, reportClaimMeta{ID: f.ID, ClaimType: f.ClaimType, Conditions: f.Conditions}, lookup); issue != "" {
				reason = issue
			}
		}
		if reason != "" {
			result.Unverified = append(result.Unverified, boundedAnalysisText(id+"（"+reason+"）："+f.Statement, 4000))
			continue
		}
		if hasChecks {
			// 免复核的算式/对象票据只发布程序可以证明的范围；模型自由背景可能夹带另一条公式。
			f.Context = "仅限引用的来源版本、实际选择对象及下列输入样本。"
			f.ClaimType, f.Conditions = "general", reportConditions{}
			// 数学和JSON正式正文完全由检查器结果生成，不沿用可能漏项的模型公式。
			var generated []string
			for _, c := range receipts {
				if c.FindingID == id {
					generated = append(generated, quality.RenderReceipt(c))
				}
			}
			f.Statement = strings.Join(generated, "；")
			if len(f.Statement) > 4000 {
				return QualityDraft{}, r.rejectQuality(qualityError("quality_contract_invalid", "核验结果正文超过单条发现上限，请减少草稿样本"))
			}
			f.Limitations = "仅证明所列对象、输入和样本；本地计算不等同于代码执行或独立语义验收。"
		}
		if !hasChecks && !risk {
			f.Context = "仅限引用的来源版本及实际读取范围；逐字材料事实。"
			f.Limitations = "未独立验证材料陈述的运行效果。"
			f.ClaimType, f.Conditions = "general", reportConditions{}
		}
		result.Findings = append(result.Findings, f)
	}
	if err := r.storageError(); err != nil {
		return QualityDraft{}, err
	}
	checked, err := ValidateAnalysisReport(result.report(), lookup)
	if err != nil {
		return QualityDraft{}, r.rejectQuality(qualityError("quality_not_ready", "全部核心发现缺少可用的引用、核验或复核依据"))
	}
	if !historical {
		checked = r.qualityScopeFilter(ctx, checked)
		checked, err = ValidateAnalysisReport(checked, lookup)
		if err != nil {
			return QualityDraft{}, r.rejectQuality(qualityError("quality_not_ready", "核心发现依赖的读取范围不完整"))
		}
	}
	keep := map[string]AnalysisFinding{}
	for _, f := range checked.Findings {
		keep[f.ID] = f
	}
	final := result.Findings[:0]
	for _, f := range result.Findings {
		if ref, ok := keep[f.ID]; ok {
			f.AnalysisFinding = ref
			final = append(final, f)
		}
	}
	result.Findings = final
	result.OverviewIDs = checked.OverviewIDs
	result.Unverified = checked.Unverified
	versions := map[string]string{}
	for _, f := range result.Findings {
		for _, ref := range f.Evidence {
			versions[ref.Path] = ref.Version
		}
	}
	// 保留草稿全部来源，撤回的发现也不能让来源改变逃过冻结票据检查。
	for p, v := range q.SourceVersions {
		versions[p] = v
	}
	evidenceHashes := map[string]string{}
	for id, hash := range q.EvidenceHashes {
		evidenceHashes[id] = hash
	}
	if !historical {
		temp := result
		temp.Checks = checks
		for _, id := range draftQualityEvidenceIDs(temp) {
			if entry, err := lookup(id); err == nil {
				evidenceHashes[id] = fmt.Sprintf("%x", sha256Sum(entry.Source))
				versions[entry.Receipt.Path] = entry.Receipt.Version
			}
		}
	}
	if err := r.qualityReadingGate(ctx, historical); err != nil {
		return QualityDraft{}, err
	}
	dependencies := r.finalQualityDependencies(ctx, checked, historical)
	summaryID, err := r.saveQualityJSON(struct {
		Report      QualityDraft      `json:"report"`
		Receipts    []quality.Receipt `json:"receipts"`
		DraftSHA256 string            `json:"draft_sha256"`
	}{result, receipts, q.DraftSHA256})
	if err != nil {
		return QualityDraft{}, err
	}
	_ = summaryID // 完整复验材料由受限状态目录持有，下方记录身份以免孤立。
	if err = r.record("quality_ready", func(s *taskState) error {
		s.Quality.Status = "ready"
		s.Quality.SourceVersions = versions
		s.Quality.EvidenceHashes = evidenceHashes
		s.Quality.Unresolved = append([]string(nil), checked.Unverified...)
		s.Quality.FinalCheckArtifactID = summaryID
		s.Quality.ScopeDependencies = dependencies
		return nil
	}); err != nil {
		return QualityDraft{}, err
	}
	return result, nil
}
func (r *runState) freezeQualityBytes(body []byte) error {
	if err := r.storageError(); err != nil {
		return err
	}
	id, err := r.saveQualityMaterial(body)
	if err != nil {
		return err
	}
	return r.record("quality_frozen", func(s *taskState) error {
		s.Quality.FrozenArtifactID = id
		s.Quality.FrozenSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
		return nil
	})
}
func (r *runState) acceptQualityAnalysis(ctx context.Context, d QualityDraft) (AnalysisReport, error) {
	lookup := r.analysisLookup(ctx, true)
	if r.state.Quality.OriginRunID != r.runID {
		lookup = func(id string) (AnalysisEvidence, error) { return r.qualityTicketEvidence(ctx, id) }
	}
	checked, err := ValidateAnalysisReport(d.report(), lookup)
	if err != nil {
		return checked, qualityError("quality_not_ready", "质量分析没有有效核心结论")
	}
	if err := r.checkQualityVersions(ctx); err != nil {
		return checked, err
	}
	if r.state.Quality.OriginRunID == r.runID {
		if err := r.checkReadingComplete(ctx); err != nil {
			return checked, err
		}
	}
	body, _ := json.Marshal(d)
	if r.state.Quality.FrozenArtifactID == "" {
		if err := r.freezeQualityBytes(body); err != nil {
			return checked, err
		}
	}
	err = r.record("quality_analysis_published", func(s *taskState) error {
		a := ensureAnalysisState(s)
		a.Reports[observe.Identity(ctx).AgentID] = checked
		s.Quality.Status = "published"
		return nil
	})
	return checked, err
}

func findingQualityChecks(checks []quality.Check, id string) []quality.Check {
	out := []quality.Check{}
	for _, c := range checks {
		if c.FindingID == id {
			out = append(out, c)
		}
	}
	return out
}

// 核验主体必须由该发现自己的有效引用锚定，不能用别的文档证明当前引用。
func qualityChecksBound(f AnalysisFinding, receipts []quality.Receipt) bool {
	for _, check := range receipts {
		if check.FindingID != f.ID {
			continue
		}
		for _, source := range check.Sources {
			anchored := false
			for _, ref := range f.Evidence {
				if ref.Path == source.Path && ref.Version == source.Version && ((source.ByteStart <= ref.ByteStart && ref.ByteEnd <= source.ByteEnd) || (ref.ByteStart <= source.ByteStart && source.ByteEnd <= ref.ByteEnd)) {
					anchored = true
					break
				}
			}
			if !anchored {
				return false
			}
		}
	}
	return true
}
