package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type deliveryReviewPromptBytesKey struct{}
type deliveryReviewRequestKey struct{}
type deliveryReviewDeferredKey struct{}

var errDeliveryReviewUsed = errors.New("交付前处理已检查")

func recordedDeliveryReviewSummary(s taskState) *DeliveryReviewSummary {
	if s.Spec.DeliveryReview == nil {
		return nil
	}
	out := &DeliveryReviewSummary{Version: 1, Status: "not_run"}
	if d := s.DeliveryReview; d != nil {
		if d.Summary != nil {
			*out = *d.Summary
			out.Unresolved = append([]DeliveryReviewIssue(nil), d.Summary.Unresolved...)
		}
		out.Status, out.AdmissionCount, out.OriginRunID, out.ErrorClass = d.Status, d.AdmissionCount, d.OriginRunID, d.ErrorClass
		out.TriggerReasons = append([]string(nil), d.TriggerReasons...)
		out.ContentComparisons = append([]DeliveryReviewContentComparison(nil), d.ContentComparisons...)
	}
	return out
}

func (r *runState) refreshDeliveryReview(ctx context.Context) error {
	if !r.deliveryReviewEnabled() {
		return nil
	}
	summary := r.deliveryReviewSummary(ctx)
	err := r.record("delivery_review_updated", func(s *taskState) error { s.DeliveryReview.Summary = summary; return nil })
	if err == nil {
		r.observeDeliveryReview(ctx, "delivery_review_updated")
	}
	return err
}

// 事件先提交额度和一次性标记，下一根请求分别消费已扣的轮次与模型票据。
func (r *runState) admitDeliveryReview(ctx context.Context, candidate schema.Message) (bool, error) {
	if !r.deliveryReviewEnabled() {
		return false, nil
	}
	r.mu.Lock()
	evaluated := r.state.DeliveryReview.Evaluated
	r.mu.Unlock()
	if evaluated {
		return false, nil
	}
	summary := r.deliveryReviewSummary(ctx)
	if summary == nil {
		return false, nil
	}
	summary.Unresolved = append(summary.Unresolved, r.reviewDeliveryCandidate(ctx, candidate)...)
	if err := r.storageError(); err != nil {
		return false, err
	}
	var artifact string
	if len(summary.Unresolved) > 0 {
		r.mu.Lock()
		possible := r.reviewBudgetAvailableLocked(len(candidate.ToolCalls))
		r.mu.Unlock()
		if possible {
			// 只留候选展示正文和已剥离载体的工具意图，不新增完整响应或不透明推理项留存。
			raw, err := json.Marshal(struct {
				Content   string            `json:"content"`
				ToolCalls []schema.ToolCall `json:"tool_calls,omitempty"`
			}{Content: candidate.Content, ToolCalls: candidate.ToolCalls})
			if err != nil {
				return false, err
			}
			artifact = newID()
			if r.artifact == nil {
				return false, r.noteStorageError(fmt.Errorf("%w：缺少交付前候选材料存储", errStorage))
			}
			if err := r.artifact(artifact, raw); err != nil {
				return false, r.noteStorageError(fmt.Errorf("%w：保存交付前候选失败: %w", errStorage, err))
			}
		}
	}
	admitted := false
	candidates := deliveryReviewCandidates(candidate)
	err := r.record("delivery_review_evaluated", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d := s.DeliveryReview
		if d.Evaluated {
			return errDeliveryReviewUsed
		}
		d.Summary = summary
		if len(summary.Unresolved) == 0 {
			d.Status = "not_needed"
			return nil
		}
		d.Evaluated = true
		seen := map[string]bool{}
		for _, i := range summary.Unresolved {
			if !seen[i.Kind] {
				d.TriggerReasons = append(d.TriggerReasons, i.Kind)
				seen[i.Kind] = true
			}
		}
		d.Issues = append([]DeliveryReviewIssue(nil), summary.Unresolved...)
		if !r.reviewBudgetAvailableLocked(len(candidate.ToolCalls)) {
			d.Status = "not_run"
			d.ErrorClass = "budget_unavailable"
			return nil
		}
		s.Usage.ModelCalls++
		s.Usage.Turns++
		d.Status = "admitted"
		d.AdmissionCount = 1
		d.OriginRunID = r.runID
		d.CandidateArtifactID = artifact
		d.ContentComparisons = candidates
		admitted = true
		return nil
	})
	if errors.Is(err, errDeliveryReviewUsed) {
		return false, nil
	}
	if err == nil {
		r.observeDeliveryReview(ctx, "delivery_review_evaluated")
	}
	return admitted, err
}

// 调用者在锁内；工具额度须覆盖本批全部尝试，并留下现有主循环可进入的额度。
func (r *runState) reviewBudgetAvailableLocked(attempts int) bool {
	s := &r.state
	elapsed := r.elapsed
	if !r.started.IsZero() {
		elapsed += time.Since(r.started)
	}
	return s.Usage.ModelCalls < s.Limits.MaxModelCalls && s.Usage.Turns < s.Limits.MaxTurns &&
		s.Usage.ToolCalls+attempts < s.Limits.MaxToolCalls && elapsed < s.Limits.Timeout
}

func (r *runState) takeDeliveryReviewTurn(ctx context.Context) (bool, error) {
	if !r.deliveryReviewEnabled() {
		return false, nil
	}
	r.mu.Lock()
	d := r.state.DeliveryReview
	ready := d.Status == "admitted" && d.OriginRunID == r.runID && !d.TurnConsumed
	r.mu.Unlock()
	if !ready {
		return false, nil
	}
	err := r.record("delivery_review_turn", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d := s.DeliveryReview
		if d.Status != "admitted" || d.OriginRunID != r.runID || d.TurnConsumed {
			return errors.New("交付前轮次票据已消费")
		}
		d.TurnConsumed = true
		return nil
	})
	return err == nil, err
}
func (r *runState) takeDeliveryReviewModel(ctx context.Context) error {
	return r.record("delivery_review_request", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d := s.DeliveryReview
		if d == nil || d.Status != "admitted" || d.OriginRunID != r.runID || !d.TurnConsumed || d.ModelConsumed {
			return errors.New("交付前模型票据无效或已消费")
		}
		if s.Usage.ToolCalls >= s.Limits.MaxToolCalls {
			return toolLimit()
		}
		d.ModelConsumed = true
		return nil
	})
}
func (r *runState) finishDeliveryReviewRequest(ctx context.Context, requestErr error) error {
	return r.record("delivery_review_response", func(s *taskState) error {
		d := s.DeliveryReview
		if d == nil || d.Status != "admitted" || (!d.ModelConsumed && requestErr == nil) {
			return errors.New("交付前响应没有有效准入")
		}
		d.Status = "completed"
		if requestErr != nil {
			d.Status = "failed"
			d.ErrorClass = observationError(requestErr)
		}
		return nil
	})
}
func (r *runState) markInterruptedDeliveryReview() error {
	if !r.deliveryReviewEnabled() {
		return nil
	}
	r.mu.Lock()
	d := r.state.DeliveryReview
	interrupted := d.Status == "admitted" && d.OriginRunID != r.runID
	r.mu.Unlock()
	if !interrupted {
		return nil
	}
	return r.record("delivery_review_interrupted", func(s *taskState) error {
		s.DeliveryReview.Status = "outcome_unknown"
		s.DeliveryReview.ErrorClass = "interrupted"
		return nil
	})
}
func (r *runState) finalizeDeliveryReview(ctx context.Context) error {
	if !r.deliveryReviewEnabled() {
		return nil
	}
	r.mu.Lock()
	unknown := r.state.DeliveryReview.Status == "admitted"
	r.mu.Unlock()
	if unknown {
		if err := r.record("delivery_review_unconfirmed", func(s *taskState) error {
			s.DeliveryReview.Status = "outcome_unknown"
			s.DeliveryReview.ErrorClass = "request_unconfirmed"
			return nil
		}); err != nil {
			return err
		}
	}
	return r.refreshDeliveryReview(context.WithoutCancel(ctx))
}

func (r *runState) observeDeliveryReview(ctx context.Context, kind string) {
	r.mu.Lock()
	q := recordedDeliveryReviewSummary(r.state)
	r.mu.Unlock()
	if q == nil {
		return
	}
	observe.Emit(ctx, kind, observe.Payload{Stage: q.Status, Decision: "delivery_review", DeliveryReview: deliveryReviewObservation(q, true)})
}

func deliveryReviewObservation(q *DeliveryReviewSummary, enabled bool) *observe.DeliveryReviewSummary {
	if q == nil {
		return &observe.DeliveryReviewSummary{Version: 1, Enabled: enabled, Status: "not_run"}
	}
	out := &observe.DeliveryReviewSummary{Version: 1, Enabled: enabled, Status: q.Status, AdmissionCount: q.AdmissionCount, Documents: q.Documents, Addressed: q.Addressed, NotRelevant: q.NotRelevant, Unverified: q.Unverified, Undeclared: q.Undeclared, ReadyToResolve: q.ReadyToResolve, Unresolved: len(q.Unresolved)}
	for _, c := range q.ContentComparisons {
		switch c.Status {
		case "changed":
			out.ContentChanged++
		case "unchanged":
			out.ContentUnchanged++
		default:
			out.ContentUnconfirmed++
		}
	}
	return out
}

func (r *runState) deliveryReviewBoundary(ctx context.Context, registry interface {
	VisibleTools(context.Context) []schema.ToolDefinition
}, calls []schema.ToolCall, stepID string) bool {
	if !r.deliveryReviewEnabled() {
		return false
	}
	if len(calls) == 0 {
		return true
	}
	for _, call := range calls {
		if call.Name == "check_evidence" {
			ctx = context.WithValue(ctx, localCheckBatchKey{}, true)
			break
		}
	}
	for _, call := range calls {
		if call.Name != "write_file" && call.Name != "edit_file" {
			continue
		}
		if r.rejectLocalCheckCall(ctx, call) != nil {
			continue
		}
		key := actionKey(stepID, call)
		r.mu.Lock()
		_, reused := r.resumeReceipts[key]
		if stepID != "" && !reused {
			_, reused = r.state.Completed[key]
		}
		r.mu.Unlock()
		if reused {
			continue
		}
		if p, ok := registry.(interface {
			Preflight(context.Context, schema.ToolCall) *schema.ToolResult
		}); ok {
			if p.Preflight(ctx, call) == nil {
				return true
			}
			continue
		}
		// 不具备无副作用预检的自定义注册表保留原执行边界，不猜测其准入。
	}
	return false
}
