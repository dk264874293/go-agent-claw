package provider

import (
	"io"
	"net/http"
	"strconv"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
)

// requestEncodingObserver 位于SDK序列化之后，将有界副本交给请求诊断旁路。
// 原有观测事件仅记录请求长度；不消费实际Body或响应流，不新增用量事件。
func requestEncodingObserver(model string, origin ...string) func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	channel, protocol := "unknown", "unknown"
	if len(origin) > 0 {
		channel = origin[0]
	}
	if len(origin) > 1 {
		protocol = origin[1]
	}
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		diagnostic := requestdiag.BeginHTTP(req.Context(), req, channel, protocol)
		var size *int64
		switch {
		case req.Body == nil || req.Body == http.NoBody:
			n := int64(0)
			size = &n
		case req.ContentLength > 0:
			n := req.ContentLength
			size = &n
		case req.GetBody != nil:
			// 少数自定义请求不提供长度；只测可重新获取的副本，测量失败保持未知。
			if body, err := req.GetBody(); err == nil && body != nil {
				if n, err := io.Copy(io.Discard, io.LimitReader(body, (1<<20)+1)); err == nil && n <= 1<<20 {
					size = &n
				}
				_ = body.Close()
			}
		}
		attempt := int64(1)
		if n, err := strconv.ParseInt(req.Header.Get("X-Stainless-Retry-Count"), 10, 64); err == nil && n >= 0 && n < 1<<30 {
			attempt = n + 1
		}
		observe.Emit(req.Context(), "model_request_encoded", observe.Payload{Model: model, EncodedRequestBytes: size, HTTPAttempt: attempt})
		resp, err := next(req)
		rememberProviderResponse(req.Context(), resp)
		diagnostic.Finish(resp, err)
		return resp, err
	}
}
