package tools

import (
	"bytes"
	"context"
	"io"
	"os"
	"unicode/utf8"
)

const maxSearchFileBytes = 4 * 1024 * 1024
const maxSearchLineBytes = 256 * 1024

type searchSkipCounts struct{ binary, encoding, tooLarge, unreadable, longLines int }

func (s *searchSkipCounts) add(reason string) {
	switch reason {
	case "binary":
		s.binary++
	case "encoding":
		s.encoding++
	case "large":
		s.tooLarge++
	case "unreadable":
		s.unreadable++
	}
}

// readSearchText 先完整识别有界内容，任何匹配输出都不能先于二进制和编码检查。
func readSearchText(ctx context.Context, root *os.Root, path string) ([]byte, string, error) {
	file, info, err := openRegular(ctx, root, path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "unreadable", nil
	}
	defer file.Close()
	if info.Size() > maxSearchFileBytes {
		return nil, "large", nil
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxSearchFileBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "unreadable", nil
	}
	if len(data) > maxSearchFileBytes {
		return nil, "large", nil
	}
	if unsupportedBOM(data) {
		return nil, "encoding", nil
	}
	if knownBinary(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, "binary", nil
	}
	if !utf8.Valid(data) {
		return nil, "encoding", nil
	}
	return data, "", nil
}

func unsupportedBOM(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) || bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff})
}

func knownBinary(data []byte) bool {
	for _, prefix := range [][]byte{
		{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		{0xff, 0xd8, 0xff}, []byte("GIF87a"), []byte("GIF89a"), []byte("%PDF-"),
		{'P', 'K', 3, 4}, {'P', 'K', 5, 6}, {'P', 'K', 7, 8}, {0x7f, 'E', 'L', 'F'},
		{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe}, {0xbe, 0xba, 0xfe, 0xca},
	} {
		if bytes.HasPrefix(data, prefix) {
			return true
		}
	}
	return false
}

func searchSnippet(text string, index int) string {
	start := max(0, index-150)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	end := min(len(text), start+400)
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	snippet := text[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet
}
