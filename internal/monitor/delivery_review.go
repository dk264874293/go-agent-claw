package monitor

import (
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func reviewViewIDs(ids []string) []string {
	var values []string
	for i, id := range ids {
		if i >= 16 {
			break
		}
		values = append(values, clip(observe.Redact(id), 128))
	}
	return values
}

func (x *Index) projectDeliveryReview(t *Task, result *engine.DeliveryReviewSummary) {
	if result == nil {
		return
	}
	view := &DeliveryReview{Version: result.Version, Status: clip(observe.Redact(result.Status), 64), AdmissionCount: result.AdmissionCount, OriginRunID: clip(observe.Redact(result.OriginRunID), 128), Documents: result.Documents, Addressed: result.Addressed, NotRelevant: result.NotRelevant, Unverified: result.Unverified, Undeclared: result.Undeclared, ReadyToResolve: result.ReadyToResolve, Omitted: result.Omitted, ErrorClass: clip(observe.Redact(result.ErrorClass), 128)}
	for i, c := range result.ContentComparisons {
		if i >= 64 {
			view.Omitted += len(result.ContentComparisons) - i
			break
		}
		view.ContentComparisons = append(view.ContentComparisons, engine.DeliveryReviewContentComparison{Kind: clip(observe.Redact(c.Kind), 32), Path: clip(observe.Redact(c.Path), 2000), Status: clip(observe.Redact(c.Status), 32), ActionID: clip(observe.Redact(c.ActionID), 128), CandidateSHA256: clip(c.CandidateSHA256, 64), ConfirmedSHA256: clip(c.ConfirmedSHA256, 64)})
	}
	for i, reason := range result.TriggerReasons {
		if i >= 16 {
			break
		}
		view.TriggerReasons = append(view.TriggerReasons, clip(observe.Redact(reason), 500))
	}
	for i, issue := range result.Unresolved {
		if i >= 64 {
			view.Omitted += len(result.Unresolved) - i
			break
		}
		view.Unresolved = append(view.Unresolved, DeliveryReviewIssue{ID: clip(observe.Redact(issue.ID), 128), Kind: clip(observe.Redact(issue.Kind), 128), Path: clip(observe.Redact(issue.Path), 2000), Version: clip(observe.Redact(issue.Version), 128), EvidenceIDs: reviewViewIDs(issue.EvidenceIDs), FindingIDs: reviewViewIDs(issue.FindingIDs), CheckIDs: reviewViewIDs(issue.CheckIDs), Reason: clip(observe.Redact(issue.Reason), 1200)})
	}
	t.DeliveryReview = view
}
