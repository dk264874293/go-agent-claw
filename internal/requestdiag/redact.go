package requestdiag

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsk-[a-z0-9_*\-]{5,}`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9+/_.=\-]+`),
	regexp.MustCompile(`(?i)(?:["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|authorization)["']?\s*[:=]\s*)["']?[^\s,"';}]+["']?`),
	regexp.MustCompile(`https?://[^\s/@:]+:[^\s/@]+@`),
}

func secretsFromEnv() []string {
	var values []string
	for _, item := range os.Environ() {
		k, v, _ := strings.Cut(item, "=")
		k = strings.ToUpper(k)
		if len(v) >= 8 && (strings.Contains(k, "API_KEY") || strings.HasSuffix(k, "TOKEN") || strings.HasSuffix(k, "SECRET") || strings.HasSuffix(k, "PASSWORD")) {
			values = append(values, v)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return values
}
func redactText(s string, secrets []string) string {
	s = strings.ToValidUTF8(s, "�")
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
func safeText(s string, n int, secrets []string) string {
	s = redactText(s, secrets)
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func secretKey(k string) bool {
	k = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", ""), "_", ""))
	switch k {
	case "apikey", "authorization", "proxyauthorization", "cookie", "setcookie", "password", "secret", "token", "accesstoken", "refreshtoken":
		return true
	}
	return false
}
func privateKey(k string) bool {
	switch strings.ToLower(k) {
	case "reasoning", "reasoning_content", "thinking", "encrypted_content", "signature":
		return true
	}
	return false
}

// 只返回已知被省略的字段种类，不记录它们的值或业务路径。
func omittedFields(value any) []string {
	seen := map[string]bool{}
	var visit func(any, int)
	visit = func(v any, depth int) {
		if depth > 64 {
			seen["depth_limit"] = true
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if privateKey(key) {
					seen[strings.ToLower(key)] = true
					continue
				}
				visit(child, depth+1)
			}
		case []any:
			for _, child := range x {
				if obj, ok := child.(map[string]any); ok {
					kind, _ := obj["type"].(string)
					if kind == "reasoning" || kind == "thinking" || kind == "redacted_thinking" {
						seen["block:"+kind] = true
					}
				}
				visit(child, depth+1)
			}
		case string:
			trim := strings.TrimSpace(x)
			if len(trim) > 0 && len(trim) <= 1<<20 && (trim[0] == '{' || trim[0] == '[') {
				var nested any
				decoder := json.NewDecoder(strings.NewReader(trim))
				decoder.UseNumber()
				if decoder.Decode(&nested) == nil {
					visit(nested, depth+1)
				}
			}
		}
	}
	visit(value, 0)
	fields := make([]string, 0, len(seen))
	for key := range seen {
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}
func sanitizeValue(v any, secrets []string, depth int) (any, bool) {
	if depth > 64 {
		return "[OMITTED:DEPTH]", true
	}
	changed := false
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if privateKey(k) {
				changed = true
				continue
			}
			if secretKey(k) {
				out[k] = "[REDACTED]"
				changed = true
				continue
			}
			clean, did := sanitizeValue(val, secrets, depth+1)
			out[k] = clean
			changed = changed || did
		}
		return out, changed
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			if obj, ok := val.(map[string]any); ok {
				if kind, _ := obj["type"].(string); kind == "reasoning" || kind == "thinking" || kind == "redacted_thinking" {
					changed = true
					continue
				}
			}
			clean, did := sanitizeValue(val, secrets, depth+1)
			out = append(out, clean)
			changed = changed || did
		}
		return out, changed
	case string:
		clean := x
		// 工具arguments常将JSON作为字符串传递，先解析再脱敏，保持其字符串类型。
		trim := strings.TrimSpace(x)
		if len(trim) > 0 && len(trim) <= 1<<20 && (trim[0] == '{' || trim[0] == '[') {
			var inner any
			decoder := json.NewDecoder(bytes.NewReader([]byte(trim)))
			decoder.UseNumber()
			if decoder.Decode(&inner) == nil {
				filtered, did := sanitizeValue(inner, secrets, depth+1)
				if did {
					b, _ := json.Marshal(filtered)
					clean = string(b)
				}
			}
		}
		clean = redactText(clean, secrets)
		return clean, clean != x
	default:
		return v, false
	}
}
