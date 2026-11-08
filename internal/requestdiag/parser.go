package requestdiag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var parameterNames = []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "max_output_tokens", "stream", "tool_choice", "parallel_tool_calls", "response_format", "text", "store", "include", "reasoning", "reasoning_effort"}

func (h *HTTPAttempt) parse(body []byte) {
	m := &h.meta
	m.Parameters = map[string]Parameter{}
	for _, name := range parameterNames {
		m.Parameters[name] = Parameter{Presence: "unknown"}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		m.CaptureState, m.CaptureReason = "unavailable", "invalid_json"
		return
	}
	for _, name := range parameterNames {
		v, ok := root[name]
		p := Parameter{Presence: "absent"}
		if ok {
			p.Presence = "value"
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				p.Presence = "null"
			} else {
				p.Value = h.safeParameter(name, v)
			}
		}
		m.Parameters[name] = p
	}
	_ = json.Unmarshal(root["model"], &m.Model)
	m.Model = safeText(m.Model, 256, h.call.a.secrets)
	m.Fingerprint = h.call.a.fingerprint(body)
	m.RoleCounts = map[string]int{}
	add := func(path string, raw json.RawMessage, count bool) {
		var info struct {
			Role string `json:"role"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &info)
		role := info.Role
		if path == "system" {
			role = "system"
		}
		switch role {
		case "system", "user", "assistant", "tool":
		default:
			role = "other"
		}
		if count {
			m.RoleCounts[role]++
			m.InputCount++
		}
		item := InputItem{JSONPath: path, Role: role, Type: safeText(info.Type, 128, h.call.a.secrets), Bytes: int64(len(raw)), Fingerprint: h.call.a.fingerprint(raw)}
		for idx, paths := range h.mappings {
			for _, p := range paths {
				if p == path {
					for _, origin := range h.call.origins {
						if origin.InputIndex == idx {
							copy := origin
							item.Origin = &copy
							break
						}
					}
				}
			}
		}
		if item.Origin == nil {
			item.Origin = &Origin{InputIndex: -1, Representation: "unknown", SourceKind: "unknown"}
		}
		if len(m.Items) < 512 {
			m.Items = append(m.Items, item)
		} else {
			m.OmittedItems++
		}
	}
	if v, ok := root["system"]; ok {
		add("system", v, true)
	}
	for _, field := range []string{"messages", "input"} {
		v, ok := root[field]
		if !ok {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(v, &items) == nil {
			for i, item := range items {
				add(fmt.Sprintf("%s[%d]", field, i), item, true)
				if field == "messages" {
					var b struct {
						Content []json.RawMessage `json:"content"`
					}
					if json.Unmarshal(item, &b) == nil {
						for j, block := range b.Content {
							path := fmt.Sprintf("messages[%d].content[%d]", i, j)
							if h.hasMapping(path) {
								add(path, block, false)
							}
						}
					}
				}
			}
		} else {
			add(field, v, true)
		}
	}
	var tools []json.RawMessage
	if json.Unmarshal(root["tools"], &tools) == nil {
		m.ToolCount = len(tools)
		m.SchemaFingerprint = h.call.a.fingerprint(root["tools"])
		for _, raw := range tools {
			var tool struct {
				Name     string `json:"name"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(raw, &tool)
			name := tool.Name
			if name == "" {
				name = tool.Function.Name
			}
			if len(m.ToolNames) < 128 {
				m.ToolNames = append(m.ToolNames, safeText(name, 128, h.call.a.secrets))
			}
		}
	}
	m.CaptureState = "complete"
	m.CaptureReason = ""
	// 全局观测只使用这些安全的标量摘要；复杂参数保留在本地白名单清单。
	if p := m.Parameters["tool_choice"]; p.Presence == "value" {
		var scalar string
		if json.Unmarshal(p.Value, &scalar) == nil {
			m.ToolChoiceMode = scalar
		} else {
			var fields map[string]string
			if json.Unmarshal(p.Value, &fields) == nil {
				m.ToolChoiceMode = fields["mode"]
				if m.ToolChoiceMode == "" {
					m.ToolChoiceMode = fields["type"]
				}
				m.ToolChoiceName = fields["name"]
			}
		}
	}
	if p := m.Parameters["response_format"]; p.Presence == "value" {
		var fields map[string]string
		if json.Unmarshal(p.Value, &fields) == nil {
			m.ResponseFormatType = fields["type"]
			m.ResponseFormatSchemaFingerprint = fields["fingerprint"]
		}
	}
	if p := m.Parameters["text"]; p.Presence == "value" {
		var fields map[string]string
		if json.Unmarshal(p.Value, &fields) == nil {
			m.ResponseFormatType = fields["type"]
			m.ResponseFormatSchemaFingerprint = fields["fingerprint"]
		}
	}
	if p := m.Parameters["reasoning"]; p.Presence == "value" {
		var fields map[string]string
		if json.Unmarshal(p.Value, &fields) == nil {
			m.ReasoningEffort = fields["effort"]
		}
	}
	if p := m.Parameters["reasoning_effort"]; p.Presence == "value" {
		_ = json.Unmarshal(p.Value, &m.ReasoningEffort)
	}
	// 清单上限只省略完整项，不截断JSON。角色与输入总数仍保留。
	for {
		encoded, _ := json.Marshal(m)
		if len(encoded) <= 60<<10 || len(m.Items) == 0 {
			break
		}
		m.Items = m.Items[:len(m.Items)-1]
		m.OmittedItems++
	}
	if h.call.a.config.Mode == OnError {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&value) == nil {
			h.omitted = omittedFields(value)
			filtered, changed := sanitizeValue(value, h.call.a.secrets, 0)
			encoded, err := json.MarshalIndent(filtered, "", "  ")
			if err == nil && int64(len(encoded)) <= h.call.a.config.MaxRequestBytes {
				h.body = encoded
				h.redacted = changed
				m.BodyCaptureState = "captured"
			} else {
				m.CaptureReason = "saved_body_limit"
				m.BodyCaptureState = "unavailable"
			}
		}
	}
}

func (h *HTTPAttempt) hasMapping(path string) bool {
	for _, paths := range h.mappings {
		for _, p := range paths {
			if path == p {
				return true
			}
		}
	}
	return false
}

func (h *HTTPAttempt) safeParameter(name string, v json.RawMessage) json.RawMessage {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(v))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	var safe any
	switch name {
	case "temperature", "top_p", "max_tokens", "max_completion_tokens", "max_output_tokens":
		if n, ok := value.(json.Number); ok {
			safe = n
		}
	case "stream", "parallel_tool_calls", "store":
		if b, ok := value.(bool); ok {
			safe = b
		}
	case "tool_choice":
		if s, ok := value.(string); ok {
			safe = safeText(s, 64, h.call.a.secrets)
		} else if obj, ok := value.(map[string]any); ok {
			out := map[string]any{}
			for _, key := range []string{"type", "name", "mode"} {
				if s, ok := obj[key].(string); ok {
					out[key] = safeText(s, 128, h.call.a.secrets)
				}
			}
			if f, ok := obj["function"].(map[string]any); ok {
				if s, ok := f["name"].(string); ok {
					out["name"] = safeText(s, 128, h.call.a.secrets)
				}
			}
			safe = out
		}
	case "reasoning_effort":
		if s, ok := value.(string); ok {
			safe = safeText(s, 64, h.call.a.secrets)
		}
	case "response_format", "reasoning", "text":
		if obj, ok := value.(map[string]any); ok {
			if name == "text" {
				obj, _ = obj["format"].(map[string]any)
			}
			out := map[string]any{"fingerprint": h.call.a.fingerprint(v)}
			for _, key := range []string{"type", "effort"} {
				if s, ok := obj[key].(string); ok {
					out[key] = safeText(s, 64, h.call.a.secrets)
				}
			}
			safe = out
		}
	case "include":
		if items, ok := value.([]any); ok {
			var out []string
			for _, item := range items {
				if s, ok := item.(string); ok && len(out) < 16 {
					out = append(out, safeText(s, 128, h.call.a.secrets))
				}
			}
			safe = out
		}
	}
	if safe == nil {
		safe = map[string]string{"value_type": "unexpected", "fingerprint": h.call.a.fingerprint(v)}
	}
	b, _ := json.Marshal(safe)
	return b
}

func changes(current, previous *Metadata) (added, removed, changed []string, complete bool) {
	before := map[string]string{}
	after := map[string]string{}
	if previous != nil {
		for _, item := range previous.Items {
			before[item.JSONPath] = item.Fingerprint
		}
	}
	for _, item := range current.Items {
		after[item.JSONPath] = item.Fingerprint
		if old, ok := before[item.JSONPath]; !ok {
			added = append(added, item.JSONPath)
		} else if old != item.Fingerprint {
			changed = append(changed, item.JSONPath)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	complete = previous != nil && current.CaptureState == "complete" && previous.CaptureState == "complete" && current.OmittedItems == 0 && previous.OmittedItems == 0 && len(added) <= 128 && len(removed) <= 128 && len(changed) <= 128
	limit := func(values []string) []string {
		if len(values) > 128 {
			return values[:128]
		}
		return values
	}
	return limit(added), limit(removed), limit(changed), complete
}

func safeError(info ErrorInfo, secrets []string) ErrorInfo {
	clean := func(value *string) *string {
		if value == nil {
			return nil
		}
		s := safeText(*value, 256, secrets)
		return &s
	}
	info.Kind = safeText(info.Kind, 64, secrets)
	info.Code = clean(info.Code)
	info.Type = clean(info.Type)
	info.Param = clean(info.Param)
	info.RequestID = clean(info.RequestID)
	info.RequestIDSource = safeText(info.RequestIDSource, 64, secrets)
	info.SafeSummary = safeText(info.SafeSummary, 512, secrets)
	if info.HTTPStatus != nil {
		v := *info.HTTPStatus
		info.HTTPStatus = &v
	}
	states := map[string]string{}
	values := map[string]json.RawMessage{}
	for _, key := range []string{"code", "type", "param", "request_id", "message"} {
		if p, ok := info.FieldStates[key]; ok {
			switch p {
			case "absent", "null", "value", "unknown":
				states[key] = p
			}
		}
		if v, ok := info.FieldValues[key]; ok {
			var val any
			if json.Unmarshal(v, &val) == nil {
				filtered, _ := sanitizeValue(val, secrets, 0)
				b, _ := json.Marshal(filtered)
				if len(b) <= 512 {
					values[key] = b
				}
			}
		}
	}
	info.FieldStates, info.FieldValues = states, values
	return info
}

func cacheKey(identity Identity, m Metadata) string {
	if m.Model == "" || m.EndpointID == "" {
		return ""
	}
	return strings.Join([]string{identity.AgentID, m.Provider, m.Protocol, m.EndpointID, m.Model}, "\x00")
}
