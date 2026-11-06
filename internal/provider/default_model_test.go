package provider_test

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
)

func TestDefaultModelAndExplicitChannelSelection(t *testing.T) {
	for _, tc := range []struct {
		channel, model, wantChannel, wantModel string
	}{
		{"", "", "deepseek", "deepseek-v4.1-flash-1017"},
		{"auto", "", "deepseek", "deepseek-v4.1-flash-1017"},
		{"deepseek", "", "deepseek", "deepseek-v4.1-flash-1017"},
		{"openai", "", "openai", provider.DefaultOpenAIModel},
		{"auto", "gpt-6.1-sol", "openai", "gpt-6.1-sol"},
		{"auto", "glm-fixture", "zhipu", "glm-fixture"},
		{"deepseek", "custom-model", "deepseek", "custom-model"},
		{"openai", "deepseek-compatible", "openai", "deepseek-compatible"},
	} {
		channel, model, err := provider.ResolveModelConfig(tc.channel, tc.model)
		if err != nil || channel != tc.wantChannel || model != tc.wantModel {
			t.Fatalf("渠道或模型选择错误: channel=%q model=%q error=%v", channel, model, err)
		}
	}
	for _, channel := range []string{"zhipu", "unknown"} {
		if _, _, err := provider.ResolveModelConfig(channel, ""); err == nil {
			t.Fatalf("缺少有效模型配置时未报错: %s", channel)
		}
	}
}
