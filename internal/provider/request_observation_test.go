package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func observedProviderEvents(t *testing.T, dir, id string) ([]observe.Event, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []observe.Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var event observe.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.EventType == "model_request_encoded" {
			events = append(events, event)
		}
		if event.EventType == "model_usage" {
			t.Fatal("请求字节观测生成了重复计费用量事件")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events, data
}

func TestProvidersObserveActualEncodedRequestWithoutBody(t *testing.T) {
	for _, tc := range []struct {
		name, model, path, response string
		newProvider                 func(string, string, string) LLMProvider
	}{
		{"兼容Chat", "deepseek-wire-fixture", "/chat/completions", `{"id":"chat_wire","choices":[{"message":{"role":"assistant","content":"完成"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`, func(model, key, url string) LLMProvider { return newCompatibleProvider(model, key, url) }},
		{"Responses", "gpt-6.1-sol", "/responses", `{"id":"resp_wire","status":"completed","output":[{"type":"message","id":"msg_wire","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":2}}`, func(model, key, url string) LLMProvider { return newCompatibleProvider(model, key, url) }},
		{"Anthropic", "claude-wire-fixture", "/v1/messages", `{"id":"msg_wire","type":"message","role":"assistant","content":[{"type":"text","text":"完成"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`, func(model, key, url string) LLMProvider { return newClaudeProvider(model, key, url) }},
	} {
		for _, failed := range []bool{false, true} {
			name := tc.name + "/成功"
			if failed {
				name = tc.name + "/失败"
			}
			t.Run(name, func(t *testing.T) {
				const privateBody = "不应进入观测的业务原文-private-body-9283"
				const privateKey = "wire-only-api-key-784392"
				received := make(chan int64, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.URL.Path != tc.path {
						t.Errorf("请求路径错误: %s", req.URL.Path)
					}
					body, err := io.ReadAll(req.Body)
					if err != nil || !bytes.Contains(body, []byte(privateBody)) {
						t.Errorf("请求正文未实际发送: %v", err)
					}
					received <- int64(len(body))
					w.Header().Set("Content-Type", "application/json")
					if failed {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"拒绝"}}`)
						return
					}
					_, _ = io.WriteString(w, tc.response)
				}))
				defer server.Close()
				dir := t.TempDir()
				ctx, attempt := observe.Start(context.Background(), observe.Options{WorkDir: t.TempDir(), Model: tc.model}, observe.Config{Dir: dir})
				id := observe.Identity(ctx).AttemptID
				ctx = observe.WithModelCall(ctx, "wire-model-call")
				model := tc.newProvider(tc.model, privateKey, server.URL+"/")
				result, err := model.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: privateBody}}, []schema.ToolDefinition{{Name: "read_file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}})
				if failed && err == nil || !failed && err != nil {
					t.Fatalf("请求结果错误: %+v %v", result, err)
				}
				if !failed && (result.Usage == nil || result.Usage.PromptTokens != 4 || result.Usage.CompletionTokens != 2) {
					t.Fatalf("观测改变了用量: %+v", result)
				}
				attempt.Close()
				events, stored := observedProviderEvents(t, dir, id)
				if len(events) != 1 || events[0].ModelCallID != "wire-model-call" || events[0].Payload.Model != tc.model || events[0].Payload.EncodedRequestBytes == nil || *events[0].Payload.EncodedRequestBytes != <-received || events[0].Payload.HTTPAttempt != 1 {
					t.Fatalf("实际编码请求长度或归属错误: %+v", events)
				}
				if bytes.Contains(stored, []byte(privateBody)) || bytes.Contains(stored, []byte(privateKey)) {
					t.Fatal("请求正文或凭据进入了观测")
				}
			})
		}
	}
}

func TestEncodedRequestUnknownSizeDoesNotConsumeOriginalBody(t *testing.T) {
	for _, replayable := range []bool{false, true} {
		dir := t.TempDir()
		ctx, attempt := observe.Start(context.Background(), observe.Options{WorkDir: t.TempDir()}, observe.Config{Dir: dir})
		id := observe.Identity(ctx).AttemptID
		ctx = observe.WithModelCall(ctx, "unknown-size-call")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unused.invalid", strings.NewReader("冻结原文"))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = -1
		if !replayable {
			req.GetBody = nil
		}
		_, err = requestEncodingObserver("fixture")(req, func(sent *http.Request) (*http.Response, error) {
			body, readErr := io.ReadAll(sent.Body)
			if readErr != nil || string(body) != "冻结原文" {
				t.Fatalf("观测消费或修改原请求: %q %v", body, readErr)
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		attempt.Close()
		events, _ := observedProviderEvents(t, dir, id)
		if len(events) != 1 || (events[0].Payload.EncodedRequestBytes != nil) != replayable {
			t.Fatalf("未知长度被记成零或副本测量失败: %+v", events)
		}
		if replayable && *events[0].Payload.EncodedRequestBytes != int64(len("冻结原文")) {
			t.Fatal("副本编码长度错误")
		}
	}
}

func TestEncodedHTTPRetriesDoNotDuplicateModelUsage(t *testing.T) {
	var sent atomic.Int64
	received := make(chan int64, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		received <- int64(len(body))
		w.Header().Set("Content-Type", "application/json")
		if sent.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"瞬时失败"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"chat_retry","choices":[{"message":{"role":"assistant","content":"完成"}}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	ctx, attempt := observe.Start(context.Background(), observe.Options{WorkDir: t.TempDir()}, observe.Config{Dir: dir})
	id := observe.Identity(ctx).AttemptID
	ctx = observe.WithModelCall(ctx, "one-model-call")
	model := &OpenAIProvider{model: "deepseek-fixture", chat: openai.NewChatCompletionService(option.WithAPIKey("fixture"), option.WithBaseURL(server.URL), option.WithMaxRetries(1), option.WithMiddleware(requestEncodingObserver("deepseek-fixture")))}
	result, err := model.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "请求"}}, nil)
	if err != nil || result.Usage == nil || result.Usage.PromptTokens != 4 {
		t.Fatalf("HTTP重试改变了最终回执: %+v %v", result, err)
	}
	attempt.Close()
	events, _ := observedProviderEvents(t, dir, id)
	if len(events) != 2 || sent.Load() != 2 {
		t.Fatalf("HTTP尝试没有分别记录: %+v", events)
	}
	for i, event := range events {
		if event.ModelCallID != "one-model-call" || event.Payload.HTTPAttempt != int64(i+1) || event.Payload.EncodedRequestBytes == nil || *event.Payload.EncodedRequestBytes != <-received {
			t.Fatalf("重试的编码字节或调用归属不符: %+v", event)
		}
	}
}
