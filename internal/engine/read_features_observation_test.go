package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestReadFeaturesClaimPromptIsBoundedAndIndependent(t *testing.T) {
	if len(claimFocusPrompt) > 1024 {
		t.Fatalf("短提示超过1KiB: %d", len(claimFocusPrompt))
	}
	for _, answer := range []string{"collected_at", "97.5", "29.5", "organizer", "tags"} {
		if strings.Contains(claimFocusPrompt, answer) {
			t.Fatalf("生产提示包含私有反例答案: %s", answer)
		}
	}
	ctx, r := carrierFixture(t)
	if r.readFeaturePrompt(ctx) != "" {
		t.Fatal("关闭态混入新增提示")
	}
	r.state.Spec.ReadContextVersion = 4
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, SourceViews: true}
	if r.readFeaturePrompt(ctx) != "" {
		t.Fatal("来源视图开关联动语义提示")
	}
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, ClaimFocus: true}
	if !strings.Contains(r.readFeaturePrompt(ctx), claimFocusPrompt) || r.localChecksEnabled() {
		t.Fatal("结论提示开启了核对能力或提示缺失")
	}
	r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, LocalChecks: true}
	if strings.Contains(r.readFeaturePrompt(ctx), claimFocusPrompt) || strings.Contains(r.readFeaturePrompt(ctx), ordinarySourceQualityPrompt) {
		t.Fatal("核对开关联动新增或旧语义长提示")
	}
}

func TestCancelledSuccessfulResponseDoesNotGrantSourceDelivery(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	ctx, cancel := context.WithCancel(ctx)
	model := scriptedProvider(func(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		cancel()
		return &schema.Message{Role: schema.RoleAssistant, Content: "返回同时已取消"}, nil
	})
	_, err := NewAgentEngine(model, nil, EngineOptions{}).generate(ctx, r, messages, nil)
	if !errors.Is(err, context.Canceled) || len(r.analysisRuntime.delivered["root"]) != 0 {
		t.Fatalf("取消请求授予来源资格: %v %+v", err, r.analysisRuntime.delivered)
	}
}
