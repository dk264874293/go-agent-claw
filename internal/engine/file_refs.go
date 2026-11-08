package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

// ReadFileRef只是本次运行的文件版本句柄，不是读取或核对授权。
type ReadFileRef struct {
	ID               string `json:"id"`
	RunID            string `json:"run_id"`
	AgentID          string `json:"agent_id"`
	Path             string `json:"path"`
	Version          string `json:"version"`
	AnchorEvidenceID string `json:"anchor_evidence_id"`
	AnchorActionID   string `json:"anchor_action_id"`
	Invalidated      bool   `json:"invalidated,omitempty"`
}

// 调用方持有r.mu，句柄与成功action_result一同提交，不在请求投影时制造状态。
func (r *runState) bindReadFileRefLocked(entry AnalysisEvidence) string {
	if !r.readFeaturesV2() || !r.localChecksEnabled() {
		return ""
	}
	m := ensureReadMemory(&r.state)
	if m.FileRefs == nil {
		m.FileRefs = map[string]ReadFileRef{}
	}
	for id, ref := range m.FileRefs {
		if ref.RunID == r.runID && ref.AgentID == entry.AgentID && ref.Path == entry.Receipt.Path && ref.Version == entry.Receipt.Version && !ref.Invalidated && r.analysisRuntime.invalidated[ref.AnchorEvidenceID] == "" {
			return id
		}
	}
	maxID := 0
	for key := range m.FileRefs {
		if strings.HasPrefix(key, "F") {
			if n, err := strconv.Atoi(strings.TrimPrefix(key, "F")); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	id := fmt.Sprintf("F%d", maxID+1)
	m.FileRefs[id] = ReadFileRef{ID: id, RunID: r.runID, AgentID: entry.AgentID, Path: entry.Receipt.Path, Version: entry.Receipt.Version, AnchorEvidenceID: entry.ID, AnchorActionID: entry.ActionID}
	return id
}

func validateReadFileRefs(s taskState) error {
	if s.ReadMemory == nil || len(s.ReadMemory.FileRefs) == 0 {
		return nil
	}
	if s.Spec.ReadContextVersion != 4 || s.Spec.ReadFeatures == nil || !s.Spec.ReadFeatures.LocalChecks {
		return errors.New("文件句柄只属于显式策略4")
	}
	for id, ref := range s.ReadMemory.FileRefs {
		n, parseErr := strconv.Atoi(strings.TrimPrefix(id, "F"))
		if parseErr != nil || n < 1 || fmt.Sprintf("F%d", n) != id {
			return errors.New("文件句柄编号无效")
		}
		e, ok := s.ReadMemory.Evidence[ref.AnchorEvidenceID]
		if !ok || id == "" || id != ref.ID || ref.RunID == "" || ref.AgentID == "" || !filepath.IsLocal(ref.Path) || ref.Version == "" || ref.AnchorActionID != e.ActionID || ref.RunID != e.RunID || ref.AgentID != e.AgentID || ref.Path != e.Receipt.Path || ref.Version != e.Receipt.Version {
			return errors.New("文件句柄的原始读取身份无效")
		}
	}
	return nil
}

func (r *runState) fileRefForEvidence(ctx context.Context, e AnalysisEvidence) string {
	if !r.readFeaturesV2() || !r.localChecksEnabled() {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.state.ReadMemory
	if m == nil {
		return ""
	}
	// 每个Agent自己的文件句柄优先，父任务仅沿既有交接边界看到子读取的句柄。
	ids := make([]string, 0, len(m.FileRefs))
	for id := range m.FileRefs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ref := m.FileRefs[id]
		if ref.RunID == r.runID && ref.AgentID == e.AgentID && ref.Path == e.Receipt.Path && ref.Version == e.Receipt.Version && !ref.Invalidated && r.analysisRuntime.invalidated[ref.AnchorEvidenceID] == "" {
			return id
		}
	}
	return ""
}

func (r *runState) resolveReadFileRef(ctx context.Context, id string) (ReadFileRef, error) {
	r.mu.Lock()
	m := r.state.ReadMemory
	var ref ReadFileRef
	var ok bool
	if m != nil {
		ref, ok = m.FileRefs[id]
	}
	r.mu.Unlock()
	if !ok {
		return ref, sourceResolutionError("source_ref_unknown", "文件句柄不存在", nil)
	}
	if ref.RunID != r.runID || ref.Invalidated {
		return ref, sourceResolutionError("source_ref_stale", "文件句柄属于历史或已失效，须重新读取工作区", nil)
	}
	anchor, err := r.analysisLookup(ctx, false)(ref.AnchorEvidenceID)
	if err != nil {
		return ref, sourceResolutionError("source_ref_stale", err.Error(), nil)
	}
	who := observe.Identity(ctx)
	if ref.AgentID != who.AgentID {
		r.mu.Lock()
		parent := who.ParentAgentID == "" && r.analysisRuntime.parents[ref.AgentID] == who.AgentID && r.analysisRuntime.delivered[ref.AgentID][ref.AnchorEvidenceID]
		r.mu.Unlock()
		if !parent {
			return ref, sourceResolutionError("source_ref_forbidden", "文件句柄不属于当前Agent或已交接的直属子任务", nil)
		}
	}
	if anchor.ActionID != ref.AnchorActionID || anchor.Receipt.Path != ref.Path || anchor.Receipt.Version != ref.Version {
		return ref, r.noteStorageError(fmt.Errorf("%w：文件句柄原始身份不匹配", errStorage))
	}
	return ref, nil
}
