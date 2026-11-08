package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/openai/openai-go/v3"
)

const maxErrorEnvelopeBytes = 16 * 1024

var failureURL = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// RequestFailure 只向用户暴露白名单摘要；Cause 仅供 errors.Is/As，不参与序列化。
// FieldStates 区分未发送、null、实际值与未知；FieldValues 保存受限、脱敏的原JSON类型。
type RequestFailure struct {
	Kind, Provider, Protocol     string
	HTTPStatus                   *int
	Code, Type, Param, RequestID *string
	FieldStates                  map[string]string
	FieldValues                  map[string]json.RawMessage
	SafeSummary                  string
	RequestIDSource              string
	Cause                        error `json:"-"`
}

func (e *RequestFailure) Error() string {
	if e == nil {
		return "模型请求失败"
	}
	return e.SafeSummary
}

func (e *RequestFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// FailureKind 不解析自由文本错误，也不把所有网关 upstream_error 当作内容拒绝。
func FailureKind(err error) string {
	var failure *RequestFailure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}

func newFailure(channel, protocol string, err error) *RequestFailure {
	return &RequestFailure{Provider: channel, Protocol: protocol, Cause: err,
		FieldStates: map[string]string{"code": "unknown", "type": "unknown", "param": "unknown", "request_id": "unknown"},
		FieldValues: map[string]json.RawMessage{}}
}

// NormalizeRequestError 由每个Provider在SDK返回后调用，HTTP状态及错误字段来自SDK证据。
func NormalizeRequestError(ctx context.Context, channel, protocol string, err error) error {
	if err == nil {
		return nil
	}
	var existing *RequestFailure
	if errors.As(err, &existing) {
		bindRequestFailure(ctx, existing)
		return err
	}
	f := newFailure(channel, protocol, err)
	status, requestID, requestIDSource := providerResponse(ctx)
	f.HTTPStatus = status
	if requestID != "" {
		setFailureString(f, "request_id", requestID)
		f.RequestIDSource = requestIDSource
	}
	var raw string
	var response *http.Response
	var openaiErr *openai.Error
	var anthropicErr *anthropic.Error
	switch {
	case errors.As(err, &openaiErr):
		status := openaiErr.StatusCode
		f.HTTPStatus = &status
		raw = openaiErr.RawJSON()
		response = openaiErr.Response
	case errors.As(err, &anthropicErr):
		status := anthropicErr.StatusCode
		f.HTTPStatus = &status
		raw = anthropicErr.RawJSON()
		response = anthropicErr.Response
	}
	var envelope []byte
	if len(raw) <= maxErrorEnvelopeBytes {
		envelope = []byte(raw)
	}
	message, parsed := parseFailureEnvelope(f, envelope)
	// 兼容SDK只解包error但网关返回顶层JSON的情况。这里只读取SDK错误响应，
	// 并恢复等价reader供后续errors.As调用者查看；成功响应和发送中的请求不读取。
	if !parsed && response != nil && response.Body != nil {
		if data, readErr := readErrorEnvelope(response); readErr == nil {
			message, parsed = parseFailureEnvelope(f, data)
		}
	}
	if !parsed {
		if openaiErr != nil {
			if openaiErr.JSON.Code.Valid() {
				setFailureString(f, "code", openaiErr.Code)
			}
			if openaiErr.JSON.Type.Valid() {
				setFailureString(f, "type", openaiErr.Type)
			}
			if openaiErr.JSON.Param.Valid() {
				setFailureString(f, "param", openaiErr.Param)
			}
			if openaiErr.JSON.Message.Valid() {
				message = openaiErr.Message
			}
		}
		if anthropicErr != nil && anthropicErr.Type() != "" {
			setFailureString(f, "type", string(anthropicErr.Type()))
		}
	}
	if response != nil {
		for _, header := range []string{"x-request-id", "request-id", "anthropic-request-id"} {
			if value := strings.TrimSpace(response.Header.Get(header)); value != "" {
				setFailureString(f, "request_id", value)
				f.RequestIDSource = "header:" + header
				break
			}
		}
	}
	if f.RequestID == nil && anthropicErr != nil && anthropicErr.RequestID != "" {
		setFailureString(f, "request_id", anthropicErr.RequestID)
		f.RequestIDSource = "sdk"
	}
	f.Kind = classifyRequestFailure(f, message)
	if f.HTTPStatus != nil && *f.HTTPStatus >= 200 && *f.HTTPStatus < 300 {
		f.Kind = "model_response_invalid"
	}
	f.SafeSummary = safeFailureSummary(f, message)
	bindRequestFailure(ctx, f)
	return f
}

func responseInvalid(ctx context.Context, channel, protocol, message string) error {
	f := newFailure(channel, protocol, nil)
	status, requestID, source := providerResponse(ctx)
	f.HTTPStatus = status
	if requestID != "" {
		setFailureString(f, "request_id", requestID)
		f.RequestIDSource = source
	}
	f.Kind = "model_response_invalid"
	f.SafeSummary = "模型返回的数据无效：" + boundedFailureString(message, 512)
	bindRequestFailure(ctx, f)
	return f
}

// 私密推理块不作为可交付答复；无有效文字和工具调用时仍将已知用量交给计费层。
func validatedProviderMessage(ctx context.Context, channel, protocol string, message *schema.Message) (*schema.Message, error) {
	if message == nil || (strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0) {
		return message, responseInvalid(ctx, channel, protocol, "API 未返回有效答复或工具调用")
	}
	return message, nil
}

func setFailureString(f *RequestFailure, name, value string) {
	value = boundedFailureString(value, 256)
	f.FieldStates[name] = "value"
	f.FieldValues[name], _ = json.Marshal(value)
	switch name {
	case "code":
		f.Code = &value
	case "type":
		f.Type = &value
	case "param":
		f.Param = &value
	case "request_id":
		f.RequestID = &value
	}
}

func parseFailureEnvelope(f *RequestFailure, data []byte) (string, bool) {
	if len(data) == 0 || len(data) > maxErrorEnvelopeBytes {
		return "", false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return "", false
	}
	object := root
	if nested, ok := root["error"]; ok {
		if json.Unmarshal(nested, &object) != nil || object == nil {
			return "", false
		}
	}
	_, hasMessage := object["message"]
	_, hasType := object["type"]
	_, hasCode := object["code"]
	if !hasMessage && !hasType && !hasCode {
		return "", false
	}
	for _, name := range []string{"code", "type", "param", "request_id"} {
		raw, ok := object[name]
		if !ok && name == "request_id" {
			raw, ok = root[name]
		}
		if !ok {
			f.FieldStates[name] = "absent"
			continue
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			f.FieldStates[name] = "null"
			f.FieldValues[name] = json.RawMessage("null")
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			setFailureString(f, name, text)
			if name == "request_id" {
				f.RequestIDSource = "body"
			}
			continue
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			f.FieldStates[name] = "value"
			f.FieldValues[name] = append(json.RawMessage(nil), raw...)
			continue
		}
		f.FieldStates[name] = "unknown"
	}
	var message string
	_ = json.Unmarshal(object["message"], &message)
	return message, true
}

type joinedErrorBody struct {
	io.Reader
	io.Closer
}

func readErrorEnvelope(response *http.Response) ([]byte, error) {
	original := response.Body
	data, err := io.ReadAll(io.LimitReader(original, maxErrorEnvelopeBytes+1))
	response.Body = &joinedErrorBody{Reader: io.MultiReader(bytes.NewReader(data), original), Closer: original}
	if err != nil {
		return nil, err
	}
	if len(data) > maxErrorEnvelopeBytes {
		return nil, errors.New("错误响应超过诊断上限")
	}
	return data, nil
}

func classifyRequestFailure(f *RequestFailure, message string) string {
	if f.HTTPStatus == nil {
		return "model_transport_error"
	}
	status := *f.HTTPStatus
	code, kind := "", ""
	if f.Code != nil {
		code = strings.ToLower(*f.Code)
	}
	if f.Type != nil {
		kind = strings.ToLower(*f.Type)
	}
	explicit := map[string]bool{"content_filter": true, "content_policy_violation": true, "content_policy_error": true, "content_rejected": true}
	knownMessage := strings.TrimSpace(message) == "Input text data may contain inappropriate content."
	if explicit[code] || explicit[kind] || ((status == 400 || status == 403) && knownMessage) {
		return "model_content_rejected"
	}
	switch {
	case status == 400:
		return "model_bad_request"
	case status == 401:
		return "model_authentication_failed"
	case status == 403:
		return "model_permission_denied"
	case status == 429:
		return "model_rate_limited"
	case status >= 500 && status <= 599:
		return "model_service_error"
	default:
		return "model_api_error"
	}
}

func safeFailureSummary(f *RequestFailure, message string) string {
	labels := map[string]string{"model_content_rejected": "模型服务拒绝了输入内容", "model_bad_request": "模型请求参数被服务拒绝", "model_authentication_failed": "模型服务认证失败", "model_permission_denied": "模型服务权限不足", "model_rate_limited": "模型服务限流", "model_service_error": "模型上游服务错误", "model_transport_error": "模型请求传输失败", "model_api_error": "模型API请求失败", "model_response_invalid": "模型返回的数据无效"}
	text := labels[f.Kind]
	if f.HTTPStatus != nil {
		text += fmt.Sprintf("（HTTP %d）", *f.HTTPStatus)
	}
	if f.HTTPStatus != nil && strings.TrimSpace(message) != "" {
		text += "：" + boundedFailureString(message, 512)
	}
	return text
}

func boundedFailureString(value string, limit int) string {
	// 在大响应进入正则脱敏前限制工作集，末端可能被截断的已知密钥也须遮蔽。
	if len(value) > maxErrorEnvelopeBytes {
		value = value[:maxErrorEnvelopeBytes]
		value = observe.RedactPrefix(value)
	}
	// 上游message/param也可能回显带查询参数的地址，用户摘要不留原始URL。
	value = failureURL.ReplaceAllString(observe.Redact(strings.ToValidUTF8(value, "�")), "[URL]")
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
