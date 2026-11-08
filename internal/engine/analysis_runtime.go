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
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// 原文只在本次运行缓存，持久状态只保存不可变材料引用；恢复不自动恢复读取资格。
type analysisRuntime struct {
	sources        map[string]AnalysisEvidence
	delivered      map[string]map[string]bool
	issues         map[string][]string
	parents        map[string]string
	accepted       map[string]map[string]bool
	next           int
	invalidated    map[string]string
	projectedNotes map[string][]string
	notesExposed   map[string]map[string]bool
}

func (r *runState) analysisEnabled() bool { return r.state.Spec.Analysis }

func ensureAnalysisState(s *taskState) *AnalysisState {
	if s.Spec.ReadContextVersion >= 1 && s.Spec.ReadContextVersion <= 4 {
		ensureReadMemory(s)
		return analysisStateOf(*s)
	}
	if s.Analysis == nil {
		s.Analysis = &AnalysisState{Version: 1}
	}
	a := analysisStateOf(*s)
	if a.Evidence == nil {
		a.Evidence = make(map[string]AnalysisEvidence)
	}
	if a.Reports == nil {
		a.Reports = make(map[string]AnalysisReport)
	}
	if a.Processed == nil {
		a.Processed = make(map[string][]string)
	}
	if a.Summaries == nil {
		a.Summaries = make(map[string]map[string]string)
	}
	return a
}

func (r *runState) captureAnalysisRead(ctx context.Context, actionID string, result *schema.ToolResult) error {
	if !r.evidenceEnabled() || result.Read == nil || result.Status != schema.ToolSucceeded {
		return nil
	}
	read := *result.Read
	if int64(len(result.ReadContent)) != read.ByteEnd-read.ByteStart {
		return r.noteStorageError(fmt.Errorf("%w：读取原文字节与结构化范围不一致", errStorage))
	}
	id := ""
	if view := read.SourceView; view != nil {
		origin, ok := r.sourceOrigin(view.ArtifactID)
		if !ok || !r.sourceViewsEnabled() || origin.ID != view.OriginEvidenceID || origin.ActionID != view.OriginActionID {
			return r.noteStorageError(fmt.Errorf("%w：来源视图的原始身份无效", errStorage))
		}
		if err := r.currentSourceOrigin(ctx, origin); err != nil {
			return err
		}
		data, err := r.readEvidenceContent(origin)
		if err != nil {
			return err
		}
		if view.ArtifactStart < 0 || view.ArtifactEnd < view.ArtifactStart || view.ArtifactEnd > int64(len(data)) || read.ByteStart != origin.Receipt.ByteStart+view.ArtifactStart || read.ByteEnd != origin.Receipt.ByteStart+view.ArtifactEnd || !bytes.Equal(data[view.ArtifactStart:view.ArtifactEnd], result.ReadContent) {
			return r.noteStorageError(fmt.Errorf("%w：来源视图字节不匹配", errStorage))
		}
		id = view.ArtifactID
	} else {
		if r.artifact == nil {
			return r.noteStorageError(fmt.Errorf("%w：分析证据存储不可用", errStorage))
		}
		id = newID()
		if err := r.artifact(id, result.ReadContent); err != nil {
			return r.noteStorageError(fmt.Errorf("%w：保存分析原文失败: %w", errStorage, err))
		}
	}
	r.mu.Lock()
	a := ensureAnalysisState(&r.state)
	if r.analysisRuntime.next == 0 {
		r.analysisRuntime.next = len(a.Evidence)
	}
	r.analysisRuntime.next++
	read.EvidenceID, read.ArtifactID = fmt.Sprintf("E%d", r.analysisRuntime.next), id
	entry := AnalysisEvidence{ID: read.EvidenceID, ActionID: actionID, AgentID: read.AgentID, RunID: r.runID, Receipt: read, ArtifactID: id, Source: append([]byte(nil), result.ReadContent...)}
	if r.analysisRuntime.sources == nil {
		r.analysisRuntime.sources = make(map[string]AnalysisEvidence)
	}
	r.analysisRuntime.sources[entry.ID] = entry
	r.mu.Unlock()
	result.Read = &read
	result.Output = fmt.Sprintf("证据: %s（原文材料 %s）\n", read.EvidenceID, id) + result.Output
	if read.SourceView == nil {
		observe.Emit(ctx, "artifact_saved", observe.Payload{ArtifactID: id, Tool: "read_file", ContentBytes: int64(len(result.ReadContent))})
	} else {
		start, end := read.ByteStart, read.ByteEnd
		observe.Emit(ctx, "source_view_created", observe.Payload{ArtifactID: id, OriginEvidenceID: read.SourceView.OriginEvidenceID, ViewEvidenceID: read.EvidenceID, Path: read.Path, BeforeVersion: read.Version, ContentBytes: int64(len(result.ReadContent)), ByteStart: &start, ByteEnd: &end, SourceKind: "current"})
	}
	return nil
}

func (r *runState) rememberAnalysisReadLocked(result schema.ToolResult) {
	if !r.evidenceEnabled() || result.Read == nil {
		return
	}
	entry, ok := r.analysisRuntime.sources[result.Read.EvidenceID]
	if !ok {
		return
	}
	entry.Receipt.FileRef = r.bindReadFileRefLocked(entry)
	result.Read.FileRef = entry.Receipt.FileRef
	r.analysisRuntime.sources[entry.ID] = entry
	entry.Source = nil
	ensureAnalysisState(&r.state).Evidence[entry.ID] = entry
	if (r.sourceViewsEnabled() || r.readFeaturesV2() && r.localChecksEnabled()) && entry.Receipt.SourceView == nil {
		m := ensureReadMemory(&r.state)
		if m.SourceArtifacts == nil {
			m.SourceArtifacts = map[string]string{}
		}
		m.SourceArtifacts[entry.ArtifactID] = entry.ID
	}
}

// lookup 每次重新核对版本；不使用 mtime，也不从历史输出猜测读取内容。
func (r *runState) analysisLookup(ctx context.Context, requireDelivered bool) func(string) (AnalysisEvidence, error) {
	return func(id string) (AnalysisEvidence, error) {
		if authorized, _ := ctx.Value(qualityPublicationKey{}).(bool); authorized && r.qualityEnabled() {
			return r.qualityTicketEvidence(ctx, id)
		}
		identity := observe.Identity(ctx)
		r.mu.Lock()
		entry, ok := r.analysisRuntime.sources[id]
		invalid := r.analysisRuntime.invalidated[id] != ""
		delivered := r.analysisRuntime.delivered[identity.AgentID][id]
		// 父任务可复用已由子任务读取并提交的证据，兄弟任务不能互相借用。
		if identity.ParentAgentID == "" && entry.AgentID != identity.AgentID {
			delivered = r.analysisRuntime.delivered[entry.AgentID][id] && r.analysisRuntime.parents[entry.AgentID] == identity.AgentID
		}
		r.mu.Unlock()
		if !ok || entry.RunID != r.runID || invalid {
			return entry, errors.New("证据不属于本次运行，须重新读取")
		}
		if entry.AgentID != identity.AgentID && identity.ParentAgentID != "" {
			return entry, errors.New("证据不属于当前子任务")
		}
		if !filepath.IsLocal(entry.Receipt.Path) || !insideParentPaths(entry.Receipt.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
			return entry, errors.New("证据路径超出权限")
		}
		if requireDelivered && !delivered {
			return entry, errors.New("原文尚未完整进入成功请求")
		}
		version, err := tools.FileVersion(ctx, entry.Receipt.Path)
		if err != nil || version != entry.Receipt.Version {
			if r.readMemoryEnabled() {
				if saveErr := r.record("read_invalidated", func(*taskState) error {
					r.removeReadVersionLocked(entry.Receipt.Path, entry.Receipt.Version)
					r.invalidateMemoryLocked(entry.Receipt.Path, "source_version_changed", false, entry.Receipt.Version)
					return nil
				}); saveErr != nil {
					return entry, saveErr
				}
			}
			return entry, errors.New("证据版本已失效")
		}
		r.mu.Lock()
		invalid = r.analysisRuntime.invalidated[id] != ""
		r.mu.Unlock()
		if invalid {
			return entry, errors.New("读取资格已被副作用撤销")
		}
		return entry, nil
	}
}

func (r *runState) acceptAnalysis(ctx context.Context, text string, final bool) (*AnalysisReport, error) {
	report, err := ParseAnalysisReport(text)
	if err != nil {
		if final {
			return nil, err
		}
		r.analysisIssue(ctx, "中间分析笔记格式无效，相关正文继续保留")
		return nil, nil
	}
	if !r.deliveryReviewEnabled() {
		for i := range report.DeferredReads {
			report.DeferredReads[i].WaitFor = nil
		}
	} else if err := r.validateDeliveryDeferredUpdates(ctx, report); err != nil {
		r.analysisIssue(ctx, "暂缓来源范围未接受："+err.Error())
		return nil, nil
	}
	newProcessed := append([]string(nil), report.ProcessedReads...)
	newDeferred := make(map[string]bool)
	for _, read := range report.DeferredReads {
		newDeferred[read.EvidenceID] = true
	}
	if final {
		if old := r.analysisReport(ctx); old != nil {
			report.DeferredReads = mergeDeferredReads(old.DeferredReads, report.DeferredReads)
		}
	}
	if !final {
		if old := r.analysisReport(ctx); old != nil {
			report = mergeAnalysisReports(*old, report)
		}
		if err := checkAnalysisStructure(report); err != nil {
			r.analysisIssue(ctx, "分析笔记合并超过上限，保留原笔记与未处理正文："+err.Error())
			return nil, nil
		}
	}
	checked, checkErr := ValidateAnalysisReport(report, r.analysisLookup(ctx, true))
	filtered := checked.Findings[:0]
	scopeFailed := false
	for _, finding := range checked.Findings {
		complete := true
		if finding.ScopeRequirement == "file" {
			if len(finding.Evidence) == 0 {
				complete = false
			}
			for _, ref := range finding.Evidence {
				coverage := r.coverageForPaths(ctx, []string{ref.Path})
				complete = complete && coverage.Status == "complete"
			}
		}
		if finding.ScopeRequirement == "manifest" {
			coverage := r.readingCoverage(ctx)
			complete = coverage != nil && coverage.Status == "complete" && finding.ScopeID != "" && finding.ScopeID == coverage.ScopeID && finding.ScopeRevision == coverage.ScopeRevision
		}
		if complete {
			filtered = append(filtered, finding)
		} else {
			scopeFailed = true
			appendAnalysisUnverified(&checked, finding.ID+"：所需文件或清单尚有读取缺口，不能用作整个范围的确定结论。 "+finding.Statement)
		}
	}
	checked.Findings = filtered
	if scopeFailed {
		checked, checkErr = ValidateAnalysisReport(checked, r.analysisLookup(ctx, true))
	}
	identity := observe.Identity(ctx)
	// 暂缓只属于提交者自己本轮真正取得的源材料。有效的新处理声明才解除旧暂缓。
	deferred := checked.DeferredReads[:0]
	for _, read := range checked.DeferredReads {
		source, err := r.analysisLookup(ctx, true)(read.EvidenceID)
		if err != nil || source.AgentID != identity.AgentID {
			continue
		}
		if containsString(newProcessed, read.EvidenceID) && containsString(checked.ProcessedReads, read.EvidenceID) && !newDeferred[read.EvidenceID] {
			continue
		}
		deferred = append(deferred, read)
	}
	checked.DeferredReads = deferred
	deferredIDs := make(map[string]bool)
	for _, read := range deferred {
		deferredIDs[read.EvidenceID] = true
	}
	processed := checked.ProcessedReads[:0]
	for _, id := range checked.ProcessedReads {
		if !deferredIDs[id] {
			processed = append(processed, id)
		}
	}
	checked.ProcessedReads = processed
	var newlyProcessed []string
	if err := r.record("analysis_updated", func(s *taskState) error {
		a := ensureAnalysisState(s)
		a.Reports[identity.AgentID] = checked
		if a.Summaries[identity.AgentID] == nil {
			a.Summaries[identity.AgentID] = make(map[string]string)
		}
		retained := a.Processed[identity.AgentID][:0]
		for _, id := range a.Processed[identity.AgentID] {
			if !deferredIDs[id] {
				retained = append(retained, id)
			}
		}
		a.Processed[identity.AgentID] = retained
		for id, summary := range checked.Summaries {
			if !r.analysisRuntime.delivered[identity.AgentID][id] || strings.TrimSpace(summary) == "" {
				continue
			}
			if _, ok := r.analysisRuntime.sources[id]; !ok {
				continue
			}
			a.Summaries[identity.AgentID][id] = summary
		}
		for _, id := range checked.ProcessedReads {
			if !r.analysisRuntime.delivered[identity.AgentID][id] {
				continue
			}
			has := a.Summaries[identity.AgentID][id] != ""
			for _, f := range checked.Findings {
				for _, ref := range f.Evidence {
					if ref.EvidenceID == id {
						has = true
					}
				}
			}
			if has && !containsString(a.Processed[identity.AgentID], id) {
				a.Processed[identity.AgentID] = append(a.Processed[identity.AgentID], id)
				newlyProcessed = append(newlyProcessed, id)
			}
		}
		return nil
	}); err != nil {
		return &checked, err
	}
	r.observeSourceViewLifecycle(ctx, "source_view_processed", newlyProcessed)
	if final && checkErr != nil {
		return &checked, checkErr
	}
	r.mu.Lock()
	if r.analysisRuntime.accepted == nil {
		r.analysisRuntime.accepted = make(map[string]map[string]bool)
	}
	if r.analysisRuntime.accepted[identity.AgentID] == nil {
		r.analysisRuntime.accepted[identity.AgentID] = make(map[string]bool)
	}
	r.analysisRuntime.accepted[identity.AgentID][analysisMessageKey(text)] = true
	r.mu.Unlock()
	return &checked, nil
}

func analysisMessageKey(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (r *runState) analysisIssue(ctx context.Context, issue string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.analysisRuntime.issues == nil {
		r.analysisRuntime.issues = make(map[string][]string)
	}
	id := observe.Identity(ctx).AgentID
	r.analysisRuntime.issues[id] = append(r.analysisRuntime.issues[id], issue)
	if len(r.analysisRuntime.issues[id]) > 4 {
		r.analysisRuntime.issues[id] = r.analysisRuntime.issues[id][len(r.analysisRuntime.issues[id])-4:]
	}
}

func (r *runState) markAnalysisDelivered(ctx context.Context, ids []string) {
	if !r.evidenceEnabled() {
		return
	}
	r.mu.Lock()
	agent := observe.Identity(ctx).AgentID
	if r.analysisRuntime.delivered == nil {
		r.analysisRuntime.delivered = make(map[string]map[string]bool)
	}
	if r.analysisRuntime.delivered[agent] == nil {
		r.analysisRuntime.delivered[agent] = make(map[string]bool)
	}
	for _, id := range ids {
		r.analysisRuntime.delivered[agent][id] = true
	}
	r.mu.Unlock()
	r.observeSourceViewLifecycle(ctx, "source_view_delivered", ids)
}

func (r *runState) analysisReport(ctx context.Context) *AnalysisReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisStateOf(r.state) == nil {
		return nil
	}
	report, ok := analysisStateOf(r.state).Reports[observe.Identity(ctx).AgentID]
	if !ok {
		return nil
	}
	data, _ := json.Marshal(report)
	var copy AnalysisReport
	_ = json.Unmarshal(data, &copy)
	return &copy
}

func validateAnalysisState(s taskState) error {
	if s.Analysis == nil {
		return nil
	}
	a := analysisStateOf(s)
	if !s.Spec.Analysis || s.Spec.Mode != tools.ModeReadOnly || s.Spec.Kind != TaskAnswer || a.Version != 1 {
		return errors.New("分析状态版本或任务模式无效")
	}
	if err := ValidateAnalysisState(a); err != nil {
		return err
	}
	if err := validateDeferredReadState(a); err != nil {
		return err
	}
	for id, e := range a.Evidence {
		if id == "" || id != e.ID || e.ActionID == "" || e.RunID == "" || e.ArtifactID == "" || !filepath.IsLocal(e.Receipt.Path) || e.Receipt.Version == "" || e.Receipt.ByteStart < 0 || e.Receipt.ByteEnd < e.Receipt.ByteStart || e.Receipt.ByteEnd > e.Receipt.TotalBytes {
			return fmt.Errorf("分析证据 %q 无效", id)
		}
	}
	for _, ids := range a.Processed {
		for _, id := range ids {
			if _, ok := a.Evidence[id]; !ok {
				return fmt.Errorf("分析处理范围引用未知证据 %s", id)
			}
		}
	}
	return nil
}

func sortedAnalysisIDs(entries map[string]AnalysisEvidence) []string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *runState) recordedAnalysisSummary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisStateOf(r.state) == nil {
		return ""
	}
	for agent, report := range analysisStateOf(r.state).Reports {
		if parent, known := r.analysisRuntime.parents[agent]; known && parent == "" {
			var out strings.Builder
			out.WriteString("\n动作当时的分析笔记（历史材料，未重新核对当前版本）：\n" + RenderAnalysis(report))
			var ids []string
			for id := range analysisStateOf(r.state).Summaries[agent] {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				out.WriteString("\n已记录文档摘要（模型声明已处理，不证明无遗漏）：")
			}
			for _, id := range ids {
				out.WriteString("\n" + id + "：" + analysisStateOf(r.state).Summaries[agent][id])
				if out.Len() > 12000 {
					out.WriteString("\n（摘要节选，完整内容保存在分析状态中。）")
					break
				}
			}
			return out.String()
		}
	}
	if last := r.state.LastResult; last != nil && last.Analysis != nil {
		return "\n历史运行的分析笔记（来源运行 " + last.RunID + "；未重新核对当前文件，不可作为本次有效引用）：\n" + RenderAnalysis(*last.Analysis)
	}
	return ""
}
