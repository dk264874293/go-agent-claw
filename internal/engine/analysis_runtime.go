package engine

import (
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
	sources   map[string]AnalysisEvidence
	delivered map[string]map[string]bool
	issues    map[string][]string
	parents   map[string]string
	accepted  map[string]map[string]bool
	next      int
}

func (r *runState) analysisEnabled() bool { return r.state.Spec.Analysis }

func ensureAnalysisState(s *taskState) *AnalysisState {
	if s.Analysis == nil {
		s.Analysis = &AnalysisState{Version: 1}
	}
	a := s.Analysis
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
	if !r.analysisEnabled() || result.Read == nil || result.Status != schema.ToolSucceeded {
		return nil
	}
	read := *result.Read
	if int64(len(result.ReadContent)) != read.ByteEnd-read.ByteStart {
		return r.noteStorageError(fmt.Errorf("%w：读取原文字节与结构化范围不一致", errStorage))
	}
	if r.artifact == nil {
		return r.noteStorageError(fmt.Errorf("%w：分析证据存储不可用", errStorage))
	}
	id := newID()
	if err := r.artifact(id, result.ReadContent); err != nil {
		return r.noteStorageError(fmt.Errorf("%w：保存分析原文失败: %w", errStorage, err))
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
	observe.Emit(ctx, "artifact_saved", observe.Payload{ArtifactID: id, Tool: "read_file", ContentBytes: int64(len(result.ReadContent))})
	return nil
}

func (r *runState) rememberAnalysisReadLocked(result schema.ToolResult) {
	if !r.state.Spec.Analysis || result.Read == nil {
		return
	}
	entry, ok := r.analysisRuntime.sources[result.Read.EvidenceID]
	if !ok {
		return
	}
	entry.Source = nil
	ensureAnalysisState(&r.state).Evidence[entry.ID] = entry
}

// lookup 每次重新核对版本；不使用 mtime，也不从历史输出猜测读取内容。
func (r *runState) analysisLookup(ctx context.Context, requireDelivered bool) func(string) (AnalysisEvidence, error) {
	return func(id string) (AnalysisEvidence, error) {
		identity := observe.Identity(ctx)
		r.mu.Lock()
		entry, ok := r.analysisRuntime.sources[id]
		delivered := r.analysisRuntime.delivered[identity.AgentID][id]
		// 父任务可复用已由子任务读取并提交的证据，兄弟任务不能互相借用。
		if identity.ParentAgentID == "" && entry.AgentID != identity.AgentID {
			delivered = r.analysisRuntime.delivered[entry.AgentID][id] && r.analysisRuntime.parents[entry.AgentID] == identity.AgentID
		}
		r.mu.Unlock()
		if !ok || entry.RunID != r.runID {
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
			return entry, errors.New("证据版本已失效")
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
			complete = r.readingCoverage(ctx).Status == "complete"
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
	if err := r.record("analysis_updated", func(s *taskState) error {
		a := ensureAnalysisState(s)
		a.Reports[identity.AgentID] = checked
		if a.Summaries[identity.AgentID] == nil {
			a.Summaries[identity.AgentID] = make(map[string]string)
		}
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
			}
		}
		return nil
	}); err != nil {
		return &checked, err
	}
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
	if !r.analysisEnabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
}

func (r *runState) analysisReport(ctx context.Context) *AnalysisReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Analysis == nil {
		return nil
	}
	report, ok := r.state.Analysis.Reports[observe.Identity(ctx).AgentID]
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
	a := s.Analysis
	if !s.Spec.Analysis || s.Spec.Mode != tools.ModeReadOnly || s.Spec.Kind != TaskAnswer || a.Version != 1 {
		return errors.New("分析状态版本或任务模式无效")
	}
	if err := ValidateAnalysisState(a); err != nil {
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
	if r.state.Analysis == nil {
		return ""
	}
	for agent, report := range r.state.Analysis.Reports {
		if parent, known := r.analysisRuntime.parents[agent]; known && parent == "" {
			var out strings.Builder
			out.WriteString("\n动作当时的分析笔记（历史材料，未重新核对当前版本）：\n" + RenderAnalysis(report))
			var ids []string
			for id := range r.state.Analysis.Summaries[agent] {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				out.WriteString("\n已记录文档摘要（模型声明已处理，不证明无遗漏）：")
			}
			for _, id := range ids {
				out.WriteString("\n" + id + "：" + r.state.Analysis.Summaries[agent][id])
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
