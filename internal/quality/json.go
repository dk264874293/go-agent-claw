package quality

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 在math/big分配巨大十进制幂之前约束指数，不能先计算再检查位数。
func jsonRational(text string) (*big.Rat, error) {
	if len(text) == 0 || len(text) > MaxLiteral {
		return nil, fmt.Errorf("JSON数字字面量过长")
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.ParseInt(text[i+1:], 10, 32)
		if err != nil || exponent > 1200 || exponent < -1200 {
			return nil, fmt.Errorf("JSON数字指数超过安全资源上限")
		}
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, fmt.Errorf("JSON数字无效")
	}
	return bounded(r)
}

// strictJSON 通过Token递归保留Number并在解码后的键上排重，包括转义拼写相同的键。
func strictJSON(raw []byte) (any, error) {
	if len(raw) > MaxJSONBytes {
		return nil, fmt.Errorf("JSON主体超过%d字节", MaxJSONBytes)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("JSON不是有效UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := jsonValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON主体后有额外内容")
	}
	return v, nil
}
func jsonValue(d *json.Decoder, depth int) (any, error) {
	if depth > MaxJSONDepth {
		return nil, fmt.Errorf("JSON深度超过%d", MaxJSONDepth)
	}
	t, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON语法无效：%w", err)
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			if depth >= MaxJSONDepth {
				return nil, fmt.Errorf("JSON深度超过%d", MaxJSONDepth)
			}
			out := map[string]any{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return nil, e
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("JSON键不是字符串")
				}
				if _, exists := out[name]; exists {
					return nil, fmt.Errorf("JSON含重复键%q", name)
				}
				value, e := jsonValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				out[name] = value
			}
			close, e := d.Token()
			if e != nil || close != json.Delim('}') {
				return nil, fmt.Errorf("JSON对象未闭合")
			}
			return out, nil
		case '[':
			if depth >= MaxJSONDepth {
				return nil, fmt.Errorf("JSON深度超过%d", MaxJSONDepth)
			}
			out := []any{}
			for d.More() {
				value, e := jsonValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				out = append(out, value)
			}
			close, e := d.Token()
			if e != nil || close != json.Delim(']') {
				return nil, fmt.Errorf("JSON数组未闭合")
			}
			return out, nil
		default:
			return nil, fmt.Errorf("不支持的JSON边界")
		}
	}
	if n, ok := t.(json.Number); ok {
		if _, err := jsonRational(n.String()); err != nil {
			return nil, err
		}
	}
	return t, nil
}
func pointer(v any, path string) (any, bool, error) {
	if len(path) > 4096 {
		return nil, false, fmt.Errorf("JSONPointer过长")
	}
	if path == "" {
		return v, true, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false, fmt.Errorf("JSONPointer必须为空或以/开始")
	}
	parts := strings.Split(path[1:], "/")
	if len(parts) > MaxJSONDepth {
		return nil, false, fmt.Errorf("JSONPointer过深")
	}
	decoded := make([]string, 0, len(parts))
	for _, encoded := range parts {
		var key strings.Builder
		for i := 0; i < len(encoded); i++ {
			if encoded[i] != '~' {
				key.WriteByte(encoded[i])
				continue
			}
			if i+1 == len(encoded) || (encoded[i+1] != '0' && encoded[i+1] != '1') {
				return nil, false, fmt.Errorf("JSONPointer的~转义无效")
			}
			i++
			if encoded[i] == '0' {
				key.WriteByte('~')
			} else {
				key.WriteByte('/')
			}
		}
		decoded = append(decoded, key.String())
	}
	for _, part := range decoded {
		switch o := v.(type) {
		case map[string]any:
			var exists bool
			v, exists = o[part]
			if !exists {
				return nil, false, nil
			}
		case []any:
			if part == "" || len(part) > 1 && part[0] == '0' {
				return nil, false, fmt.Errorf("JSONPointer数组下标无效")
			}
			for _, c := range []byte(part) {
				if !isDigit(c) {
					return nil, false, fmt.Errorf("JSONPointer数组下标无效")
				}
			}
			index, err := strconv.ParseUint(part, 10, 63)
			if err != nil {
				return nil, false, fmt.Errorf("JSONPointer数组下标无效")
			}
			if index >= uint64(len(o)) {
				return nil, false, nil
			}
			v = o[index]
		default:
			return nil, false, nil
		}
	}
	return v, true, nil
}
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return "unknown"
}
func jsonEqual(a, b any) bool {
	if jsonType(a) != jsonType(b) {
		return false
	}
	switch x := a.(type) {
	case nil:
		return true
	case bool:
		return x == b.(bool)
	case string:
		return x == b.(string)
	case json.Number:
		ar, aerr := jsonRational(string(x))
		br, berr := jsonRational(string(b.(json.Number)))
		return aerr == nil && berr == nil && ar.Cmp(br) == 0
	case []any:
		y := b.([]any)
		if len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !jsonEqual(v, y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y := b.(map[string]any)
		if len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, exists := y[k]
			if !exists || !jsonEqual(v, other) {
				return false
			}
		}
		return true
	}
	return false
}
