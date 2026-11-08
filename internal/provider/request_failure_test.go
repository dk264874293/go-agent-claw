package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/openai/openai-go/v3"
)

func TestProvidersClassifyStructuredRequestFailures(t *testing.T) {
	providers := []struct {
		name, model string
		create      func(string, string, string) LLMProvider
	}{
		{"chat", "chat-fixture", func(m, k, u string) LLMProvider { return newCompatibleProviderForChannel(m, k, u, "deepseek") }},
		{"responses", "gpt-6.1-sol", func(m, k, u string) LLMProvider { return newCompatibleProviderForChannel(m, k, u, "openai") }},
		{"anthropic", "claude-fixture", func(m, k, u string) LLMProvider { return newClaudeProviderForChannel(m, k, u, "zhipu") }},
	}
	cases := []struct {
		name       string
		status     int
		body, kind string
	}{
		{"原任务拒绝消息", 400, `{"error":{"type":"upstream_error","message":"Input text data may contain inappropriate content.","param":null}}`, "model_content_rejected"},
		{"兼容顶层拒绝", 400, `{"type":"upstream_error","message":"Input text data may contain inappropriate content.","param":null}`, "model_content_rejected"},
		{"明确内容码", 400, `{"error":{"code":"content_filter","message":"拒绝"}}`, "model_content_rejected"},
		{"普通上游类型", 400, `{"error":{"type":"upstream_error","message":"The upstream request is invalid"}}`, "model_bad_request"},
		{"参数错误", 400, `{"error":{"type":"invalid_request_error","code":"invalid_field","param":"temperature","message":"参数无效"}}`, "model_bad_request"},
		{"认证", 401, `{"error":{"type":"authentication_error","message":"拒绝"}}`, "model_authentication_failed"},
		{"权限", 403, `{"error":{"message":"权限不足"}}`, "model_permission_denied"},
		{"限流", 429, `{"error":{"message":"过于频繁"}}`, "model_rate_limited"},
		{"HTML上游错误", 502, `<html>bad gateway</html>`, "model_service_error"},
		{"未知HTTP", 418, `{"error":{"message":"未知"}}`, "model_api_error"},
	}
	for _, p := range providers {
		for _, tc := range cases {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				count := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Request-ID", "request-fixture-id")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				_, err := p.create(p.model, "fixture", server.URL+"/").Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "正常材料"}}, nil)
				var failure *RequestFailure
				if !errors.As(err, &failure) || FailureKind(err) != tc.kind || failure.HTTPStatus == nil || *failure.HTTPStatus != tc.status {
					t.Fatalf("错误分类不准确: kind=%s failure=%+v err=%v", FailureKind(err), failure, err)
				}
				if failure.RequestID == nil || *failure.RequestID != "request-fixture-id" || failure.RequestIDSource != "header:x-request-id" {
					t.Fatalf("服务端关联ID缺失: %+v", failure)
				}
				if count != 1 {
					t.Fatalf("错误诊断自动重试: %d", count)
				}
				if strings.Contains(tc.name, "拒绝") && failure.FieldStates["param"] != "null" {
					t.Fatalf("null参数被当作未设置: %+v", failure.FieldStates)
				}
			})
		}
	}
}

func TestFailureFieldsPreservePresenceAndSafeErrorDoesNotExposeNativeError(t *testing.T) {
	t.Setenv("PROVIDER_DIAG_SECRET", "private-query-secret-983472")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"bad api_key=private-query-secret-983472 see https://private-gateway.invalid/path?tenant=private-tenant","type":"","code":0,"param":null}}`)
	}))
	defer server.Close()
	_, err := newCompatibleProvider("fixture", "fixture", server.URL+"/?api_key=private-query-secret-983472").Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "正文"}}, nil)
	var f *RequestFailure
	if !errors.As(err, &f) {
		t.Fatal("缺少类型化失败")
	}
	if f.FieldStates["code"] != "value" || string(f.FieldValues["code"]) != "0" || f.FieldStates["type"] != "value" || f.Type == nil || *f.Type != "" || f.FieldStates["param"] != "null" || f.FieldStates["request_id"] != "absent" {
		t.Fatalf("字段存在状态丢失: %+v %+v", f.FieldStates, f.FieldValues)
	}
	if strings.Contains(err.Error(), "private-query-secret") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "private-gateway") || strings.Contains(err.Error(), "private-tenant") {
		t.Fatalf("Error泄露原始URL或凭据: %v", err)
	}
	var native *openai.Error
	if !errors.As(err, &native) || native.Response == nil {
		t.Fatal("Unwrap丢失SDK错误")
	}
	data, readErr := io.ReadAll(native.Response.Body)
	if readErr != nil || len(data) == 0 {
		t.Fatalf("诊断消费了SDK错误响应: %v", readErr)
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		t.Fatal("SDK原始错误响应被修改")
	}
}

func TestLastHTTPAttemptClearsPriorResponseStatus(t *testing.T) {
	ctx := withProviderResponse(context.Background())
	rememberProviderResponse(ctx, &http.Response{StatusCode: 503, Header: http.Header{"X-Request-Id": []string{"old-response"}}})
	rememberProviderResponse(ctx, nil)
	failure := NormalizeRequestError(ctx, "openai", "chat_completions", context.DeadlineExceeded)
	var f *RequestFailure
	if !errors.As(failure, &f) || f.HTTPStatus != nil || f.RequestID != nil || FailureKind(failure) != "model_transport_error" || !errors.Is(failure, context.DeadlineExceeded) {
		t.Fatalf("末次传输失败沿用了前次HTTP响应: %+v", f)
	}
}

func TestRequestFailureKeepsContextCauseAndDoesNotExposeTransportURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newCompatibleProvider("fixture", "fixture", "http://127.0.0.1:1/?password=private-value").Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "正文"}}, nil)
	if !errors.Is(err, context.Canceled) || FailureKind(err) != "model_transport_error" || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("取消cause或安全摘要错误: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	_, err = newCompatibleProvider("fixture", "fixture", "http://127.0.0.1:1/").Generate(ctx, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("请求超时cause丢失: %v", err)
	}
}

func TestProviderInvalidResponsesAreTypedWithoutFalseSuccess(t *testing.T) {
	for _, test := range []struct{ model, response string }{
		{"fixture", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0}}`},
		{"gpt-6.1-sol", `{"id":"resp_fixture","status":"incomplete","output":[],"usage":{"input_tokens":1,"output_tokens":0}}`},
	} {
		t.Run(test.model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			_, err := newCompatibleProvider(test.model, "fixture", server.URL+"/").Generate(context.Background(), nil, nil)
			if FailureKind(err) != "model_response_invalid" {
				t.Fatalf("空/未完成响应未分类: %v", err)
			}
		})
	}
}
