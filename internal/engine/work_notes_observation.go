package engine

import (
	"context"
	"encoding/json"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type workNotesSnapshot struct {
	accepted  bool
	processed map[string]bool
	deferred  map[string]bool
}

// 快照只用于兼容文字载体的计数，不参与处理资格或执行裁决。
func (r *runState) workNotesObservationSnapshot(ctx context.Context, text string) workNotesSnapshot {
	result := workNotesSnapshot{processed: map[string]bool{}, deferred: map[string]bool{}}
	if !r.workNotesCarrierEnabled() {
		return result
	}
	agent := observe.Identity(ctx).AgentID
	r.mu.Lock()
	defer r.mu.Unlock()
	result.accepted = r.analysisRuntime.accepted[agent][analysisMessageKey(text)]
	if state := analysisStateOf(r.state); state != nil {
		for _, id := range state.Processed[agent] {
			result.processed[id] = true
		}
		for _, item := range state.Reports[agent].DeferredReads {
			result.deferred[item.EvidenceID] = true
		}
	}
	return result
}

func (r *runState) observeWorkNotesTextFallback(ctx context.Context, text string, before workNotesSnapshot, batch *workNotesCarrierBatch) {
	if !r.workNotesCarrierEnabled() {
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &envelope) != nil || len(envelope) != 1 || envelope["work_notes"] == nil {
		return
	}
	after := r.workNotesObservationSnapshot(ctx, text)
	batch.response.Submitted++
	if !after.accepted {
		batch.response.Rejected++
		return
	}
	if before.accepted {
		batch.response.Duplicates++
		return
	}
	batch.response.Accepted++
	for id := range after.processed {
		if !before.processed[id] {
			batch.response.ProcessedSources++
		}
	}
	for id := range after.deferred {
		if !before.deferred[id] {
			batch.response.DeferredSources++
		}
	}
}

// 每个有效模型响应仅发一条事件，不能把同批其余工具未携带笔记算成多次缺失。
func (r *runState) observeWorkNotesResponse(ctx context.Context, msg *schema.Message, batch workNotesCarrierBatch, reason string) {
	if !r.workNotesCarrierEnabled() || msg == nil || msg.SourceModelCallID == "" {
		return
	}
	eligible := false
	for _, call := range msg.ToolCalls {
		eligible = eligible || r.workNotesCarrierTool(call.Name)
	}
	opportunity := eligible && msg.NotesCandidateCount > 0
	metrics := batch.response
	if reason == "" {
		switch {
		case msg.NotesCandidateCount == 0:
			reason = "no_candidates"
		case !eligible:
			reason = "no_eligible_tool"
		case metrics.Accepted > 0 && metrics.ProcessedSources == 0 && metrics.DeferredSources > 0:
			reason = "deferred_only"
		case metrics.Accepted > 0:
			reason = "accepted"
		case metrics.Rejected > 0:
			reason = "rejected"
		case metrics.Duplicates > 0:
			reason = "duplicate"
		default:
			reason = "missing"
		}
	}
	observe.Emit(observe.WithModelCall(ctx, msg.SourceModelCallID), "work_notes_response", observe.Payload{
		NotesCandidates: msg.NotesCandidateCount, NotesOpportunity: opportunity,
		NotesResponseAccepted: metrics.Accepted > 0, NotesProcessedSources: int64(metrics.ProcessedSources),
		NotesDeferredSources: int64(metrics.DeferredSources), NotesSubmitted: int64(metrics.Submitted),
		NotesResponseRejected: int64(metrics.Rejected), NotesDuplicates: int64(metrics.Duplicates), ErrorClass: reason,
	})
}
