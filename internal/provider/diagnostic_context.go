package provider

import (
	"context"
	"net/http"
	"sync"

	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
)

func bindRequestFailure(ctx context.Context, failure *RequestFailure) {
	if failure == nil {
		return
	}
	requestdiag.BindError(ctx, requestdiag.ErrorInfo{
		Kind: failure.Kind, HTTPStatus: failure.HTTPStatus, Code: failure.Code, Type: failure.Type,
		Param: failure.Param, RequestID: failure.RequestID, RequestIDSource: failure.RequestIDSource,
		SafeSummary: failure.SafeSummary, FieldStates: failure.FieldStates, FieldValues: failure.FieldValues,
	})
}

func actualProvider(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

type providerCallKey struct{}
type providerCallResponse struct {
	mu                         sync.Mutex
	status                     *int
	requestID, requestIDSource string
}

// SDK在2xx之后也可能返回解析错误；这个局部句柄只绑定本次Generate的响应元数据。
// 它不持有response/request Body，不读取成功流，也不是Provider上的共享currentCall。
func withProviderResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, providerCallKey{}, &providerCallResponse{})
}
func rememberProviderResponse(ctx context.Context, response *http.Response) {
	state, _ := ctx.Value(providerCallKey{}).(*providerCallResponse)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	// SDK重试的最后一次可能没有HTTP响应，不能沿用前次5xx作为本次结果。
	state.status = nil
	state.requestID, state.requestIDSource = "", ""
	if response == nil {
		return
	}
	status := response.StatusCode
	state.status = &status
	for _, key := range []string{"x-request-id", "request-id", "anthropic-request-id"} {
		if value := response.Header.Get(key); value != "" {
			state.requestID, state.requestIDSource = value, "header:"+key
			break
		}
	}
}
func providerResponse(ctx context.Context) (*int, string, string) {
	state, _ := ctx.Value(providerCallKey{}).(*providerCallResponse)
	if state == nil {
		return nil, "", ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status == nil {
		return nil, state.requestID, state.requestIDSource
	}
	status := *state.status
	return &status, state.requestID, state.requestIDSource
}
