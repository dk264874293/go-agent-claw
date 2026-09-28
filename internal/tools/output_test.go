package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestBoundedOutputDiscardsExcess(t *testing.T) {
	var buffer boundedOutput
	chunk := []byte(strings.Repeat("中", 1000))
	for i := 0; i < 1000; i++ {
		n, err := buffer.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("排空输出失败: n=%d err=%v", n, err)
		}
	}
	output, truncated := buffer.snapshot()
	if !truncated || len(buffer.data) != maxOutputBytes || len(output) > maxOutputBytes+100 || !utf8.ValidString(output) {
		t.Fatalf("缓冲未遵守上限或字符边界: bytes=%d output=%d truncated=%v", len(buffer.data), len(output), truncated)
	}
}

func TestReadFileBoundedAndRooted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte(strings.Repeat("字", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("不能读取"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.Register(NewReadFileTool(dir))
	for _, tc := range []struct {
		path string
		fail bool
	}{
		{"large", false}, {"missing", true}, {"../outside", true}, {outside, true}, {"escape", true}, {"", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"path": tc.path})
			result := r.Execute(context.Background(), schema.ToolCall{ID: "read", Name: "read_file", Arguments: args})
			if result.IsError != tc.fail {
				t.Fatalf("读取错误状态不正确: %+v", result)
			}
			if !tc.fail && (!result.Truncated || len(result.Output) > maxOutputBytes+100 || !utf8.ValidString(result.Output)) {
				t.Fatalf("读取未有界截断: %+v", result.ToolMetadata)
			}
		})
	}
}
