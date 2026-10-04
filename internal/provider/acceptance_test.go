package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func acceptanceProvider(t *testing.T, kind, url string) LLMProvider {
	t.Helper()
	for _, key := range []string{"OPENAI_API_KEY", "ZHIPU_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(key, "验收用假密钥")
	}
	p, err := NewConfigured(kind, "mock", url)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func protocolResponse(kind, content, usage string) string {
	if strings.Contains(kind, "anthropic") {
		return fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"mock","content":%s,"stop_reason":"end_turn","usage":%s}`, content, usage)
	}
	return fmt.Sprintf(`{"id":"m","object":"chat.completion","created":1,"model":"mock","choices":[{"index":0,"message":%s,"finish_reason":"stop"}],"usage":%s}`, content, usage)
}

func TestAcceptanceProviderReplay(t *testing.T) {
	for _, kind := range []string{"openai", "deepseek-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				requests = append(requests, request)
				w.Header().Set("Content-Type", "application/json")
				if kind == "openai" {
					fmt.Fprint(w, protocolResponse(kind, `{"role":"assistant","content":"先检查","tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"甲\"}"}},{"id":"b","type":"function","function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]}`, `{"prompt_tokens":7,"completion_tokens":3}`))
				} else {
					fmt.Fprint(w, protocolResponse(kind, `[{"type":"text","text":"先检查"},{"type":"tool_use","id":"a","name":"read_file","input":{"path":"甲"}},{"type":"tool_use","id":"b","name":"list_files","input":{"path":"."}}]`, `{"input_tokens":7,"output_tokens":3}`))
				}
			}))
			defer server.Close()
			p := acceptanceProvider(t, kind, server.URL)
			history := []schema.Message{{Role: schema.RoleSystem, Content: "规则"}, {Role: schema.RoleUser, Content: "开始"}}
			defs := []schema.ToolDefinition{{Name: "read_file", Description: "读取", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}}
			msg, err := p.Generate(context.Background(), history, defs)
			if err != nil || msg == nil {
				t.Fatalf("响应: %v %v", msg, err)
			}
			if msg.Content != "先检查" || len(msg.ToolCalls) != 2 || msg.ToolCalls[0].ID != "a" || msg.ToolCalls[1].ID != "b" || msg.Usage.PromptTokens != 7 {
				t.Fatalf("文本、工具或账单丢失: %+v", msg)
			}
			history = append(history, *msg, schema.Message{Role: schema.RoleUser, ToolCallID: "a", Content: "甲内容"}, schema.Message{Role: schema.RoleUser, ToolCallID: "b", Content: "目录结果"})
			if _, err = p.Generate(context.Background(), history, defs); err != nil {
				t.Fatal(err)
			}
			messages := requests[1]["messages"].([]any)
			if kind == "openai" {
				assistant := messages[2].(map[string]any)
				calls := assistant["tool_calls"].([]any)
				if assistant["content"] != "先检查" || len(calls) != 2 {
					t.Fatal(assistant)
				}
				for i, tc := range msg.ToolCalls {
					call := calls[i].(map[string]any)
					if call["id"] != tc.ID || call["function"].(map[string]any)["arguments"] != string(tc.Arguments) {
						t.Fatal(call)
					}
					result := messages[3+i].(map[string]any)
					if result["role"] != "tool" || result["tool_call_id"] != tc.ID || result["content"] != history[3+i].Content {
						t.Fatal(result)
					}
				}
			} else {
				assistant := messages[1].(map[string]any)
				blocks := assistant["content"].([]any)
				if len(blocks) != 3 || blocks[0].(map[string]any)["text"] != "先检查" {
					t.Fatal(assistant)
				}
				for i, tc := range msg.ToolCalls {
					block := blocks[i+1].(map[string]any)
					var args map[string]any
					_ = json.Unmarshal(tc.Arguments, &args)
					if block["id"] != tc.ID || block["name"] != tc.Name || block["input"].(map[string]any)["path"] != args["path"] {
						t.Fatal(block)
					}
					result := messages[2+i].(map[string]any)["content"].([]any)[0].(map[string]any)
					if result["tool_use_id"] != tc.ID || result["content"].([]any)[0].(map[string]any)["text"] != history[3+i].Content {
						t.Fatal(result)
					}
				}
			}
		})
	}
}

func TestAcceptanceProviderCancellation(t *testing.T) {
	for _, kind := range []string{"openai", "deepseek-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			started, released := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-r.Context().Done():
				case <-released:
				}
			}))
			defer server.Close()
			defer close(released)
			p := acceptanceProvider(t, kind, server.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := p.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "取消"}}, nil)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("请求未开始")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消语义: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP 请求未响应取消")
			}
		})
	}
}

func TestAcceptanceProviderInvalidResponses(t *testing.T) {
	for _, kind := range []string{"openai", "deepseek-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			content, usage, empty := `{"role":"assistant","content":"完成"}`, `{"prompt_tokens":4,"completion_tokens":2}`, `{"role":"assistant","content":""}`
			if strings.Contains(kind, "anthropic") {
				content = `[{"type":"text","text":"完成"}]`
				usage = `{"input_tokens":4,"output_tokens":2}`
				empty = `[]`
			}
			cases := []struct {
				name, body     string
				status         int
				wantErr, known bool
			}{
				{"正常", protocolResponse(kind, content, usage), 200, false, true},
				{"缺少账单", protocolResponse(kind, content, `null`), 200, false, false},
				{"空账单", protocolResponse(kind, content, `{}`), 200, false, false},
				{"部分账单", protocolResponse(kind, content, `{"prompt_tokens":4,"input_tokens":4}`), 200, false, false},
				{"负数账单", protocolResponse(kind, content, `{"prompt_tokens":-4,"completion_tokens":2,"input_tokens":-4,"output_tokens":2}`), 200, false, false},
				{"小数账单", protocolResponse(kind, content, `{"prompt_tokens":1.5,"completion_tokens":2,"input_tokens":1.5,"output_tokens":2}`), 200, false, false},
				{"真实零账单", protocolResponse(kind, content, `{"prompt_tokens":0,"completion_tokens":0,"input_tokens":0,"output_tokens":0}`), 200, false, true},
				{"空响应", protocolResponse(kind, empty, usage), 200, true, true},
				{"空对象", `{}`, 200, true, false},
				{"损坏JSON", `{"`, 200, true, false},
				{"HTTP失败", `{"error":{"type":"invalid_request_error","message":"模拟失败"}}`, 400, true, false},
			}
			if kind == "openai" {
				cases = append(cases, struct {
					name, body     string
					status         int
					wantErr, known bool
				}{"空choices有账单", `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`, 200, true, true})
			}
			invalidCalls := []string{
				`{"id":"","name":"read_file"}`, `{"id":"a","name":""}`,
				`{"id":"a","name":"read_file"},{"id":"a","name":"read_file"}`,
			}
			for i, rawCalls := range invalidCalls {
				var calls []map[string]string
				_ = json.Unmarshal([]byte("["+rawCalls+"]"), &calls)
				var blocks []map[string]any
				for _, call := range calls {
					if kind == "openai" {
						blocks = append(blocks, map[string]any{"id": call["id"], "type": "function", "function": map[string]any{"name": call["name"], "arguments": "{}"}})
					} else {
						blocks = append(blocks, map[string]any{"type": "tool_use", "id": call["id"], "name": call["name"], "input": map[string]any{}})
					}
				}
				var body []byte
				if kind == "openai" {
					body, _ = json.Marshal(map[string]any{"role": "assistant", "tool_calls": blocks})
				} else {
					body, _ = json.Marshal(blocks)
				}
				cases = append(cases, struct {
					name, body     string
					status         int
					wantErr, known bool
				}{fmt.Sprintf("非法工具标识%d", i), protocolResponse(kind, string(body), usage), 200, true, true})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(tc.status)
						fmt.Fprint(w, tc.body)
					}))
					defer server.Close()
					p := acceptanceProvider(t, kind, server.URL)
					msg, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil)
					if (err != nil) != tc.wantErr {
						t.Fatalf("错误判定: msg=%+v err=%v", msg, err)
					}
					known := msg != nil && msg.Usage != nil
					if known != tc.known {
						t.Fatalf("账单已知性: msg=%+v err=%v", msg, err)
					}
				})
			}
		})
	}
}

func TestAcceptanceConfiguredKinds(t *testing.T) {
	for _, tc := range []struct{ kind, key, url, limit string }{
		{"openai", "OPENAI_API_KEY", DefaultGatewayURL, "max_completion_tokens"},
		{"zhipu-openai", "ZHIPU_API_KEY", ZhipuURL, "max_tokens"},
		{"deepseek-openai", "DEEPSEEK_API_KEY", DeepseekOpenAIURL, "max_tokens"},
		{"zhipu-anthropic", "ZHIPU_API_KEY", ZhipuURL, "max_tokens"},
		{"deepseek-anthropic", "DEEPSEEK_API_KEY", DeepseekClaudeURL, "max_tokens"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			for _, key := range []string{"OPENAI_API_KEY", "ZHIPU_API_KEY", "DEEPSEEK_API_KEY"} {
				t.Setenv(key, "")
			}
			if _, err := NewConfigured(tc.kind, "mock", ""); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatal("缺密钥未拒绝", err)
			}
			t.Setenv(tc.key, "fake")
			if _, err := NewConfigured(tc.kind, "", ""); err == nil {
				t.Fatal("空模型未拒绝")
			}
			if DefaultURL(tc.kind) != tc.url {
				t.Fatal("默认地址错误")
			}
			var request map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&request)
				if r.Header.Get("Authorization") != "Bearer fake" && r.Header.Get("X-Api-Key") != "fake" {
					t.Error("密钥映射错误")
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(tc.kind, "anthropic") {
					fmt.Fprint(w, protocolResponse(tc.kind, `[{"type":"text","text":"完成"}]`, `{"input_tokens":1,"output_tokens":1}`))
				} else {
					fmt.Fprint(w, protocolResponse(tc.kind, `{"role":"assistant","content":"完成"}`, `{"prompt_tokens":1,"completion_tokens":1}`))
				}
			}))
			defer server.Close()
			p, err := NewConfigured(tc.kind, "mock", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Generate(WithMaxOutputTokens(context.Background(), 37), []schema.Message{{Role: schema.RoleUser, Content: "测试"}}, nil); err != nil {
				t.Fatal(err)
			}
			if request[tc.limit] != float64(37) || request["model"] != "mock" {
				t.Fatal(request)
			}
		})
	}
	if _, err := NewConfigured("未知", "mock", ""); err == nil {
		t.Fatal("未知供应商未拒绝")
	}
}
