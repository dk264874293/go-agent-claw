package engine

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// objectFields 只定位顶层字段，不重新编码业务数值或未知字段；重复键不能可靠投影。
type rawObjectField struct {
	key               string
	start, value, end int
}

func objectFields(raw []byte) ([]rawObjectField, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields, seen := []rawObjectField{}, map[string]bool{}
	previous := int(decoder.InputOffset())
	for decoder.More() {
		start := previous
		for start < len(raw) && (raw[start] == ',' || raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\t' || raw[start] == '\r') {
			start++
		}
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, false
		}
		seen[key] = true
		value := int(decoder.InputOffset())
		for value < len(raw) && (raw[value] == ':' || raw[value] == ' ' || raw[value] == '\n' || raw[value] == '\t' || raw[value] == '\r') {
			value++
		}
		var body json.RawMessage
		if decoder.Decode(&body) != nil {
			return nil, false
		}
		previous = int(decoder.InputOffset())
		fields = append(fields, rawObjectField{key, start, value, previous})
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	return fields, true
}

func removeWorkNotesMetadata(raw []byte) ([]byte, int64, bool) {
	fields, ok := objectFields(raw)
	if !ok {
		return raw, 0, false
	}
	for i, field := range fields {
		if field.key != "_work_notes" {
			continue
		}
		start, end := field.start, field.end
		if i+1 < len(fields) {
			end = fields[i+1].start
		} else if i > 0 {
			start = fields[i-1].end
		}
		result := append(append([]byte(nil), raw[:start]...), raw[end:]...)
		if !json.Valid(result) {
			return raw, 0, false
		}
		return result, int64(end - start), true
	}
	return raw, 0, true
}

// projectWorkNotesMetadata 只改请求副本。Responses 按原始调用身份及参数逐字匹配，
// 不能同步时保留整条调用；不透明推理项、未知项和原顺序保持不变。
func projectWorkNotesMetadata(messages []schema.Message, consumed func(schema.ToolCall) bool) ([]schema.Message, int64, int64) {
	result := append([]schema.Message(nil), messages...)
	var removed, retained int64
	for i, message := range messages {
		if message.Role != schema.RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		result[i].ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		result[i].ResponseItems = append([]json.RawMessage(nil), message.ResponseItems...)
		for j, call := range message.ToolCalls {
			clean, size, reliable := removeWorkNotesMetadata(call.Arguments)
			if !reliable {
				if bytes.Contains(call.Arguments, []byte(`"_work_notes"`)) {
					retained += int64(len(call.Arguments))
				}
				continue
			}
			if size == 0 {
				continue
			}
			if !consumed(call) {
				retained += size
				continue
			}
			matches := 0
			for _, other := range message.ToolCalls {
				if other.ID == call.ID {
					matches++
				}
			}
			if matches != 1 {
				retained += size
				continue
			}
			if len(message.ResponseItems) > 0 {
				itemIndex, updated, ok := matchingCarrierResponseItem(message.ResponseItems, call, clean)
				if !ok {
					retained += size
					continue
				}
				result[i].ResponseItems[itemIndex] = updated
			}
			result[i].ToolCalls[j].Arguments = clean
			removed += size
		}
	}
	return result, removed, retained
}

func matchingCarrierResponseItem(items []json.RawMessage, call schema.ToolCall, clean []byte) (int, json.RawMessage, bool) {
	match := -1
	var replacement json.RawMessage
	for i, raw := range items {
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return 0, nil, false
		}
		if header.Type != "function_call" {
			continue
		}
		var identity struct {
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(raw, &identity) != nil {
			return 0, nil, false
		}
		if identity.CallID != call.ID {
			continue
		}
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return 0, nil, false
		}
		if match >= 0 || item.Name != call.Name || item.Arguments != string(call.Arguments) {
			return 0, nil, false
		}
		fields, ok := objectFields(raw)
		if !ok {
			return 0, nil, false
		}
		for _, field := range fields {
			if field.key == "arguments" {
				encoded, _ := json.Marshal(string(clean))
				replacement = append(append(append([]byte(nil), raw[:field.value]...), encoded...), raw[field.end:]...)
				match = i
			}
		}
	}
	return match, replacement, match >= 0
}
