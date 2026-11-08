package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func (span *DeferredReadRange) UnmarshalJSON(raw []byte) error {
	var input struct {
		ByteStart *int64 `json:"byte_start"`
		ByteEnd   *int64 `json:"byte_end"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return err
	}
	if input.ByteStart == nil || input.ByteEnd == nil {
		return errors.New("wait_for须成对提供非null的源字节范围")
	}
	span.ByteStart, span.ByteEnd = *input.ByteStart, *input.ByteEnd
	return nil
}

func (r *runState) validateDeliveryDeferredUpdates(ctx context.Context, report AnalysisReport) error {
	for _, read := range report.DeferredReads {
		if read.WaitFor == nil {
			continue
		}
		e, err := r.analysisLookup(ctx, true)(read.EvidenceID)
		if err != nil || e.AgentID != observe.Identity(ctx).AgentID || e.RunID != r.runID || read.WaitFor.ByteStart < 0 || read.WaitFor.ByteEnd <= read.WaitFor.ByteStart || read.WaitFor.ByteEnd > e.Receipt.TotalBytes {
			return errors.New("wait_for须锚定本人本轮已交付来源，且位于源文件总字节内")
		}
	}
	return r.storageError()
}

// SourceReviewInput 是模型对某文档的处置声明，不表示原文完整或语义正确。
type SourceReviewInput struct {
	EvidenceIDs []string `json:"evidence_ids"`
	FindingIDs  []string `json:"finding_ids,omitempty"`
	CheckIDs    []string `json:"check_ids,omitempty"`
	Disposition string   `json:"disposition"`
	Reason      string   `json:"reason"`
}

// SourceReview 由引擎绑定来源身份；引用发现改变后不能借用此前的处置声明。
type SourceReview struct {
	SourceReviewInput
	AgentID        string            `json:"agent_id"`
	RunID          string            `json:"run_id"`
	Path           string            `json:"path"`
	Version        string            `json:"version"`
	FindingDigests map[string]string `json:"finding_digests,omitempty"`
}

func sourceReviewKey(agent, run, path, version string) string {
	return agent + "\x00" + run + "\x00" + path + "\x00" + version
}
func sourceReviewID(key string) string { return fmt.Sprintf("D%x", sha256.Sum256([]byte(key)))[:17] }
func sourceFindingDigest(f AnalysisFinding) string {
	b, _ := json.Marshal(f)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// 默认模式保持原解析行为：过去未知的wait_for不会因新增字段而获得新语义。
func (r *runState) parseWorkNotesReport(raw []byte) (AnalysisReport, []SourceReviewInput, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return AnalysisReport{}, nil, errors.New("笔记必须为对象")
	}
	var updates []SourceReviewInput
	if r.deliveryReviewEnabled() {
		if data, ok := fields["source_reviews"]; ok {
			if string(data) == "null" || json.Unmarshal(data, &updates) != nil || len(updates) > 64 {
				return AnalysisReport{}, nil, errors.New("文档处置目录格式无效")
			}
		}
	} else {
		if data, ok := fields["deferred_reads"]; ok {
			var reads []map[string]json.RawMessage
			if json.Unmarshal(data, &reads) == nil {
				for _, read := range reads {
					delete(read, "wait_for")
				}
				fields["deferred_reads"], _ = json.Marshal(reads)
			}
		}
	}
	delete(fields, "source_reviews")
	body, _ := json.Marshal(fields)
	report, err := ParseAnalysisReport(string(body))
	return report, updates, err
}

func deliveryWorkNotesKey(report AnalysisReport, reviews []SourceReviewInput) string {
	body, _ := json.Marshal(struct {
		Report  AnalysisReport
		Reviews []SourceReviewInput
	}{report, reviews})
	return analysisMessageKey(string(body))
}

// 扩展只投影到当前任务的Schema副本，不更改注册表或旧任务的定义。
func (r *runState) withDeliveryReviewNotesSchema(carrier map[string]any) {
	if !r.deliveryReviewEnabled() {
		return
	}
	props := carrier["properties"].(map[string]any)
	props["source_reviews"] = map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "object", "required": []string{"evidence_ids", "disposition", "reason"}, "properties": map[string]any{
		"evidence_ids": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
		"finding_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"check_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"disposition":  map[string]any{"type": "string", "enum": []string{"addressed", "not_relevant", "unverified"}},
		"reason":       map[string]any{"type": "string", "description": "说明已采用的发现、与任务无关的依据，或仍未解决的问题；声明不等于语义通过"},
	}}}
	deferred := props["deferred_reads"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	deferred["wait_for"] = map[string]any{"type": "object", "required": []string{"byte_start", "byte_end"}, "properties": map[string]any{"byte_start": map[string]any{"type": "integer", "minimum": 0}, "byte_end": map[string]any{"type": "integer", "minimum": 1}}, "description": "原证据锚定文件的绝对源字节半开区间，仅等待材料，不自动处理或折叠"}
}

func sourceReviewShape(input SourceReviewInput) error {
	if len(input.EvidenceIDs) == 0 || len(input.EvidenceIDs) > 128 || len(input.FindingIDs) > 64 || len(input.CheckIDs) > 64 || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2000 {
		return errors.New("文档处置引用或理由无效")
	}
	if input.Disposition != "addressed" && input.Disposition != "not_relevant" && input.Disposition != "unverified" {
		return errors.New("文档处置只能为addressed/not_relevant/unverified")
	}
	if input.Disposition == "addressed" && len(input.FindingIDs) == 0 {
		return errors.New("addressed须关联有效发现")
	}
	for _, ids := range [][]string{input.EvidenceIDs, input.FindingIDs, input.CheckIDs} {
		seen := map[string]bool{}
		for _, id := range ids {
			if !analysisIDValid(id) || seen[id] {
				return errors.New("文档处置身份无效或重复")
			}
			seen[id] = true
		}
	}
	return nil
}

func (r *runState) prepareSourceReview(ctx context.Context, input SourceReviewInput, report AnalysisReport) (SourceReview, error) {
	result := SourceReview{SourceReviewInput: input, RunID: r.runID, AgentID: observe.Identity(ctx).AgentID, FindingDigests: map[string]string{}}
	if err := sourceReviewShape(input); err != nil {
		return result, err
	}
	for _, id := range input.EvidenceIDs {
		e, err := r.analysisLookup(ctx, true)(id)
		if err != nil || e.RunID != r.runID || e.AgentID != result.AgentID {
			return result, errors.New("文档处置须使用本人本轮已交付来源")
		}
		if result.Path == "" {
			result.Path, result.Version = e.Receipt.Path, e.Receipt.Version
		}
		if e.Receipt.Path != result.Path || e.Receipt.Version != result.Version {
			return result, errors.New("一次文档处置不能混合文件或版本")
		}
	}
	for _, id := range input.FindingIDs {
		found := false
		for _, f := range report.Findings {
			if f.ID != id {
				continue
			}
			for _, ref := range f.Evidence {
				if ref.Path == result.Path && ref.Version == result.Version && ref.Status == "valid" {
					found = true
				}
			}
			// 检查器使用的规范状态以机械校验后的引用为准；旧合法引用允许空状态。
			if !found {
				for _, ref := range f.Evidence {
					if ref.Path == result.Path && ref.Version == result.Version && ref.Status != "invalid" {
						if e, err := r.analysisLookup(ctx, true)(ref.EvidenceID); err == nil && e.RunID == r.runID && e.AgentID == result.AgentID {
							found = true
						}
					}
				}
			}
			if found {
				result.FindingDigests[id] = sourceFindingDigest(f)
			}
		}
		if !found {
			return result, errors.New("文档处置关联发现未引用当前文档")
		}
	}
	r.mu.Lock()
	checks := append([]LocalCheckSummary(nil), r.state.LocalChecks...)
	r.mu.Unlock()
	for _, id := range input.CheckIDs {
		found := false
		for _, check := range checks {
			if check.ID != id || check.OriginRunID != r.runID || check.AgentID != result.AgentID {
				continue
			}
			for _, source := range check.Sources {
				if source.Path == result.Path && source.Version == result.Version {
					found = true
				}
			}
		}
		if !found {
			return result, errors.New("文档处置关联核对不属于当前来源")
		}
	}
	return result, nil
}

// 处置按文档合并，不改变ProcessedReads、摘要、覆盖或任何折叠资格。
func (r *runState) acceptSourceReviews(ctx context.Context, updates []SourceReviewInput, report AnalysisReport, seen map[string]bool) (int, bool, error) {
	if !r.deliveryReviewEnabled() || len(updates) == 0 {
		return 0, false, nil
	}
	var accepted []SourceReview
	conflict := false
	for _, update := range updates {
		review, err := r.prepareSourceReview(ctx, update, report)
		if err != nil {
			r.analysisIssue(ctx, "文档处置未接受："+err.Error())
			continue
		}
		key := sourceReviewKey(review.AgentID, review.RunID, review.Path, review.Version)
		if seen[key] {
			conflict = true
			continue
		}
		seen[key] = true
		r.mu.Lock()
		unchanged := false
		if r.state.ReadMemory != nil {
			for _, old := range r.state.ReadMemory.SourceReviews {
				if sourceReviewKey(old.AgentID, old.RunID, old.Path, old.Version) == key {
					a, _ := json.Marshal(old)
					b, _ := json.Marshal(review)
					unchanged = bytes.Equal(a, b)
					break
				}
			}
		}
		r.mu.Unlock()
		if unchanged {
			conflict = true
			continue
		}
		accepted = append(accepted, review)
	}
	if err := r.storageError(); err != nil {
		return 0, conflict, err
	}
	if len(accepted) == 0 {
		return 0, conflict, nil
	}
	err := r.record("source_reviews_updated", func(s *taskState) error {
		memory := ensureReadMemory(s)
		for _, review := range accepted {
			key := sourceReviewKey(review.AgentID, review.RunID, review.Path, review.Version)
			replaced := false
			for i, old := range memory.SourceReviews {
				if sourceReviewKey(old.AgentID, old.RunID, old.Path, old.Version) == key {
					memory.SourceReviews[i] = review
					replaced = true
					break
				}
			}
			if !replaced {
				memory.SourceReviews = append(memory.SourceReviews, review)
			}
		}
		return nil
	})
	return len(accepted), conflict, err
}

func validateDeliveryReviewMemory(s taskState) error {
	if s.ReadMemory == nil {
		return nil
	}
	enabled := s.Spec.DeliveryReview != nil && s.Spec.DeliveryReview.Version == 1
	if len(s.ReadMemory.SourceReviews) > 0 && !enabled {
		return errors.New("文档处置仅属于显式交付处理任务")
	}
	seen := map[string]bool{}
	for _, review := range s.ReadMemory.SourceReviews {
		if err := sourceReviewShape(review.SourceReviewInput); err != nil {
			return err
		}
		if review.AgentID == "" || review.RunID == "" || !filepath.IsLocal(review.Path) || review.Version == "" {
			return errors.New("文档处置绑定身份无效")
		}
		key := sourceReviewKey(review.AgentID, review.RunID, review.Path, review.Version)
		if seen[key] {
			return errors.New("文档处置重复")
		}
		seen[key] = true
		for _, id := range review.EvidenceIDs {
			e, ok := s.ReadMemory.Evidence[id]
			if !ok || e.AgentID != review.AgentID || e.RunID != review.RunID || e.Receipt.Path != review.Path || e.Receipt.Version != review.Version {
				return errors.New("文档处置来源绑定无效")
			}
		}
		for _, id := range review.FindingIDs {
			if len(review.FindingDigests[id]) != 64 {
				return errors.New("文档处置缺少发现指纹")
			}
		}
		for _, id := range review.CheckIDs {
			found := false
			for _, check := range s.LocalChecks {
				if check.ID == id && check.AgentID == review.AgentID && check.OriginRunID == review.RunID {
					for _, source := range check.Sources {
						if source.Path == review.Path && source.Version == review.Version {
							found = true
						}
					}
				}
			}
			if !found {
				return errors.New("文档处置核对绑定无效")
			}
		}
	}
	for _, report := range s.ReadMemory.Notes {
		for _, read := range report.DeferredReads {
			if read.WaitFor == nil {
				continue
			}
			e, ok := s.ReadMemory.Evidence[read.EvidenceID]
			if !enabled || !ok || read.WaitFor.ByteStart < 0 || read.WaitFor.ByteEnd <= read.WaitFor.ByteStart || read.WaitFor.ByteEnd > e.Receipt.TotalBytes {
				return errors.New("等待来源范围无效")
			}
		}
	}
	return nil
}

type deliveryReviewDocument struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Version     string   `json:"version"`
	EvidenceIDs []string `json:"evidence_ids"`
	Disposition string   `json:"disposition"`
	Coverage    string   `json:"coverage"`
	Reason      string   `json:"reason,omitempty"`
}

type deliveryReviewCatalog struct {
	Documents []deliveryReviewDocument
	Issues    []DeliveryReviewIssue
}

func (r *runState) deliveryReviewCatalog(ctx context.Context) deliveryReviewCatalog {
	result := deliveryReviewCatalog{}
	if !r.deliveryReviewEnabled() {
		return result
	}
	agent := observe.Identity(ctx).AgentID
	r.mu.Lock()
	entries := map[string]AnalysisEvidence{}
	invalidated := map[string]DeliveryReviewIssue{}
	for id, e := range r.analysisRuntime.sources {
		if e.AgentID == agent && e.RunID == r.runID && r.analysisRuntime.invalidated[id] != "" {
			key := sourceReviewKey(agent, r.runID, e.Receipt.Path, e.Receipt.Version)
			invalidated[key] = DeliveryReviewIssue{ID: "unavailable_" + sourceReviewID(key), Kind: "source_unavailable", Path: e.Receipt.Path, Version: e.Receipt.Version, Reason: "先前读取资格已失效，不能借旧处置声明恢复当前依据；需要时重新取证"}
		}
		if e.AgentID == agent && e.RunID == r.runID && r.analysisRuntime.delivered[agent][id] {
			entries[id] = e
		}
	}
	var reviews []SourceReview
	report := AnalysisReport{}
	if r.state.ReadMemory != nil {
		reviews = append(reviews, r.state.ReadMemory.SourceReviews...)
		report = r.state.ReadMemory.Notes[agent]
	}
	checks := append([]LocalCheckSummary(nil), r.state.LocalChecks...)
	r.mu.Unlock()
	for _, issue := range invalidated {
		result.Issues = append(result.Issues, issue)
	}
	grouped := map[string]*deliveryReviewDocument{}
	ranges := map[string][][2]int64{}
	totals := map[string]int64{}
	adoptedChecks := map[string]bool{}
	for _, id := range sortedAnalysisIDs(entries) {
		e, err := r.analysisLookup(ctx, true)(id)
		if err != nil {
			previous := entries[id]
			key := sourceReviewKey(agent, r.runID, previous.Receipt.Path, previous.Receipt.Version)
			result.Issues = append(result.Issues, DeliveryReviewIssue{ID: "unavailable_" + sourceReviewID(key), Kind: "source_unavailable", Path: previous.Receipt.Path, Version: previous.Receipt.Version, EvidenceIDs: []string{id}, Reason: "本轮先前来源当前已不可用，不能把目录变为空等同于处理完成；需要时重新取证"})
			continue
		}
		key := sourceReviewKey(agent, r.runID, e.Receipt.Path, e.Receipt.Version)
		doc := grouped[key]
		if doc == nil {
			doc = &deliveryReviewDocument{ID: sourceReviewID(key), Path: e.Receipt.Path, Version: e.Receipt.Version, Disposition: "undeclared"}
			grouped[key] = doc
			totals[key] = e.Receipt.TotalBytes
		}
		doc.EvidenceIDs = append(doc.EvidenceIDs, id)
		ranges[key] = append(ranges[key], [2]int64{e.Receipt.ByteStart, e.Receipt.ByteEnd})
	}
	for key, doc := range grouped {
		end := int64(0)
		complete := true
		rr := ranges[key]
		sort.Slice(rr, func(i, j int) bool { return rr[i][0] < rr[j][0] })
		for _, span := range rr {
			if span[0] > end {
				complete = false
			}
			if span[1] > end {
				end = span[1]
			}
		}
		if complete && end == totals[key] {
			doc.Coverage = "complete"
		} else {
			doc.Coverage = "partial"
		}
		for _, review := range reviews {
			if sourceReviewKey(review.AgentID, review.RunID, review.Path, review.Version) != key {
				continue
			}
			valid := true
			for _, fid := range review.FindingIDs {
				found := false
				for _, f := range report.Findings {
					if f.ID == fid && sourceFindingDigest(f) == review.FindingDigests[fid] {
						found = true
						for _, ref := range f.Evidence {
							if _, err := r.analysisLookup(ctx, true)(ref.EvidenceID); err != nil {
								found = false
							}
						}
					}
				}
				valid = valid && found
			}
			for _, eid := range review.EvidenceIDs {
				_, err := r.analysisLookup(ctx, true)(eid)
				valid = valid && err == nil
			}
			if valid {
				doc.Disposition, doc.Reason = review.Disposition, review.Reason
				for _, checkID := range review.CheckIDs {
					adoptedChecks[checkID] = true
				}
			}
		}
		if doc.Disposition == "undeclared" || doc.Disposition == "unverified" {
			reason := doc.Reason
			if reason == "" {
				reason = "来源已交付，尚无文档处置声明；读取完整不代表判断完成"
			}
			result.Issues = append(result.Issues, DeliveryReviewIssue{ID: doc.ID, Kind: "source_" + doc.Disposition, Path: doc.Path, Version: doc.Version, EvidenceIDs: doc.EvidenceIDs, Reason: reason})
		}
		result.Documents = append(result.Documents, *doc)
	}
	sort.Slice(result.Documents, func(i, j int) bool { return result.Documents[i].Path < result.Documents[j].Path })
	for _, read := range report.DeferredReads {
		e, err := r.analysisLookup(ctx, true)(read.EvidenceID)
		if err != nil || e.RunID != r.runID || e.AgentID != agent {
			continue
		}
		kind := "deferred_unresolved"
		reason := read.Reason
		if read.WaitFor != nil {
			kind = "deferred_waiting"
			start := read.WaitFor.ByteStart
			rr := ranges[sourceReviewKey(agent, r.runID, e.Receipt.Path, e.Receipt.Version)]
			sort.Slice(rr, func(i, j int) bool { return rr[i][0] < rr[j][0] })
			for _, span := range rr {
				if span[0] <= start && span[1] > start {
					start = span[1]
				}
			}
			if start >= read.WaitFor.ByteEnd {
				kind = "ready_to_resolve"
				reason = "等待的源字节已成功交付；请形成发现、说明不相关或保留待核实，不能自动当作已处理。原理由：" + reason
			}
		}
		result.Issues = append(result.Issues, DeliveryReviewIssue{ID: "deferred_" + read.EvidenceID, Kind: kind, Path: e.Receipt.Path, Version: e.Receipt.Version, EvidenceIDs: []string{read.EvidenceID}, Reason: reason})
	}
	for _, check := range checks {
		if check.AgentID != agent || check.OriginRunID != r.runID || check.Result == "pass" || adoptedChecks[check.ID] {
			continue
		}
		// 来源不足本身就是待办，不能因为没有成功解析Sources而消失。
		// 只保留本Agent、本运行的检查，不把回执补造成当前原文证据。
		result.Issues = append(result.Issues, DeliveryReviewIssue{ID: check.ID, Kind: "local_check_unresolved", CheckIDs: []string{check.ID}, Reason: "局部核对结果=" + check.Result + "；" + check.Reason})
	}
	if len(result.Documents) > 0 && len(result.Issues) == 0 && len(r.currentDeliveryReviewNotes(ctx).Findings) == 0 {
		result.Issues = append(result.Issues, DeliveryReviewIssue{ID: "no_valid_findings", Kind: "no_valid_findings", Reason: "已有读取来源，但没有引用有效的发现；不相关声明不证明任务已有结论，可说明任务确无相关材料，不强迫编造发现"})
	}
	result.Issues = append(result.Issues, r.currentDeliveryContentIssues(ctx)...)
	sort.Slice(result.Issues, func(i, j int) bool { return result.Issues[i].ID < result.Issues[j].ID })
	return result
}

func (r *runState) deliveryReviewSummary(ctx context.Context) *DeliveryReviewSummary {
	if !r.deliveryReviewEnabled() {
		return nil
	}
	catalog := r.deliveryReviewCatalog(ctx)
	return r.deliveryReviewSummaryWithCatalog(catalog)
}

func (r *runState) deliveryReviewSummaryWithCatalog(catalog deliveryReviewCatalog) *DeliveryReviewSummary {
	summary := &DeliveryReviewSummary{Version: 1, Status: "not_run", Documents: len(catalog.Documents), Unresolved: catalog.Issues}
	r.mu.Lock()
	if s := r.state.DeliveryReview; s != nil {
		summary.Status = s.Status
		summary.AdmissionCount = s.AdmissionCount
		summary.OriginRunID = s.OriginRunID
		summary.TriggerReasons = append([]string(nil), s.TriggerReasons...)
		summary.ErrorClass = s.ErrorClass
		summary.ContentComparisons = append([]DeliveryReviewContentComparison(nil), s.ContentComparisons...)
		if s.OriginRunID != "" && s.OriginRunID != r.runID {
			oldIssues := append([]DeliveryReviewIssue(nil), s.Issues...)
			if s.Summary != nil {
				oldIssues = append(oldIssues, s.Summary.Unresolved...)
			}
			for _, issue := range oldIssues {
				if !strings.HasPrefix(issue.Kind, "history_") {
					issue.Kind = "history_" + issue.Kind
				}
				summary.Unresolved = append(summary.Unresolved, issue)
			}
			summary.Unresolved = append(summary.Unresolved, DeliveryReviewIssue{ID: "history_evidence_required", Kind: "history_evidence_required", Reason: "旧交付处理票据仅作历史线索；本轮未继承读取、处置或折叠资格，文档计数仅包含本轮重新取得的来源。"})
		}
	}
	r.mu.Unlock()
	for _, doc := range catalog.Documents {
		switch doc.Disposition {
		case "addressed":
			summary.Addressed++
		case "not_relevant":
			summary.NotRelevant++
		case "unverified":
			summary.Unverified++
		default:
			summary.Undeclared++
		}
	}
	seen := map[string]bool{}
	unique := summary.Unresolved[:0]
	for _, issue := range summary.Unresolved {
		if !seen[issue.ID] {
			unique = append(unique, issue)
			seen[issue.ID] = true
			if issue.Kind == "ready_to_resolve" {
				summary.ReadyToResolve++
			}
		}
	}
	summary.Unresolved = unique
	return summary
}

func (r *runState) deliveryReviewPrompt(ctx context.Context) string {
	if !r.deliveryReviewEnabled() {
		return ""
	}
	catalog := r.deliveryReviewCatalog(ctx)
	summary := r.deliveryReviewSummaryWithCatalog(catalog)
	const prefix = "\n交付前文档处置目录（均为当前本人已成功交付的来源；处置是模型声明，不等于语义通过；原文覆盖与处置分开）："
	const example = "\n在既有适用工具的_work_notes中可增量提交findings和source_reviews，关联当前evidence_ids/finding_ids/check_ids，disposition用addressed/not_relevant/unverified并说明理由。没有有效增量时省略，不增加登记调用。补页齐备只标ready_to_resolve，不自动processed或折叠。"
	method := "\n当前check_evidence不可用：仅依已有原文静态判断，保留无法核实的前提，不宣称已运行核对。"
	if r.localChecksEnabled() {
		method = "\n当前可用check_evidence：对将采用的数值或明确JSON按file_ref和完整范围核对；检查与写入分批，随后按实际回执修订。pass仅证明指定公式/对象，不证明代码前提或通用语义。"
	}
	focus := ""
	if summary.AdmissionCount > 0 {
		focus = "\n本任务唯一交付前处理机会已准入：聚焦未处置文档、就绪暂缓和核对反例；使用已有材料形成限定结论，未解决的保留待核实。"
	}
	type projection struct {
		CandidateIssues []DeliveryReviewIssue    `json:"candidate_issues,omitempty"`
		Claims          []deliveryReviewClaim    `json:"claims"`
		Checks          []deliveryReviewCheck    `json:"checks"`
		Unverified      []string                 `json:"unverified,omitempty"`
		Documents       []deliveryReviewDocument `json:"documents"`
		Unresolved      []DeliveryReviewIssue    `json:"unresolved"`
		TotalDocuments  int                      `json:"document_count"`
		TotalIssues     int                      `json:"unresolved_count"`
		Omitted         int                      `json:"omitted"`
		OmittedClaims   int                      `json:"omitted_claims"`
		OmittedChecks   int                      `json:"omitted_checks"`
	}
	notes := r.currentDeliveryReviewNotes(ctx)
	view := projection{Claims: deliveryReviewClaims(notes), Checks: r.deliveryReviewChecks(ctx), Unverified: notes.Unverified, Documents: catalog.Documents, Unresolved: summary.Unresolved, TotalDocuments: summary.Documents, TotalIssues: len(summary.Unresolved)}
	// 原候选提示仅说明首次提交的问题；当前确认正文的提示另列于unresolved。
	r.mu.Lock()
	if d := r.state.DeliveryReview; d != nil && d.OriginRunID == r.runID {
		for _, issue := range d.Issues {
			switch issue.Kind {
			case "citation_alias_unresolved", "source_path_difference", "source_locations_absent", "content_review_omitted":
				view.CandidateIssues = append(view.CandidateIssues, issue)
			}
		}
	}
	r.mu.Unlock()
	// 可操作的就绪、失效和待核实项优先；省略不能让已补齐的旧暂缓再被目录淹没。
	view.Unresolved = append([]DeliveryReviewIssue(nil), view.Unresolved...)
	priority := func(kind string) int {
		switch kind {
		case "ready_to_resolve", "source_unavailable":
			return 0
		case "source_unverified", "local_check_unresolved":
			return 1
		case "deferred_unresolved":
			return 2
		case "deferred_waiting":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(view.Unresolved, func(i, j int) bool { return priority(view.Unresolved[i].Kind) < priority(view.Unresolved[j].Kind) })
	all := view
	view.Claims, view.Checks, view.Unverified, view.Unresolved, view.Documents = nil, nil, nil, nil, nil
	view.CandidateIssues = nil
	view.Omitted = len(all.Claims) + len(all.Checks) + len(all.Unverified) + len(all.Unresolved) + len(all.Documents) + len(all.CandidateIssues) + notes.UnverifiedDropped
	view.OmittedClaims, view.OmittedChecks = len(all.Claims), len(all.Checks)
	suffix := example + deliveryReviewClaimInstructions + method + focus
	// 按完整条目尝试放入；单个超大条目不能挤掉后续可容纳的小条目。
	try := func(add, undo func()) {
		view.Omitted--
		add()
		raw, _ := json.Marshal(view)
		if len(prefix)+len(raw)+len(suffix) > 4*1024 {
			undo()
			view.Omitted++
		}
	}
	addCheck := func(check deliveryReviewCheck) {
		try(func() { view.Checks = append(view.Checks, check); view.OmittedChecks-- }, func() { view.Checks = view.Checks[:len(view.Checks)-1]; view.OmittedChecks++ })
	}
	addIssue := func(issue DeliveryReviewIssue) {
		try(func() { view.Unresolved = append(view.Unresolved, issue) }, func() { view.Unresolved = view.Unresolved[:len(view.Unresolved)-1] })
	}
	for _, issue := range all.CandidateIssues {
		try(func() { view.CandidateIssues = append(view.CandidateIssues, issue) }, func() { view.CandidateIssues = view.CandidateIssues[:len(view.CandidateIssues)-1] })
	}
	for _, check := range all.Checks {
		if check.Result != "pass" {
			addCheck(check)
		}
	}
	for _, issue := range all.Unresolved {
		if priority(issue.Kind) < 2 {
			addIssue(issue)
		}
	}
	for _, claim := range all.Claims {
		try(func() { view.Claims = append(view.Claims, claim); view.OmittedClaims-- }, func() { view.Claims = view.Claims[:len(view.Claims)-1]; view.OmittedClaims++ })
	}
	for _, item := range all.Unverified {
		try(func() { view.Unverified = append(view.Unverified, item) }, func() { view.Unverified = view.Unverified[:len(view.Unverified)-1] })
	}
	for _, issue := range all.Unresolved {
		if priority(issue.Kind) >= 2 {
			addIssue(issue)
		}
	}
	for _, check := range all.Checks {
		if check.Result == "pass" {
			addCheck(check)
		}
	}
	for _, doc := range all.Documents {
		try(func() { view.Documents = append(view.Documents, doc) }, func() { view.Documents = view.Documents[:len(view.Documents)-1] })
	}
	raw, _ := json.Marshal(view)
	return prefix + string(raw) + suffix
}
