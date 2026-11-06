package main

import (
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
)

func TestAgentOpsModelChannelConfiguration(t *testing.T) {
	for _, tc := range []struct{ channel, model, wantModel, wantError string }{
		{"", "", provider.DefaultModel, ""},
		{"openai", "", provider.DefaultOpenAIModel, ""},
		{"zhipu", "glm-local", "glm-local", ""},
		{"deepseek", "deepseek-local", "deepseek-local", ""},
		{"zhipu", "", "", "CLAW_MODEL"},
		{"deepseek", "", provider.DefaultModel, ""},
		{"", "gpt-6.1-sol", "gpt-6.1-sol", ""},
		{"unknown", "fixture-model", "fixture-model", "未知模型渠道"},
	} {
		t.Run(tc.channel+"/"+tc.model, func(t *testing.T) {
			t.Setenv("CLAW_PROVIDER", tc.channel)
			t.Setenv("CLAW_MODEL", tc.model)
			t.Setenv("OPENAI_API_KEY", "fixture-openai")
			t.Setenv("ZHIPU_API_KEY", "fixture-zhipu")
			t.Setenv("DEEPSEEK_API_KEY", "fixture-deepseek")
			model, llm, err := configuredModelProvider()
			if model != tc.wantModel {
				t.Fatalf("模型默认或指定值错误: got=%q want=%q", model, tc.wantModel)
			}
			if tc.wantError != "" {
				if err == nil || llm != nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("配置未明确拒绝: error=%v", err)
				}
			} else if err != nil || llm == nil {
				t.Fatalf("合法配置未创建Provider: error=%v", err)
			}
		})
	}
}
