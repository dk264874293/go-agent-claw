package observe

import (
	"encoding/json"
	"strings"
)

// ParameterValue 保留未发送、null、零值与未知之间的区别，不携带任意参数对象。
type ParameterValue struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value,omitempty"`
}

type RequestErrorSummary struct {
	HTTPStatus *int           `json:"http_status,omitempty"`
	Code       ParameterValue `json:"code"`
	Type       ParameterValue `json:"type"`
	Param      ParameterValue `json:"param"`
	RequestID  ParameterValue `json:"request_id"`
	Summary    string         `json:"summary,omitempty"`
}

// RequestSummary 是常驻日志的白名单，完整输入清单和诊断正文不能进入本类型。
type RequestSummary struct {
	Provider            string                    `json:"provider,omitempty"`
	Protocol            string                    `json:"protocol,omitempty"`
	EndpointID          string                    `json:"endpoint_id,omitempty"`
	Model               string                    `json:"model,omitempty"`
	Parameters          map[string]ParameterValue `json:"parameters,omitempty"`
	InputCount          *int                      `json:"input_count,omitempty"`
	RoleCounts          map[string]int            `json:"role_counts,omitempty"`
	ToolNames           []string                  `json:"tool_names,omitempty"`
	SchemaFingerprint   string                    `json:"schema_fingerprint,omitempty"`
	RequestFingerprint  string                    `json:"request_fingerprint,omitempty"`
	HTTPStatus          *int                      `json:"http_status,omitempty"`
	ServiceRequestID    string                    `json:"service_request_id,omitempty"`
	ManifestID          string                    `json:"manifest_id,omitempty"`
	DiagnosticID        string                    `json:"diagnostic_id,omitempty"`
	PreviousModelCallID string                    `json:"previous_model_call_id,omitempty"`
	CaptureMode         string                    `json:"capture_mode,omitempty"`
	CaptureStatus       string                    `json:"capture_status,omitempty"`
	CaptureReason       string                    `json:"capture_reason,omitempty"`
	CaptureComplete     *bool                     `json:"capture_complete,omitempty"`
	CaptureRedacted     *bool                     `json:"capture_redacted,omitempty"`
	OriginalBytes       *int64                    `json:"original_bytes,omitempty"`
	SavedBytes          *int64                    `json:"saved_bytes,omitempty"`
	PreviousAvailable   *bool                     `json:"previous_available,omitempty"`
	OmittedItems        int                       `json:"omitted_items,omitempty"`
	ChangedInputs       *int                      `json:"changed_inputs,omitempty"`
	AddedInputs         *int                      `json:"added_inputs,omitempty"`
	RemovedInputs       *int                      `json:"removed_inputs,omitempty"`
	ChangedParameters   []string                  `json:"changed_parameters,omitempty"`
	SchemaChanged       *bool                     `json:"schema_changed,omitempty"`
	DiffComplete        *bool                     `json:"diff_complete,omitempty"`
	Error               *RequestErrorSummary      `json:"error,omitempty"`
}

var requestParameterNames = map[string]bool{
	"temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true,
	"max_output_tokens": true, "stream": true, "parallel_tool_calls": true, "store": true,
	"tool_choice": true, "tool_choice_mode": true, "tool_choice_name": true,
	"response_format_type": true, "response_format_schema_fingerprint": true,
	"reasoning_effort": true, "reasoning": true, "response_format": true, "include": true,
}

func cleanParameter(p ParameterValue, secrets []string) ParameterValue {
	switch p.State {
	case "absent", "unknown":
		return ParameterValue{State: p.State}
	case "null":
		return ParameterValue{State: "null", Value: json.RawMessage("null")}
	case "value":
		if len(p.Value) > 1024 || !json.Valid(p.Value) {
			return ParameterValue{State: "unknown"}
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(p.Value)))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return ParameterValue{State: "unknown"}
		}
		switch v := value.(type) {
		case string:
			value = bounded(redact(v, secrets), 128)
		case bool, json.Number:
		case nil:
			return ParameterValue{State: "null", Value: json.RawMessage("null")}
		default:
			return ParameterValue{State: "unknown"}
		}
		encoded, _ := json.Marshal(value)
		if len(encoded) > 160 {
			return ParameterValue{State: "unknown"}
		}
		return ParameterValue{State: "value", Value: encoded}
	default:
		return ParameterValue{State: "unknown"}
	}
}

// SanitizeRequestSummary 供只读投影复用生产端白名单，历史输入不能绕过字段限制。
func SanitizeRequestSummary(input *RequestSummary) *RequestSummary {
	return cleanRequestSummary(input, knownSecrets())
}

func cleanRequestSummary(input *RequestSummary, secrets []string) *RequestSummary {
	if input == nil {
		return nil
	}
	p := *input
	cloneBool := func(x *bool) *bool {
		if x == nil {
			return nil
		}
		v := *x
		return &v
	}
	cloneBytes := func(x *int64) *int64 {
		if x == nil || *x < 0 {
			return nil
		}
		v := *x
		return &v
	}
	p.CaptureComplete, p.CaptureRedacted, p.PreviousAvailable = cloneBool(p.CaptureComplete), cloneBool(p.CaptureRedacted), cloneBool(p.PreviousAvailable)
	p.SchemaChanged, p.DiffComplete = cloneBool(p.SchemaChanged), cloneBool(p.DiffComplete)
	p.OriginalBytes, p.SavedBytes = cloneBytes(p.OriginalBytes), cloneBytes(p.SavedBytes)
	cloneCount := func(x *int) *int {
		if x == nil || *x < 0 {
			return nil
		}
		v := *x
		return &v
	}
	p.InputCount = cloneCount(p.InputCount)
	p.AddedInputs, p.RemovedInputs, p.ChangedInputs = cloneCount(p.AddedInputs), cloneCount(p.RemovedInputs), cloneCount(p.ChangedInputs)
	fields := []*string{&p.Provider, &p.Protocol, &p.EndpointID, &p.Model, &p.SchemaFingerprint, &p.RequestFingerprint, &p.ServiceRequestID, &p.ManifestID, &p.DiagnosticID, &p.PreviousModelCallID, &p.CaptureMode, &p.CaptureStatus, &p.CaptureReason}
	for _, field := range fields {
		*field = bounded(redact(*field, secrets), 128)
	}
	p.Parameters = map[string]ParameterValue{}
	p.ChangedParameters = nil
	for _, name := range input.ChangedParameters {
		if requestParameterNames[name] {
			p.ChangedParameters = append(p.ChangedParameters, name)
		}
	}
	for name, value := range input.Parameters {
		if requestParameterNames[name] {
			p.Parameters[name] = cleanParameter(value, secrets)
		}
	}
	p.RoleCounts = map[string]int{}
	for name, value := range input.RoleCounts {
		switch name {
		case "system", "developer", "user", "assistant", "tool", "message", "function_call", "function_call_output", "tool_use", "tool_result", "reasoning", "unknown":
			p.RoleCounts[name] = max(0, value)
		}
	}
	p.ToolNames = nil
	for i, name := range input.ToolNames {
		if i >= 32 {
			p.OmittedItems += len(input.ToolNames) - i
			break
		}
		p.ToolNames = append(p.ToolNames, bounded(redact(name, secrets), 128))
	}
	p.OmittedItems = max(0, p.OmittedItems)
	copyStatus := func(value *int) *int {
		if value == nil || *value < 100 || *value > 599 {
			return nil
		}
		n := *value
		return &n
	}
	p.HTTPStatus = copyStatus(input.HTTPStatus)
	if input.Error != nil {
		e := *input.Error
		e.HTTPStatus = copyStatus(e.HTTPStatus)
		e.Code, e.Type, e.Param, e.RequestID = cleanParameter(e.Code, secrets), cleanParameter(e.Type, secrets), cleanParameter(e.Param, secrets), cleanParameter(e.RequestID, secrets)
		e.Summary = bounded(redact(e.Summary, secrets), 512)
		p.Error = &e
	}
	for {
		b, _ := json.Marshal(p)
		if len(b) <= 4*1024 || len(p.ToolNames) == 0 {
			break
		}
		p.ToolNames = p.ToolNames[:len(p.ToolNames)-1]
		p.OmittedItems++
	}
	return &p
}
