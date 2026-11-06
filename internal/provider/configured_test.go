package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestConfiguredProvidersUseSeparateURLsAndKeys(t *testing.T) {
	for _, tc := range []struct {
		channel, model, path, response string
	}{
		{channel: "openai", model: provider.DefaultOpenAIModel, path: "/openai/v1/responses", response: `{"id":"resp_test","status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","content":[{"type":"output_text","text":"成功","annotations":[]}]}]}`},
		{channel: "zhipu", model: "glm-local-test", path: "/zhipu/v1/chat/completions", response: `{"id":"chat_test","choices":[{"message":{"role":"assistant","content":"成功"}}]}`},
		{channel: "deepseek", model: provider.DefaultModel, path: "/deepseek/v1/chat/completions", response: `{"id":"chat_test","choices":[{"message":{"role":"assistant","content":"成功"}}]}`},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer fixture-"+tc.channel {
					t.Errorf("渠道URL、方法或密钥混用: method=%s path=%s", r.Method, r.URL.Path)
				}
				if r.Header.Get("OpenAI-Organization") != "fixture-organization" || r.Header.Get("OpenAI-Project") != "fixture-project" || r.Header.Get("X-Fixture") != "configured" {
					t.Error("组织、项目或自定义Header没有保留")
				}
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != tc.model {
					t.Errorf("模型配置错误: model=%q error=%v", body.Model, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			for _, channel := range []string{"openai", "zhipu", "deepseek"} {
				prefix := strings.ToUpper(channel)
				t.Setenv(prefix+"_API_KEY", "fixture-"+channel)
				t.Setenv(prefix+"_BASE_URL", "  "+server.URL+"/"+channel+"/v1/  ")
			}
			t.Setenv("OPENAI_ORG_ID", "fixture-organization")
			t.Setenv("OPENAI_PROJECT_ID", "fixture-project")
			t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Fixture: configured\nIgnored-Line")
			p, err := provider.NewConfiguredProvider(tc.channel, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			message, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "本地渠道验证"}}, nil)
			if err != nil || message == nil || message.Content != "成功" || requests.Load() != 1 {
				t.Fatalf("渠道请求失败: requests=%d message=%+v error=%v", requests.Load(), message, err)
			}
		})
	}
}

type configuredRoundTrip func(*http.Request) (*http.Response, error)

func (f configuredRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmptyChannelURLsKeepExistingDefaults(t *testing.T) {
	for _, tc := range []struct{ channel, wantURL string }{
		{"openai", "https://api.openai.com/v1/chat/completions"},
		{"zhipu", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"deepseek", "https://api.deepseek.com/chat/completions"},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			t.Setenv(strings.ToUpper(tc.channel)+"_API_KEY", "fixture-key")
			t.Setenv(strings.ToUpper(tc.channel)+"_BASE_URL", " \t ")
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			calls := 0
			http.DefaultTransport = configuredRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != tc.wantURL {
					t.Errorf("默认渠道URL发生变化: got=%s want=%s", r.URL, tc.wantURL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"本地回执"}}]}`)), Request: r}, nil
			})
			p, err := provider.NewConfiguredProvider(tc.channel, "fixture-model")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "仅拦截URL"}}, nil); err != nil || calls != 1 {
				t.Fatalf("默认地址验证失败: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestThirdPartyChannelIgnoresInvalidOpenAIBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"独立配置成功"}}]}`)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", "\t://invalid-openai-url")
	t.Setenv("OPENAI_API_KEY", "unrelated-openai-fixture")
	for _, channel := range []string{"zhipu", "deepseek"} {
		t.Run(channel, func(t *testing.T) {
			prefix := strings.ToUpper(channel)
			t.Setenv(prefix+"_API_KEY", "fixture-"+channel)
			t.Setenv(prefix+"_BASE_URL", server.URL+"/v1/")
			p, err := provider.NewConfiguredProvider(channel, channel+"-local")
			if err != nil {
				t.Fatal(err)
			}
			message, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "本地验证"}}, nil)
			if err != nil || message == nil || message.Content != "独立配置成功" {
				t.Fatalf("无关OpenAI配置干扰渠道: message=%+v error=%v", message, err)
			}
		})
	}
}

func TestConfiguredProviderRejectsInvalidConfiguration(t *testing.T) {
	for _, channel := range []string{"openai", "zhipu", "deepseek"} {
		t.Run(channel, func(t *testing.T) {
			key := strings.ToUpper(channel) + "_API_KEY"
			t.Setenv("OPENAI_API_KEY", "fixture-openai")
			t.Setenv("ZHIPU_API_KEY", "fixture-zhipu")
			t.Setenv("DEEPSEEK_API_KEY", "fixture-deepseek")
			t.Setenv(key, " ")
			if p, err := provider.NewConfiguredProvider(channel, "fixture-model"); err == nil || p != nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("未检查渠道自己的密钥: channel=%s error=%v", channel, err)
			}
			if p, err := provider.NewConfiguredProvider(channel, " "); err == nil || p != nil || !strings.Contains(err.Error(), "模型名称") {
				t.Fatalf("空模型没有明确拒绝: error=%v", err)
			}
		})
	}
	if p, err := provider.NewConfiguredProvider("unknown", "fixture-model"); err == nil || p != nil || !strings.Contains(err.Error(), "未知模型渠道") {
		t.Fatalf("未知渠道没有明确拒绝: error=%v", err)
	}
}
