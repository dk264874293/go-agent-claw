package engine

import (
	"context"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

type featurePromptBytesKey struct{}

// 新短块仅描述核对方法，不包含课程答案，也不要求额外响应或私密思维。
const claimFocusPrompt = `结论核对：字段缺失或丢失判断须确认对象、字段、版本和比较两端；字段存在、非空、类型正确、合法性分开。静态观察、推断与运行结果分开；无执行证据的效果列待实测。否定结论限定范围。重要事实、条件、引用和未解决项可增量记入现有findings；引用通过不证明语义正确。`

func (r *runState) readFeaturePrompt(ctx context.Context) string {
	if r.state.Spec.ReadContextVersion == 3 && r.localChecksEnabled() {
		return "\n" + ordinarySourceQualityPrompt + r.localCheckContext(ctx)
	}
	if !r.readFeaturesV2() {
		return ""
	}
	text := ""
	if r.readFeatureEnabled("claim_focus") {
		text += "\n" + claimFocusPrompt
	}
	if r.localChecksEnabled() {
		text += r.localCheckContext(ctx)
	}
	return text
}

func observationReadFeatures(spec TaskSpec) *observe.ReadFeatureSummary {
	if spec.ReadContextVersion < 2 {
		return &observe.ReadFeatureSummary{Contract: "legacy_read_policy"}
	}
	if spec.ReadContextVersion == 3 {
		return &observe.ReadFeatureSummary{SourceViews: true, LocalChecks: true, ClaimFocus: true, Contract: "legacy_v3"}
	}
	if spec.ReadFeatures == nil {
		return &observe.ReadFeatureSummary{Version: 1, Contract: "baseline_v2"}
	}
	f := spec.ReadFeatures
	return &observe.ReadFeatureSummary{Version: f.Version, SourceViews: f.SourceViews, LocalChecks: f.LocalChecks, ClaimFocus: f.ClaimFocus, Contract: "experimental_v2"}
}

// 观察不授予证据资格。只输出已经由执行/投影路径确认的本次消费者视图身份。
func (r *runState) observeSourceViewLifecycle(ctx context.Context, kind string, ids []string) {
	identity := observe.Identity(ctx)
	var payloads []observe.Payload
	r.mu.Lock()
	seen := map[string]bool{}
	for _, id := range ids {
		entry, ok := r.analysisRuntime.sources[id]
		if seen[id] || !ok || entry.Receipt.SourceView == nil || entry.RunID != r.runID || entry.AgentID != identity.AgentID {
			continue
		}
		seen[id] = true
		start, end := entry.Receipt.ByteStart, entry.Receipt.ByteEnd
		payloads = append(payloads, observe.Payload{ViewEvidenceID: id, OriginEvidenceID: entry.Receipt.SourceView.OriginEvidenceID, ArtifactID: entry.ArtifactID, ContentBytes: end - start, ByteStart: &start, ByteEnd: &end, SourceKind: "current"})
	}
	r.mu.Unlock()
	for _, p := range payloads {
		observe.Emit(ctx, kind, p)
	}
}
