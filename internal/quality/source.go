package quality

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"
)

type sourceError struct{ text string }

func (e sourceError) Error() string                { return e.text }
func unavailable(format string, args ...any) error { return sourceError{fmt.Sprintf(format, args...)} }
func sourceUnavailable(err error) bool             { var e sourceError; return errors.As(err, &e) }

// Resolve 不读取文件，也不从工具显示文字重建来源。Lookup必须拒绝失效和越权证据。
func Resolve(sel Selector, lookup Lookup) (Resolved, error) {
	var out Resolved
	if len(sel.EvidenceIDs) == 0 || len(sel.EvidenceIDs) > 128 || sel.ByteStart < 0 || sel.ByteEnd <= sel.ByteStart || sel.ByteEnd-sel.ByteStart > MaxJSONBytes {
		return out, fmt.Errorf("来源选择需要1至128个证据、非空合法字节范围，且最多%d字节", MaxJSONBytes)
	}
	if lookup == nil {
		return out, unavailable("来源查询不可用")
	}
	parts := make([]Source, 0, len(sel.EvidenceIDs))
	seen := map[string]bool{}
	for _, id := range sel.EvidenceIDs {
		if id == "" || seen[id] {
			return out, fmt.Errorf("来源证据ID为空或重复")
		}
		seen[id] = true
		s, err := lookup(id)
		if err != nil {
			return out, unavailable("证据%s不可用：%v", id, err)
		}
		if s.ID != id || s.Path == "" || s.Version == "" || s.ByteStart < 0 || s.ByteEnd < s.ByteStart || s.TotalBytes < s.ByteEnd || int64(len(s.Content)) != s.ByteEnd-s.ByteStart {
			return out, unavailable("证据%s的实际读取区间无效", id)
		}
		if len(s.Content) > MaxJSONBytes || !utf8.Valid(s.Content) {
			return out, unavailable("证据%s过大或不是有效UTF-8", id)
		}
		if len(parts) > 0 && (s.Path != parts[0].Path || s.Version != parts[0].Version || s.TotalBytes != parts[0].TotalBytes) {
			return out, unavailable("来源片段不属于同一文件和版本")
		}
		parts = append(parts, s)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].ByteStart < parts[j].ByteStart })
	for i, a := range parts {
		for _, b := range parts[i+1:] {
			start, end := max(a.ByteStart, b.ByteStart), min(a.ByteEnd, b.ByteEnd)
			if start < end && !bytes.Equal(a.Content[start-a.ByteStart:end-a.ByteStart], b.Content[start-b.ByteStart:end-b.ByteStart]) {
				return out, unavailable("同版本来源的重叠字节不一致")
			}
		}
	}
	data := make([]byte, 0, sel.ByteEnd-sel.ByteStart)
	cursor := sel.ByteStart
	for _, s := range parts {
		if s.ByteEnd <= cursor || s.ByteStart >= sel.ByteEnd {
			continue
		}
		if s.ByteStart > cursor {
			return out, unavailable("来源在字节%d处存在未读缺口", cursor)
		}
		end := min(s.ByteEnd, sel.ByteEnd)
		data = append(data, s.Content[cursor-s.ByteStart:end-s.ByteStart]...)
		cursor = end
	}
	if cursor != sel.ByteEnd {
		return out, unavailable("来源未覆盖完整主体，缺少[%d,%d)", cursor, sel.ByteEnd)
	}
	if !utf8.Valid(data) {
		return out, unavailable("选择范围切断UTF-8字符")
	}
	out.SourceRef = SourceRef{EvidenceIDs: append([]string(nil), sel.EvidenceIDs...), Path: parts[0].Path, Version: parts[0].Version, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), ByteStart: sel.ByteStart, ByteEnd: sel.ByteEnd}
	out.Content = data
	for _, s := range parts {
		if sel.ByteStart > s.ByteStart && sel.ByteStart <= s.ByteEnd {
			b := s.Content[sel.ByteStart-s.ByteStart-1]
			out.Before = &b
		}
		if sel.ByteEnd >= s.ByteStart && sel.ByteEnd < s.ByteEnd {
			b := s.Content[sel.ByteEnd-s.ByteStart]
			out.After = &b
		}
		if sel.ByteStart >= s.ByteStart && sel.ByteStart < s.ByteEnd && s.FirstLine > 0 {
			out.FirstLine = s.FirstLine + bytes.Count(s.Content[:sel.ByteStart-s.ByteStart], []byte{'\n'})
		}
		if s.Boundary != nil {
			out.StartsLine = out.StartsLine || (sel.ByteStart == s.ByteStart && s.Boundary.StartsLine)
			out.EndsLine = out.EndsLine || (sel.ByteEnd == s.ByteEnd && s.Boundary.EndsLine)
		}
	}
	out.StartsLine = out.StartsLine || sel.ByteStart == 0 || (out.Before != nil && *out.Before == '\n')
	out.EndsLine = out.EndsLine || sel.ByteEnd == parts[0].TotalBytes || (len(data) > 0 && data[len(data)-1] == '\n')
	if out.FirstLine > 0 {
		out.LastLine = out.FirstLine + bytes.Count(data, []byte{'\n'})
		if data[len(data)-1] == '\n' {
			out.LastLine--
		}
	}
	if sel.RequireCompleteLine && (!out.StartsLine || !out.EndsLine) {
		return Resolved{}, unavailable("读取边界不足以证明完整行；请补充完整读取依据")
	}
	return out, nil
}
