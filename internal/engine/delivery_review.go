package engine

import (
	"errors"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// DeliveryReviewSpec 为普通任务提供一次交付前处理机会，不授予语义验收。
type DeliveryReviewSpec struct {
	Version int `json:"version"`
}

// DeliveryReviewIssue 只引用已有材料；完整正文仍在受限任务材料中。
type DeliveryReviewIssue struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Path        string   `json:"path,omitempty"`
	Version     string   `json:"version,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	FindingIDs  []string `json:"finding_ids,omitempty"`
	CheckIDs    []string `json:"check_ids,omitempty"`
	Reason      string   `json:"reason"`
}

type DeliveryReviewSummary struct {
	ContentComparisons []DeliveryReviewContentComparison `json:"content_comparisons,omitempty"`
	RecordedOnly       bool                              `json:"recorded_only,omitempty"`
	Version            int                               `json:"version"`
	Status             string                            `json:"status"`
	AdmissionCount     int                               `json:"admission_count"`
	OriginRunID        string                            `json:"origin_run_id,omitempty"`
	TriggerReasons     []string                          `json:"trigger_reasons,omitempty"`
	Unresolved         []DeliveryReviewIssue             `json:"unresolved,omitempty"`
	Documents          int                               `json:"documents"`
	Addressed          int                               `json:"addressed"`
	NotRelevant        int                               `json:"not_relevant"`
	Unverified         int                               `json:"unverified"`
	Undeclared         int                               `json:"undeclared"`
	ReadyToResolve     int                               `json:"ready_to_resolve"`
	Omitted            int                               `json:"omitted,omitempty"`
	ErrorClass         string                            `json:"error_class,omitempty"`
}

// DeliveryReviewState 的额度票据只能由原运行消费；恢复不得重新发起。
type DeliveryReviewState struct {
	ContentComparisons  []DeliveryReviewContentComparison `json:"content_comparisons,omitempty"`
	Version             int                               `json:"version"`
	Status              string                            `json:"status"`
	Evaluated           bool                              `json:"evaluated"`
	AdmissionCount      int                               `json:"admission_count"`
	OriginRunID         string                            `json:"origin_run_id,omitempty"`
	TurnConsumed        bool                              `json:"turn_consumed,omitempty"`
	ModelConsumed       bool                              `json:"model_consumed,omitempty"`
	TriggerReasons      []string                          `json:"trigger_reasons,omitempty"`
	Issues              []DeliveryReviewIssue             `json:"issues,omitempty"`
	CandidateArtifactID string                            `json:"candidate_artifact_id,omitempty"`
	ErrorClass          string                            `json:"error_class,omitempty"`
	Summary             *DeliveryReviewSummary            `json:"summary,omitempty"`
}

func (r *runState) deliveryReviewEnabled() bool {
	return r != nil && r.state.Spec.DeliveryReview != nil && r.state.Spec.DeliveryReview.Version == 1
}

func NormalizeDeliveryReviewSpec(spec *TaskSpec) error {
	if spec.DeliveryReview == nil {
		return nil
	}
	copy := *spec.DeliveryReview
	spec.DeliveryReview = &copy
	return ValidateDeliveryReviewSpec(*spec)
}

func ValidateDeliveryReviewSpec(spec TaskSpec) error {
	if spec.DeliveryReview == nil {
		return nil
	}
	if spec.DeliveryReview.Version != 1 {
		return errors.New("交付前处理仅支持version=1")
	}
	if spec.Analysis || spec.Report != nil || spec.Quality != nil || spec.Plan {
		return errors.New("交付前处理仅支持普通任务，不能组合分析、报告、质量或规划模式")
	}
	if spec.ReadContextVersion != 0 && spec.ReadContextVersion != 2 && spec.ReadContextVersion != 4 {
		return errors.New("交付前处理不能升级历史读取策略")
	}
	if spec.Mode != "" && spec.Mode != tools.ModeReadOnly && spec.Mode != tools.ModeWorkspace {
		return errors.New("交付前处理仅支持readonly或workspace权限")
	}
	if spec.Kind != "" && ((spec.Mode == tools.ModeReadOnly && spec.Kind != TaskAnswer) || (spec.Mode == tools.ModeWorkspace && spec.Kind != TaskChange)) {
		return errors.New("交付前处理须使用readonly + answer或workspace + change")
	}
	return nil
}

func validateDeliveryReviewState(s taskState) error {
	if s.Version != 3 {
		if s.Spec.DeliveryReview != nil || s.DeliveryReview != nil {
			return errors.New("历史任务不能携带交付前处理配置或状态")
		}
		return nil
	}
	if s.Spec.DeliveryReview == nil || s.Quality != nil || s.Spec.Quality != nil {
		return errors.New("版本3任务必须包含普通交付前处理配置")
	}
	if s.Spec.Mode == "" || s.Spec.Kind == "" {
		return errors.New("交付前处理持久任务缺少执行模式或类型")
	}
	if s.Spec.ReadContextVersion != 2 && s.Spec.ReadContextVersion != 4 {
		return errors.New("交付前处理持久任务须使用读取策略2或4")
	}
	if err := ValidateDeliveryReviewSpec(s.Spec); err != nil {
		return err
	}
	d := s.DeliveryReview
	if d == nil || d.Version != 1 || d.AdmissionCount < 0 || d.AdmissionCount > 1 {
		return errors.New("交付前处理状态或准入次数无效")
	}
	switch d.Status {
	case "not_run", "not_needed", "admitted", "completed", "failed", "outcome_unknown":
	default:
		return errors.New("交付前处理阶段无效")
	}
	if d.AdmissionCount == 0 && (d.OriginRunID != "" || d.TurnConsumed || d.ModelConsumed || d.Status == "admitted" || d.Status == "completed" || d.Status == "failed" || d.Status == "outcome_unknown") {
		return errors.New("交付前处理未准入却存在额度票据")
	}
	if d.AdmissionCount == 1 && (!d.Evaluated || !analysisIDValid(d.OriginRunID)) {
		return errors.New("交付前处理准入身份缺失")
	}
	if d.AdmissionCount == 1 && (d.Status == "not_run" || d.Status == "not_needed") {
		return errors.New("交付前处理准入后不能退回未运行阶段")
	}
	if d.ModelConsumed && !d.TurnConsumed {
		return errors.New("交付前处理模型票据不能先于轮次消费")
	}
	if d.Status == "completed" && (!d.ModelConsumed || !d.TurnConsumed) {
		return errors.New("交付前处理未消费请求却标完成")
	}
	if d.CandidateArtifactID != "" && !analysisIDValid(d.CandidateArtifactID) {
		return errors.New("交付前处理候选材料身份无效")
	}
	if err := validateDeliveryReviewComparisons(d); err != nil {
		return err
	}
	return nil
}

func validateDeliveryReviewTransition(before, after taskState) error {
	a, b := before.Spec.DeliveryReview, after.Spec.DeliveryReview
	if (a == nil) != (b == nil) || a != nil && *a != *b {
		return errors.New("同一任务不能更换交付前处理配置")
	}
	if before.DeliveryReview != nil && after.DeliveryReview != nil {
		x, y := before.DeliveryReview, after.DeliveryReview
		if y.AdmissionCount < x.AdmissionCount || x.Evaluated && !y.Evaluated || x.TurnConsumed && !y.TurnConsumed || x.ModelConsumed && !y.ModelConsumed {
			return errors.New("交付前处理票据不能重置")
		}
		if x.AdmissionCount == 1 && y.OriginRunID != x.OriginRunID {
			return errors.New("交付前处理准入运行身份不能改变")
		}
		if x.CandidateArtifactID != "" && x.CandidateArtifactID != y.CandidateArtifactID {
			return errors.New("交付前候选材料不能替换")
		}
		if x.AdmissionCount == 1 && !sameDeliveryReviewCandidates(x.ContentComparisons, y.ContentComparisons) {
			return errors.New("交付前候选正文身份不能替换")
		}
		if x.Status != y.Status {
			allowed := (x.Status == "not_run" || x.Status == "not_needed") && !x.Evaluated && (y.Status == "not_needed" || y.Status == "not_run" || y.Status == "admitted") || x.Status == "admitted" && (y.Status == "completed" || y.Status == "failed" || y.Status == "outcome_unknown")
			if !allowed {
				return errors.New("交付前处理阶段不能回退或重新准入")
			}
		}
		if x.Evaluated && x.AdmissionCount == 0 && y.AdmissionCount != 0 {
			return errors.New("首次交付前检查已结束，不能再次准入")
		}
	}
	return nil
}

func validateDeliveryReviewResume(request, saved TaskSpec) error {
	if request.DeliveryReview == nil {
		return nil
	}
	if saved.DeliveryReview == nil || *saved.DeliveryReview != *request.DeliveryReview {
		return fmt.Errorf("恢复时不能开启或更改交付前处理策略")
	}
	return nil
}
