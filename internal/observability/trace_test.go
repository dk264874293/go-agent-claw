package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceUsesRunIDAndReportsFailures(t *testing.T) {
	dir := t.TempDir()
	span := &Span{Name: "Agent.Run", Attributes: map[string]any{"RunID": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	first, err := ExportTraceToFile(span, dir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportTraceToFile(span, dir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil || first == second {
		t.Fatalf("Trace 覆盖: %q %q %v", first, second, err)
	}
	if _, err := ExportTraceToFile(span, dir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("重复 RunID 覆盖文件")
	}
	if _, err := ExportTraceToFile(span, dir, "../escape"); err == nil {
		t.Fatal("非法 RunID 被接受")
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportTraceToFile(span, blocked, "cccccccccccccccccccccccccccccccc"); err == nil {
		t.Fatal("目录失败被忽略")
	}
	if !strings.Contains(first, "trace_aaaaaaaa") {
		t.Fatal(first)
	}
}
