package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDefaultOpenAIResponsesToolRoundTrip(t *testing.T) {
	testOpenAIResponsesToolRoundTrip(t, provider.DefaultOpenAIModel)
}

func TestExplicitSolResponsesToolRoundTrip(t *testing.T) {
	testOpenAIResponsesToolRoundTrip(t, "gpt-6.1-sol")
}

func testOpenAIResponsesToolRoundTrip(t *testing.T, model string) {
	t.Helper()
	const firstOutput = `[
		{"type":"reasoning","id":"rs_first","summary":[],"encrypted_content":"encrypted-test"},
		{"type":"message","id":"msg_first","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"我来读取文件。","annotations":[]}]},
		{"type":"function_call","id":"fc_first","call_id":"call_read","status":"completed","name":"read_file","arguments":"{\"path\":\"test.txt\"}"}
	]`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("请求路由错误: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model              string            `json:"model"`
			Input              []json.RawMessage `json:"input"`
			Tools              []json.RawMessage `json:"tools"`
			Store              *bool             `json:"store"`
			Include            []string          `json:"include"`
			Reasoning          json.RawMessage   `json:"reasoning"`
			PreviousResponseID json.RawMessage   `json:"previous_response_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("无法解析请求: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Model != model || body.Store == nil || *body.Store ||
			!reflect.DeepEqual(body.Include, []string{"reasoning.encrypted_content"}) ||
			len(body.Reasoning) != 0 || len(body.PreviousResponseID) != 0 {
			t.Errorf("模型、无状态模式或推理参数错误: %+v", body)
		}
		if len(body.Tools) != 2 {
			t.Errorf("工具数为 %d，预期 2", len(body.Tools))
		} else {
			assertResponsesJSON(t, body.Tools[0], `{"type":"function","name":"read_file","description":"读取文件","strict":false,"parameters":{"type":"object","properties":{"path":{"type":"string"},"max_bytes":{"type":"integer"}},"required":["path"]}}`)
			assertResponsesJSON(t, body.Tools[1], `{"type":"function","name":"list_dir","description":"列出工作区目录的直接子项。","strict":false,"parameters":{"type":"object","properties":{"path":{"type":"string","description":"工作区相对路径，默认 ."}},"additionalProperties":false}}`)
		}

		expected := []string{
			`{"role":"system","content":"请使用工具。"}`,
			`{"role":"user","content":"读取测试文件。"}`,
		}
		if request == 2 {
			var original []json.RawMessage
			if err := json.Unmarshal([]byte(firstOutput), &original); err != nil {
				t.Errorf("测试夹具错误: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			for _, item := range original {
				expected = append(expected, string(item))
			}
			expected = append(expected, `{"type":"function_call_output","call_id":"call_read","output":"测试内容"}`)
		}
		if len(body.Input) != len(expected) {
			t.Errorf("第 %d 次请求的输入项有 %d 项，预期 %d 项", request, len(body.Input), len(expected))
		} else {
			for i, want := range expected {
				assertResponsesJSON(t, body.Input[i], want)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		switch request {
		case 1:
			_, _ = w.Write([]byte(`{"id":"resp_first","object":"response","status":"completed","output":` + firstOutput + `,"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}`))
		case 2:
			_, _ = w.Write([]byte(`{"id":"resp_second","object":"response","status":"completed","output":[{"type":"message","id":"msg_second","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"读取成功。","annotations":[]}]}],"usage":{"input_tokens":31,"output_tokens":5,"total_tokens":36}}`))
		default:
			t.Errorf("意外的第 %d 次请求", request)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "qa-placeholder")
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
	p := provider.NewOpenAIProvider(model)
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "请使用工具。"},
		{Role: schema.RoleUser, Content: "读取测试文件。"},
	}
	defs := []schema.ToolDefinition{
		{Name: "read_file", Description: "读取文件", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": "integer"}},
			"required": []string{"path"},
		}},
		tools.NewListDirTool(t.TempDir()).Definition(),
	}
	first, err := p.Generate(context.Background(), msgs, defs)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []schema.ToolCall{{ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"test.txt"}`)}}
	if first.Role != schema.RoleAssistant || first.Content != "我来读取文件。" || !reflect.DeepEqual(first.ToolCalls, wantCalls) ||
		len(first.ResponseItems) != 3 || first.Usage == nil || *first.Usage != (schema.Usage{PromptTokens: 17, CompletionTokens: 9}) {
		t.Fatalf("首轮响应转换不正确: %+v", first)
	}
	msgs = append(msgs, *first, schema.Message{Role: schema.RoleUser, ToolCallID: first.ToolCalls[0].ID, Content: "测试内容"})
	second, err := p.Generate(context.Background(), msgs, defs)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || second.Content != "读取成功。" || len(second.ToolCalls) != 0 || second.Usage == nil ||
		*second.Usage != (schema.Usage{PromptTokens: 31, CompletionTokens: 5}) {
		t.Fatalf("第二轮响应转换不正确，已请求 %d 次: %+v", requests.Load(), second)
	}
}

func assertResponsesJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal([]byte(want), &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Errorf("JSON 内容不匹配\n实际: %s\n期望: %s", got, want)
	}
}
