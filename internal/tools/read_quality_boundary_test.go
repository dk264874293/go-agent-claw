package tools

import (
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestQualityReadBoundaryIsFromActualSourceWindow(t *testing.T) {
	for _, tc := range []struct {
		source     string
		args       map[string]any
		start, end bool
	}{
		{"甲\r\n乙\n丙", map[string]any{"offset": 0, "limit": 3}, true, false},
		{"甲\r\n乙\n丙", map[string]any{"offset": 5, "limit": 4}, true, true},
		{"abcdef\nnext", map[string]any{"offset": 2, "limit": 3}, false, false},
		{"abcdef\nnext", map[string]any{"offset": 2, "limit": 5}, false, true},
		{"甲\r\n乙\n丙", map[string]any{"start_line": 2, "end_line": 2}, true, true},
		{"abcdef\nnext", map[string]any{"offset": 7}, true, true},
		{"", map[string]any{}, true, true},
	} {
		ctx, r := numberedReadFixture(t, tc.source)
		tc.args["path"] = "source.go"
		z := runTestTool(ctx, r, "read_file", tc.args)
		if z.Status != schema.ToolSucceeded || z.Read == nil || z.Read.LineBoundary == nil || z.Read.LineBoundary.StartsLine != tc.start || z.Read.LineBoundary.EndsLine != tc.end {
			t.Fatalf("source=%q args=%+v receipt=%+v result=%+v", tc.source, tc.args, z.Read, z)
		}
	}
	// 按行工具同样使用源字节边界，展示编号不能让截断的长行冒充完整。
	ctx, r := numberedReadFixture(t, "甲\r\n乙\n丙")
	r.Register(NewReadLinesTool(ScopeFromContext(ctx).WorkDir))
	z := runTestTool(ctx, r, "read_lines", map[string]any{"path": "source.go", "start_line": 2, "end_line": 3})
	if z.Read == nil || z.Read.LineBoundary == nil || !z.Read.LineBoundary.StartsLine || !z.Read.LineBoundary.EndsLine {
		t.Fatal(z)
	}
}
