package tools

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const maxOutputBytes = 8000

// boundedOutput 持续排空进程管道，只保留限定大小的前缀。
type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), maxOutputBytes-len(b.data))
	b.data = append(b.data, p[:n]...)
	b.truncated = b.truncated || n < len(p)
	return len(p), nil
}

func (b *boundedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return outputText(b.data, b.truncated), b.truncated
}

func outputText(data []byte, truncated bool) string {
	// 去掉被上限切开的尾部字符；二进制中的非法 UTF-8 不扩大结果大小。
	if truncated && len(data) > 0 {
		start := len(data) - 1
		for start > 0 && !utf8.RuneStart(data[start]) {
			start--
		}
		if !utf8.FullRune(data[start:]) {
			data = data[:start]
		}
	}
	text := strings.ToValidUTF8(string(data), "?")
	if truncated {
		text += "\n[工具输出超过 8000 字节，已截断]"
	}
	return text
}
