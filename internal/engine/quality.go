package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// QualitySpec 只启用质量门禁，不扩大权限、共享预算或子任务能力。
type QualitySpec struct {
	Version int `json:"version"`
}

// QualityState 的阶段准入在网络请求之前持久化，恢复不得重发未确认调用。
type QualityState struct {
	AgentID              string                  `json:"agent_id,omitempty"`
	EvidenceHashes       map[string]string       `json:"evidence_hashes,omitempty"`
	MaterialHashes       map[string]string       `json:"material_hashes,omitempty"`
	Version              int                     `json:"version"`
	DraftID              string                  `json:"draft_id,omitempty"`
	DraftSHA256          string                  `json:"draft_sha256,omitempty"`
	OriginRunID          string                  `json:"origin_run_id,omitempty"`
	Status               string                  `json:"status"`
	DraftArtifactID      string                  `json:"draft_artifact_id,omitempty"`
	FinalCheckArtifactID string                  `json:"final_check_artifact_id,omitempty"`
	CheckedArtifactID    string                  `json:"checked_artifact_id,omitempty"`
	ReviewArtifactID     string                  `json:"review_artifact_id,omitempty"`
	RevisionArtifactID   string                  `json:"revision_artifact_id,omitempty"`
	FrozenArtifactID     string                  `json:"frozen_artifact_id,omitempty"`
	FrozenSHA256         string                  `json:"frozen_sha256,omitempty"`
	SourceVersions       map[string]string       `json:"source_versions,omitempty"`
	ScopeDependencies    []ReportScopeDependency `json:"scope_dependencies,omitempty"`
	CheckResult          string                  `json:"check_result"`
	ReviewStatus         string                  `json:"review_status"`
	RevisionStatus       string                  `json:"revision_status"`
	ReviewRunID          string                  `json:"review_run_id,omitempty"`
	RevisionRunID        string                  `json:"revision_run_id,omitempty"`
	ErrorCode            string                  `json:"error_code,omitempty"`
	Unresolved           []string                `json:"unresolved,omitempty"`
	// 完整检查、复核和材料放受限任务证据，状态只保存身份与摘要。
}

type QualityDelivery struct {
	Version        int      `json:"version"`
	DraftID        string   `json:"draft_id,omitempty"`
	Status         string   `json:"status"`
	CheckResult    string   `json:"check_result"`
	ReviewStatus   string   `json:"review_status"`
	RevisionStatus string   `json:"revision_status"`
	Unresolved     []string `json:"unresolved,omitempty"`
	EvidenceIDs    []string `json:"evidence_ids,omitempty"`
}

func NormalizeQualitySpec(spec *TaskSpec) error {
	if spec.Quality == nil {
		return nil
	}
	copy := *spec.Quality
	if copy.Version == 0 {
		copy.Version = 1
	}
	spec.Quality = &copy
	if spec.Analysis && spec.Delegation == "" {
		spec.Delegation = "off"
	}
	return ValidateQualitySpec(*spec)
}

func ValidateQualitySpec(spec TaskSpec) error {
	if spec.Quality == nil {
		return nil
	}
	if spec.Quality.Version != 1 {
		return errors.New("质量策略仅支持version=1")
	}
	if (spec.Analysis == (spec.Report != nil)) || spec.Plan || spec.VerifyCommand != "" || (spec.Delegation != "" && spec.Delegation != "off") {
		return errors.New("质量模式必须组合分析或报告，不能组合规划、验收或子任务委派")
	}
	if spec.Analysis && (spec.Mode != tools.ModeReadOnly || spec.Kind != "" && spec.Kind != TaskAnswer) {
		return errors.New("质量分析仅支持readonly + answer")
	}
	if spec.Report != nil && (spec.Mode != tools.ModeWorkspace || spec.Kind != "" && spec.Kind != TaskChange) {
		return errors.New("质量报告仅支持workspace + change")
	}
	return nil
}

// ValidateTaskStateVersion 是恢复和只读Inspection共用的失败关闭边界。
func ValidateTaskStateVersion(s taskState) error {
	if err := validateDeliveryReviewState(s); err != nil {
		return err
	}
	if s.Version == 3 {
		return nil
	}
	if s.Version == 1 {
		if s.Spec.Quality != nil || s.Quality != nil {
			return errors.New("版本1任务不能携带质量配置或质量状态")
		}
		return nil
	}
	if s.Version != 2 || s.Spec.Quality == nil {
		return fmt.Errorf("不支持的任务版本或缺少质量配置：%d", s.Version)
	}
	if err := ValidateQualitySpec(s.Spec); err != nil {
		return err
	}
	if s.Quality == nil || s.Quality.Version != 1 {
		return errors.New("质量任务缺少有效质量状态")
	}
	q := s.Quality
	switch q.Status {
	case "exploring", "checking", "checked", "ready", "blocked", "published", "stale":
	default:
		return errors.New("质量阶段无效")
	}
	for _, stage := range []string{q.ReviewStatus, q.RevisionStatus} {
		switch stage {
		case "not_run", "admitted", "completed", "invalid", "failed", "skipped":
		default:
			return errors.New("质量模型阶段状态无效")
		}
	}
	switch q.CheckResult {
	case "not_run", "pass", "partial", "fail", "error":
	default:
		return errors.New("质量检查结果无效")
	}
	if q.DraftID != "" {
		if !analysisIDValid(q.DraftID) || !validReportVersion(q.DraftSHA256) || !analysisIDValid(q.DraftArtifactID) || !analysisIDValid(q.OriginRunID) {
			return errors.New("质量草稿身份无效")
		}
	}
	for _, id := range []string{q.CheckedArtifactID, q.FinalCheckArtifactID, q.ReviewArtifactID, q.RevisionArtifactID, q.FrozenArtifactID} {
		if id != "" && !analysisIDValid(id) {
			return errors.New("质量材料身份无效")
		}
	}
	if q.FrozenArtifactID != "" && !validReportVersion(q.FrozenSHA256) {
		return errors.New("质量冻结材料摘要无效")
	}
	for path, version := range q.SourceVersions {
		if !validQualityPath(path) || !validReportVersion(version) {
			return errors.New("质量来源路径或版本无效")
		}
	}
	if len(q.MaterialHashes) > 128 || len(q.EvidenceHashes) > 4096 {
		return errors.New("质量材料索引超过上限")
	}
	for id, hash := range q.MaterialHashes {
		if !analysisIDValid(id) || !validReportVersion(hash) {
			return errors.New("质量材料索引无效")
		}
	}
	for id, hash := range q.EvidenceHashes {
		if !analysisIDValid(id) || !validReportVersion(hash) {
			return errors.New("质量证据索引无效")
		}
	}
	if q.Status == "exploring" && (q.DraftID != "" || q.ReviewStatus != "not_run" || q.RevisionStatus != "not_run" || q.CheckedArtifactID != "") {
		return errors.New("探索阶段不能携带已登记的草稿或阶段准入")
	}
	if q.Status != "exploring" && q.Status != "blocked" && q.DraftID == "" {
		return errors.New("质量阶段缺少草稿票据")
	}
	if (q.Status == "checked" || q.Status == "ready" || q.Status == "published" || q.Status == "stale") && q.CheckedArtifactID == "" {
		return errors.New("质量阶段缺少核验完成材料")
	}
	if q.DraftID != "" && !analysisIDValid(q.AgentID) {
		return errors.New("质量票据缺少根Agent身份")
	}
	if q.ReviewStatus == "completed" && q.ReviewArtifactID == "" {
		return errors.New("复核完成但缺少材料")
	}
	if q.RevisionStatus == "completed" && q.RevisionArtifactID == "" {
		return errors.New("修订完成但缺少材料")
	}
	if (q.Status == "ready" || q.Status == "published" || q.Status == "stale") && (q.FinalCheckArtifactID == "" || q.RevisionStatus != "completed") {
		return errors.New("质量发布缺少复验或修订材料")
	}
	if (q.Status == "published" || q.Status == "stale") && (q.FrozenArtifactID == "" || q.ReviewStatus != "completed" && q.ReviewStatus != "skipped" && q.ReviewStatus != "invalid") {
		return errors.New("质量发布缺少冻结材料或已确认复核状态")
	}
	for _, id := range []string{q.DraftArtifactID, q.CheckedArtifactID, q.FinalCheckArtifactID, q.ReviewArtifactID, q.RevisionArtifactID, q.FrozenArtifactID} {
		if id != "" && !validReportVersion(q.MaterialHashes[id]) {
			return errors.New("质量完成回执缺少材料摘要索引")
		}
	}
	if len(q.Unresolved) > 192 {
		return errors.New("质量未解决项超过上限")
	}
	return nil
}

func validQualityPath(path string) bool {
	return path != "" && !strings.HasPrefix(path, "/") && !strings.Contains(path, "\\") && !strings.ContainsRune(path, 0) && path != ".." && !strings.HasPrefix(path, "../") && !strings.Contains(path, "/../")
}

func (r *runState) qualityEnabled() bool { return r.state.Spec.Quality != nil }
func (r *runState) qualityDelivery() *QualityDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.state.Quality
	if q == nil {
		return nil
	}
	ids := make(map[string]bool, len(q.EvidenceHashes))
	for id := range q.EvidenceHashes {
		ids[id] = true
	}
	return &QualityDelivery{Version: 1, DraftID: q.DraftID, Status: q.Status, CheckResult: q.CheckResult, ReviewStatus: q.ReviewStatus, RevisionStatus: q.RevisionStatus, Unresolved: append([]string(nil), q.Unresolved...), EvidenceIDs: sortedQualityIDs(ids)}
}

func qualityError(code, reason string) error { return reportError{code, reason} }

func (r *runState) saveQualityMaterial(raw []byte) (string, error) {
	if len(raw) > 16*1024*1024 {
		return "", qualityError("quality_contract_invalid", "质量材料超过16MiB安全上限")
	}
	if r.artifact == nil {
		return "", r.noteStorageError(fmt.Errorf("%w：质量材料存储不可用", errStorage))
	}
	id := newID()
	if err := r.artifact(id, raw); err != nil {
		return "", r.noteStorageError(fmt.Errorf("%w：保存质量材料失败: %w", errStorage, err))
	}
	if err := r.record("quality_material_saved", func(s *taskState) error {
		if s.Quality == nil {
			return qualityError("quality_contract_invalid", "没有质量存储上下文")
		}
		if s.Quality.MaterialHashes == nil {
			s.Quality.MaterialHashes = map[string]string{}
		}
		s.Quality.MaterialHashes[id] = fmt.Sprintf("%x", sha256Sum(raw))
		return nil
	}); err != nil {
		return "", err
	}
	return id, nil
}
func (r *runState) saveQualityJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return r.saveQualityMaterial(raw)
}

// stage准入必须与扣轮次及模型额度处于同一事件，不复用会拆成两个事件的普通reserve。
func (r *runState) reserveQualityStage(ctx context.Context, stage string) error {
	return r.record(stage+"_admitted", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Quality == nil || s.Quality.DraftID == "" {
			return qualityError("quality_contract_invalid", "没有登记的质量草稿")
		}
		status := &s.Quality.ReviewStatus
		run := &s.Quality.ReviewRunID
		if stage == "quality_revision" {
			status = &s.Quality.RevisionStatus
			run = &s.Quality.RevisionRunID
		}
		if *status != "not_run" {
			return qualityError("quality_outcome_unknown", "本阶段已准入，不能重复请求")
		}
		if s.Usage.Turns >= s.Limits.MaxTurns {
			return budgetLimit{"turn_limit", "主循环轮数"}
		}
		if s.Usage.ModelCalls >= s.Limits.MaxModelCalls {
			return budgetLimit{"model_limit", "模型调用次数"}
		}
		if r.budgetSnapshotLocked().RemainingMS <= 0 {
			return budgetLimit{"deadline", "累计时间"}
		}
		s.Usage.Turns++
		s.Usage.ModelCalls++
		*status = "admitted"
		*run = r.runID
		return nil
	})
}

func (r *runState) readQualityMaterial(id string) ([]byte, error) {
	r.mu.Lock()
	expected := ""
	if r.state.Quality != nil {
		expected = r.state.Quality.MaterialHashes[id]
	}
	r.mu.Unlock()
	if !validReportVersion(expected) {
		return nil, r.noteStorageError(fmt.Errorf("%w：质量材料没有摘要回执", errStorage))
	}
	var raw []byte
	for offset := 0; ; {
		if r.readArtifact == nil && r.readArtifactPage == nil {
			return nil, r.noteStorageError(fmt.Errorf("%w：质量材料读取不可用", errStorage))
		}
		var part []byte
		var err error
		if r.readArtifactPage != nil {
			page, e := r.readArtifactPage(id, offset, 4000)
			part, err = page.Data, e
		} else {
			part, err = r.readArtifact(id, offset, 4000)
		}
		if err != nil {
			return nil, r.noteStorageError(fmt.Errorf("%w：质量材料读取失败: %w", errStorage, err))
		}
		if len(part) == 0 {
			break
		}
		raw = append(raw, part...)
		offset += len(part)
		if len(raw) > 16*1024*1024 {
			return nil, r.noteStorageError(fmt.Errorf("%w：质量材料大小与保存契约不符", errStorage))
		}
	}
	if fmt.Sprintf("%x", sha256Sum(raw)) != expected {
		return nil, r.noteStorageError(fmt.Errorf("%w：质量材料摘要不符", errStorage))
	}
	return raw, nil
}

// 同一票据、阶段准入和来源摘要不可在后续日志中回退或替换。
func validateQualityTransition(previous, next taskState) error {
	if previous.Spec.Quality == nil || next.Spec.Quality == nil {
		return nil
	}
	a, b := previous.Quality, next.Quality
	if a == nil || b == nil {
		return errors.New("质量阶段缺失")
	}
	if qualityHash(previous.Spec) != qualityHash(next.Spec) {
		return errors.New("持久质量任务不能更改原策略或范围")
	}
	if a.DraftID != "" && (a.DraftID != b.DraftID || a.DraftSHA256 != b.DraftSHA256 || a.DraftArtifactID != b.DraftArtifactID || a.OriginRunID != b.OriginRunID || a.AgentID != b.AgentID) {
		return errors.New("质量草稿票据被替换或清除")
	}
	allowed := func(old, new string) bool {
		if old == new {
			return true
		}
		switch old {
		case "not_run":
			return new == "admitted" || new == "skipped"
		case "admitted":
			return new == "completed" || new == "invalid" || new == "failed"
		}
		return false
	}
	if !allowed(a.ReviewStatus, b.ReviewStatus) || !allowed(a.RevisionStatus, b.RevisionStatus) {
		return errors.New("质量阶段准入不能回退或重复")
	}
	if a.Status != "exploring" && b.Status == "exploring" {
		return errors.New("质量任务不能回到探索阶段")
	}
	for id, hash := range a.MaterialHashes {
		if b.MaterialHashes[id] != hash {
			return errors.New("质量材料摘要被改写")
		}
	}
	for id, hash := range a.EvidenceHashes {
		if b.EvidenceHashes[id] != hash {
			return errors.New("质量来源摘要被改写")
		}
	}
	return nil
}
