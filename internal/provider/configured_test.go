package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestConfiguredOpenAIOutputLimitAndUsage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "测试密钥")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"mock","choices":[{"index":0,"message":{"role":"assistant","content":"完成"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()
	p, err := NewConfigured("openai", "mock", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithMaxOutputTokens(context.Background(), 123)
	msg, err := p.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil)
	if err != nil || msg.Usage == nil || msg.Usage.PromptTokens != 3 {
		t.Fatalf("响应解析错误: %+v %v", msg, err)
	}
	if request["max_completion_tokens"] != float64(123) {
		t.Fatalf("未传输出上限: %+v", request)
	}
	if _, err := p.(InputEstimator).EstimateInput(ctx, []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredAnthropicOutputLimitAndUsage(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "测试密钥")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"mock","content":[{"type":"text","text":"完成"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer server.Close()
	p, err := NewConfigured("deepseek-anthropic", "mock", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithMaxOutputTokens(context.Background(), 321)
	msg, err := p.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil)
	if err != nil || msg.Usage == nil || msg.Usage.CompletionTokens != 2 {
		t.Fatalf("响应解析错误: %+v %v", msg, err)
	}
	if request["max_tokens"] != float64(321) {
		t.Fatalf("未传输出上限: %+v", request)
	}
	if _, err := p.(InputEstimator).EstimateInput(ctx, []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil); err != nil {
		t.Fatal(err)
	}
}
