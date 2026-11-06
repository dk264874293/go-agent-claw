package main

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
)

func TestBenchmarkChannelSelection(t *testing.T) {
	for _, tc := range []struct{ channel, model, want string }{
		{"auto", "", "deepseek"},
		{"auto", provider.DefaultModel, "deepseek"},
		{"auto", "gpt-local", "openai"},
		{"auto", "glm-local", "zhipu"},
		{"auto", "deepseek-local", "deepseek"},
		{"openai", "glm-local", "openai"},
		{"zhipu", "custom-local", "zhipu"},
		{"deepseek", "custom-local", "deepseek"},
	} {
		got, _, err := provider.ResolveModelConfig(tc.channel, tc.model)
		if err != nil || got != tc.want {
			t.Errorf("渠道选择错误: channel=%s model=%s got=%s want=%s", tc.channel, tc.model, got, tc.want)
		}
	}
}
