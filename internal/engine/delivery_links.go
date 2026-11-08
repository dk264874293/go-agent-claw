package engine

import (
	"encoding/json"
	"html"
	"strings"
	"unicode"
)

// DisplayRunFinalText 只生成展示副本，不修改模型原文或持久结果。
// 产物身份只取效果账本；本地链接的完整可见名称须唯一匹配确认产物。
func DisplayRunFinalText(result RunResult) string {
	text := result.FinalText
	if text == "" || json.Valid([]byte(strings.TrimSpace(text))) {
		return text
	}
	paths := map[string]bool{}
	for _, artifact := range artifactsFromDelivery(result.Delivery) {
		// 同一文件多次确认写入仍是同一个完整路径，不能把版本回执数当作同名歧义。
		paths[artifact.Path] = true
	}
	if len(paths) == 0 {
		return text
	}
	protected := deliveryCodeRegions(text)
	// 业务 JSON 可以出现在说明文字之后；完整对象/数组仍作为不透明正文保留。
	for start := 0; start < len(text); {
		end := deliveryLineEnd(text, start)
		pos := start
		for pos < end && (text[pos] == ' ' || text[pos] == '\t') {
			pos++
		}
		if pos < end && !protected[pos] && (text[pos] == '{' || text[pos] == '[') {
			decoder := json.NewDecoder(strings.NewReader(text[pos:]))
			var value json.RawMessage
			if decoder.Decode(&value) == nil {
				markDeliveryRegion(protected, pos, pos+int(decoder.InputOffset()))
			}
		}
		start = end
	}
	refs := map[string]deliveryReference{}
	for start := 0; start < len(text); {
		end := deliveryLineEnd(text, start)
		pos := start
		for pos < end && text[pos] == ' ' && pos-start < 3 {
			pos++
		}
		if pos < end && !protected[pos] && text[pos] == '[' {
			close, ok := deliveryLabelEnd(text, pos)
			if ok && close+1 < end && text[close+1] == ':' {
				// 定义自身保持原样；同名定义有歧义时不解析引用链接。
				markDeliveryRegion(protected, start, end)
				name := deliveryReferenceName(text[pos+1 : close])
				dest, valid := deliveryDestination(strings.TrimSpace(text[close+2 : end]))
				if name != "" && valid {
					ref := deliveryReference{destination: dest}
					if _, exists := refs[name]; exists {
						ref.ambiguous = true
					}
					refs[name] = ref
				}
			}
		}
		start = end
	}
	var out strings.Builder
	last := 0
	for i := 0; i < len(text); {
		if protected[i] || text[i] != '[' || deliveryEscaped(text, i) || (i > 0 && text[i-1] == '!' && !deliveryEscaped(text, i-1)) {
			i++
			continue
		}
		close, ok := deliveryLabelEnd(text, i)
		if !ok {
			i = deliveryLineEnd(text, i)
			continue
		}
		rawLabel := text[i+1 : close]
		label, plain := deliveryVisibleLabel(rawLabel)
		end := close + 1
		destination, parsed := "", false
		switch {
		case end < len(text) && text[end] == '(':
			var content string
			content, end, ok = deliveryLinkTarget(text, end)
			if ok {
				destination, parsed = deliveryDestination(content)
			}
		case end < len(text) && text[end] == '[':
			var refClose int
			refClose, ok = deliveryLabelEnd(text, end)
			if ok {
				refLabel := text[end+1 : refClose]
				if refLabel == "" {
					refLabel = rawLabel
				}
				ref, found := refs[deliveryReferenceName(refLabel)]
				destination, parsed = ref.destination, found && !ref.ambiguous
				end = refClose + 1
			}
		default:
			ref, found := refs[deliveryReferenceName(rawLabel)]
			destination, parsed = ref.destination, found && !ref.ambiguous
		}
		if !ok {
			// 不完整或嵌套语法不猜测，保留这一行。
			i = deliveryLineEnd(text, i)
			continue
		}
		blocked := false
		for p := i; p < end; p++ {
			blocked = blocked || protected[p]
		}
		if plain && paths[label] && parsed && !blocked && deliveryLocalDestination(destination) {
			out.WriteString(text[last:i])
			out.WriteString(rawLabel)
			last = end
		}
		i = end
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

type deliveryReference struct {
	destination string
	ambiguous   bool
}

func deliveryLineEnd(text string, start int) int {
	if n := strings.IndexByte(text[start:], '\n'); n >= 0 {
		return start + n + 1
	}
	return len(text)
}

func markDeliveryRegion(regions []bool, start, end int) {
	for i := start; i < end; i++ {
		regions[i] = true
	}
}

// 只标记代码区间；没有把握的 Markdown 保持原文，不重排整篇内容。
func deliveryCodeRegions(text string) []bool {
	regions := make([]bool, len(text))
	var fence byte
	fenceSize := 0
	for start := 0; start < len(text); {
		end := deliveryLineEnd(text, start)
		line := strings.TrimRight(text[start:end], "\r\n")
		trimmed := strings.TrimLeft(line, " \t")
		// 引用或列表中的围栏也保守视为代码，避免改写示例。
		marker := strings.TrimSpace(strings.TrimLeft(trimmed, "> "))
		if len(marker) > 2 && strings.ContainsRune("-*+", rune(marker[0])) && marker[1] == ' ' {
			marker = strings.TrimSpace(marker[2:])
		}
		for i := 0; i < len(marker) && i < 9; i++ {
			if marker[i] < '0' || marker[i] > '9' {
				if i > 0 && (marker[i] == '.' || marker[i] == ')') && i+1 < len(marker) && marker[i+1] == ' ' {
					marker = strings.TrimSpace(marker[i+2:])
				}
				break
			}
		}
		if fence != 0 {
			markDeliveryRegion(regions, start, end)
			n := deliveryRun(marker, 0, fence)
			if n >= fenceSize && strings.TrimSpace(marker[n:]) == "" {
				fence, fenceSize = 0, 0
			}
		} else if len(line)-len(trimmed) >= 4 || (len(line) > len(trimmed) && strings.ContainsRune(line[:len(line)-len(trimmed)], '\t')) {
			markDeliveryRegion(regions, start, end)
		} else if len(marker) >= 3 && (marker[0] == '`' || marker[0] == '~') && deliveryRun(marker, 0, marker[0]) >= 3 {
			fence = marker[0]
			fenceSize = deliveryRun(marker, 0, fence)
			markDeliveryRegion(regions, start, end)
		}
		start = end
	}
	for i := 0; i < len(text); {
		if regions[i] || text[i] != '`' || deliveryEscaped(text, i) {
			i++
			continue
		}
		n := deliveryRun(text, i, '`')
		end := i + n
		for end < len(text) {
			if regions[end] {
				break
			}
			if text[end] != '`' {
				end++
				continue
			}
			run := deliveryRun(text, end, '`')
			if run == n {
				markDeliveryRegion(regions, i, end+run)
				end += run
				break
			}
			end += run
		}
		i += n
	}
	return regions
}

func deliveryRun(text string, start int, char byte) int {
	end := start
	for end < len(text) && text[end] == char {
		end++
	}
	return end - start
}

func deliveryEscaped(text string, pos int) bool {
	n := 0
	for pos > 0 && text[pos-1] == '\\' {
		n++
		pos--
	}
	return n%2 != 0
}

func deliveryLabelEnd(text string, start int) (int, bool) {
	depth := 1
	for i := start + 1; i < len(text); i++ {
		if text[i] == '\n' || text[i] == '\r' {
			return 0, false
		}
		if deliveryEscaped(text, i) {
			continue
		}
		switch text[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func deliveryVisibleLabel(raw string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) && deliveryPunctuation(raw[i+1]) {
			i++
			out.WriteByte(raw[i])
			continue
		}
		if strings.ContainsRune("[]`*_~<>\r\n", rune(raw[i])) {
			return "", false
		}
		out.WriteByte(raw[i])
	}
	return html.UnescapeString(out.String()), true
}

func deliveryPunctuation(char byte) bool {
	return char >= '!' && char <= '/' || char >= ':' && char <= '@' || char >= '[' && char <= '`' || char >= '{' && char <= '~'
}

func deliveryReferenceName(raw string) string {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) && deliveryPunctuation(raw[i+1]) {
			i++
		}
		out.WriteByte(raw[i])
	}
	return strings.ToLower(strings.Join(strings.Fields(html.UnescapeString(out.String())), " "))
}

func deliveryLinkTarget(text string, start int) (string, int, bool) {
	depth := 1
	var quote byte
	angle := false
	for i := start + 1; i < len(text); i++ {
		char := text[i]
		if char == '\n' || char == '\r' {
			return "", 0, false
		}
		if deliveryEscaped(text, i) {
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if angle {
			if char == '>' {
				angle = false
			}
			continue
		}
		if char == '<' && strings.TrimSpace(text[start+1:i]) == "" {
			angle = true
			continue
		}
		if (char == '\'' || char == '"') && i > start+1 && (text[i-1] == ' ' || text[i-1] == '\t') {
			quote = char
			continue
		}
		switch char {
		case '(':
			depth++
			if depth > 32 {
				return "", 0, false
			}
		case ')':
			depth--
			if depth == 0 {
				return text[start+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

func deliveryDestination(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", false
	}
	dest, suffix := raw, ""
	if raw[0] == '<' {
		end := -1
		for i := 1; i < len(raw); i++ {
			if raw[i] == '>' && !deliveryEscaped(raw, i) {
				end = i
				break
			}
		}
		if end < 0 {
			return "", false
		}
		dest, suffix = raw[1:end], strings.TrimSpace(raw[end+1:])
	} else {
		for i := 0; i < len(raw); i++ {
			if (raw[i] == '\'' || raw[i] == '"') && !deliveryEscaped(raw, i) && i > 0 && (raw[i-1] == ' ' || raw[i-1] == '\t') {
				dest, suffix = strings.TrimSpace(raw[:i]), raw[i:]
				break
			}
		}
	}
	if suffix != "" {
		if len(suffix) < 2 || (suffix[0] != '\'' && suffix[0] != '"') || suffix[len(suffix)-1] != suffix[0] || deliveryEscaped(suffix, len(suffix)-1) {
			return "", false
		}
		for i := 1; i < len(suffix)-1; i++ {
			if suffix[i] == suffix[0] && !deliveryEscaped(suffix, i) {
				return "", false
			}
		}
	}
	if dest == "" || strings.ContainsAny(dest, "<>`\x00") {
		return "", false
	}
	var decoded strings.Builder
	for i := 0; i < len(dest); i++ {
		if dest[i] == '\\' && i+1 < len(dest) && deliveryPunctuation(dest[i+1]) {
			i++
		}
		decoded.WriteByte(dest[i])
	}
	return html.UnescapeString(decoded.String()), true
}

func deliveryLocalDestination(destination string) bool {
	if destination == "" || strings.HasPrefix(destination, "//") || strings.HasPrefix(destination, "#") || strings.HasPrefix(destination, "?") || strings.IndexFunc(destination, unicode.IsControl) >= 0 {
		return false
	}
	if colon := strings.IndexByte(destination, ':'); colon >= 0 {
		if colon == 1 && len(destination) > 2 && ((destination[0] >= 'a' && destination[0] <= 'z') || (destination[0] >= 'A' && destination[0] <= 'Z')) && (destination[2] == '/' || destination[2] == '\\') {
			return true
		}
		if !strings.ContainsAny(destination[:colon], "/\\ ") {
			return strings.EqualFold(destination[:colon], "file")
		}
	}
	return true
}
