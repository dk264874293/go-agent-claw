package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestDiscoveryReceiptUsesBoundedWalkMetadata(t *testing.T) {
	dir := t.TempDir()
	discoveryFile(t, dir, "a.md", []byte("甲\n"))
	discoveryFile(t, dir, "nested/b.md", []byte("版本二"))
	discoveryFile(t, dir, "ignored.txt", []byte("不属于清单"))
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir, Mode: ModeReadOnly})
	registry := NewRegistry()
	registry.Register(NewFindFilesTool(dir))
	result := runTestTool(ctx, registry, "find_files", map[string]any{"path": ".", "name_glob": "*.md"})
	if result.IsError || result.Discovery == nil {
		t.Fatalf("缺少结构化发现: %+v", result)
	}
	r := result.Discovery
	if len(r.ManifestID) != 32 || r.Path != "." || r.NameGlob != "*.md" || r.Truncated || r.Unreadable != 0 || len(r.Files) != 2 || r.Files[0].Path != "a.md" || r.Files[0].Bytes != 4 || r.Files[1].Path != "nested/b.md" || r.Files[1].Bytes != 9 {
		t.Fatalf("发现元数据错误: %+v", r)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "不属于清单") || !strings.Contains(string(encoded), `"discovery"`) {
		t.Fatal("回执遗漏或读取了未请求的正文")
	}
	second := runTestTool(ctx, registry, "find_files", map[string]any{"path": ".", "name_glob": "*.md", "limit": 1})
	if second.Discovery == nil || !second.Discovery.Truncated || len(second.Discovery.Files) != 1 || second.Discovery.ManifestID == r.ManifestID {
		t.Fatalf("限额或清单身份错误: %+v", second)
	}
}

func TestDiscoveryReceiptMarksUnreadableAndScope(t *testing.T) {
	dir := t.TempDir()
	discoveryFile(t, dir, "visible/a.md", []byte("a"))
	discoveryFile(t, dir, "restricted/b.md", []byte("b"))
	tool := NewFindFilesTool(dir)
	tool.openDirectory = func(ctx context.Context, root *os.Root, path string) (findDirectory, error) {
		if path == "restricted" {
			return nil, os.ErrPermission
		}
		return openFindDirectory(ctx, root, path)
	}
	registry := NewRegistry()
	registry.Register(tool)
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	result := runTestTool(ctx, registry, "find_files", map[string]any{"name_glob": "*.md"})
	if result.IsError || result.Discovery == nil || result.Discovery.Unreadable != 1 || result.Discovery.Truncated || len(result.Discovery.Files) != 1 {
		t.Fatalf("不可读范围被当成完整: %+v", result)
	}
	ctx = WithScope(ctx, ExecutionScope{WorkDir: dir, AllowedPaths: []string{"visible"}})
	result = runTestTool(ctx, registry, "find_files", map[string]any{"name_glob": "*.md"})
	if result.IsError || result.Discovery.Unreadable != 0 || len(result.Discovery.Files) != 1 || result.Discovery.Files[0].Path != "visible/a.md" {
		t.Fatalf("发现越过权限边界: %+v", result)
	}
}

func TestReadLinesDistinctSchemaAndSharedSource(t *testing.T) {
	ctx, registry := numberedReadFixture(t, "甲\r\n乙丙\r\n丁")
	registry.Register(NewReadLinesTool(ScopeFromContext(ctx).WorkDir))
	result := runTestTool(ctx, registry, "read_lines", map[string]any{"path": "source.go", "start_line": 2, "end_line": 2})
	if result.IsError || result.Read == nil || string(result.ReadContent) != "乙丙\r\n" || !strings.HasSuffix(result.Output, "2 | 乙丙\r\n") || !strings.Contains(result.Output, "EOF: false") || result.Read.ByteStart != 5 || result.Read.ByteEnd != 13 {
		t.Fatalf("按行工具没有复用准确源范围: %+v", result)
	}
	legacy := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "start_line": 2, "end_line": 2})
	if legacy.IsError || legacy.Read.ByteStart != result.Read.ByteStart || legacy.Read.ByteEnd != result.Read.ByteEnd || string(legacy.ReadContent) != string(result.ReadContent) {
		t.Fatal("旧合法行调用不兼容")
	}
	for _, def := range registry.VisibleTools(ctx) {
		properties := def.InputSchema.(map[string]any)["properties"].(map[string]any)
		if def.Name == "read_file" && (properties["start_line"] != nil || properties["end_line"] != nil) {
			t.Fatal("字节工具仍展示混合坐标")
		}
		if def.Name == "read_lines" && (properties["offset"] != nil || properties["limit"] != nil || properties["start_line"] == nil || properties["end_line"] == nil) {
			t.Fatal("行工具Schema错误")
		}
	}
	for _, raw := range []string{`{"path":"source.go","start_line":1}`, `{"path":"source.go","start_line":1,"end_line":2,"offset":0}`, `{"path":"source.go","start_line":1,"end_line":2,"limit":8000}`} {
		bad := registry.Execute(ctx, schema.ToolCall{ID: "invalid", Name: "read_lines", Arguments: json.RawMessage(raw)})
		if bad.Status != schema.ToolRejected || bad.Read != nil || !strings.Contains(bad.Output, "read_lines") || !strings.Contains(bad.Output, "read_file") {
			t.Fatalf("非法行参数缺少纠正示例: %+v", bad)
		}
	}
}

func TestReadLinesRangeEndIsNotFileEOF(t *testing.T) {
	source := strings.Repeat("甲\n", 426)
	ctx, registry := numberedReadFixture(t, source)
	registry.Register(NewReadLinesTool(ScopeFromContext(ctx).WorkDir))
	first := runTestTool(ctx, registry, "read_lines", map[string]any{"path": "source.go", "start_line": 1, "end_line": 400})
	if first.IsError || first.Read.ByteEnd != 1600 || first.Read.TotalBytes != 1704 || !strings.Contains(first.Output, "EOF: false") || !strings.Contains(first.Output, "下一段 offset: 1600") {
		t.Fatalf("请求末行被误报文件EOF: %+v", first)
	}
	tail := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "offset": first.Read.ByteEnd, "line_numbers": true})
	if tail.IsError || tail.Read.FirstLine != 401 || tail.Read.LastLine != 426 || !strings.Contains(tail.Output, "EOF: true") {
		t.Fatalf("尾部续读错误: %+v", tail)
	}
}

func TestReadReviewMetadataAndObservationWhitelist(t *testing.T) {
	ctx, registry := numberedReadFixture(t, "实际原文\n")
	registry.Register(NewReadLinesTool(ScopeFromContext(ctx).WorkDir))
	for _, name := range []string{"read_file", "read_lines"} {
		args := map[string]any{"path": "source.go", "review": map[string]string{"target_id": "action-original", "question": "确认原文中是否还有未核对的条件"}}
		if name == "read_lines" {
			args["start_line"] = 1
			args["end_line"] = 1
		}
		result := runTestTool(ctx, registry, name, args)
		if result.IsError || result.Read == nil || result.Read.Review == nil || result.Read.Review.TargetID != "action-original" {
			t.Fatalf("复核目标未结构化传递: %+v", result)
		}
		raw, _ := json.Marshal(args)
		observed := observationForTool(schema.ToolCall{ID: "review", Name: name, Arguments: raw})
		if name == "read_lines" && (observed.StartLine != 1 || observed.EndLine != 1) {
			t.Fatalf("按行请求没有记录结构化范围: %+v", observed)
		}
		payload, _ := json.Marshal(observed)
		if strings.Contains(string(payload), "未核对的条件") || strings.Contains(string(payload), "action-original") {
			t.Fatalf("复核自由文字泄漏到观测: %s", payload)
		}
	}
	for _, review := range []any{nil, map[string]string{"target_id": "a"}, map[string]string{"target_id": " ", "question": "核对"}, map[string]string{"target_id": "a", "question": strings.Repeat("中", 201)}} {
		bad := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "review": review})
		if bad.Status != schema.ToolRejected || bad.Read != nil {
			t.Fatalf("非法复核对象进入读取: %+v", bad)
		}
	}
}

func TestReportObservationOnlyRecordsValidatedIdentityAndCount(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	call := schema.ToolCall{ID: "report", Name: "read_report", Arguments: json.RawMessage(`{"report_id":"` + id + `","finding_ids":["F1","F2"],"content":"不能存的正文"}`)}
	p := observationForTool(call)
	raw, _ := json.Marshal(p)
	if p.ReportID != id || p.FindingCount != 2 || strings.Contains(string(raw), "不能存的正文") || strings.Contains(string(raw), "F1") {
		t.Fatalf("报告观测越过白名单: %s", raw)
	}
	call.Arguments = json.RawMessage(`{"report_id":"sk-private-secret","finding_ids":[]}`)
	p = observationForTool(call)
	raw, _ = json.Marshal(p)
	if p.ReportID != "" || strings.Contains(string(raw), "private-secret") {
		t.Fatalf("无效报告ID被保存: %s", raw)
	}
}
