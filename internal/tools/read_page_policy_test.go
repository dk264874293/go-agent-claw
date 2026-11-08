package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestReadPagePolicySchemaAndAdmission(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte(strings.Repeat("a", 20000)), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	reader := NewReadFileTool(dir)
	r.Register(reader)
	r.Register(NewReadLinesTool(dir))
	for _, configured := range []int{0, 8000, 16000} {
		ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir, ReadPageBytes: configured})
		page := ReadPageBytes(ctx)
		for _, def := range r.VisibleTools(ctx) {
			if def.Name != "read_file" {
				if !strings.Contains(def.Description, fmt.Sprint(page)) {
					t.Fatalf("按行工具未展示当前档位: %+v", def)
				}
				continue
			}
			props := def.InputSchema.(map[string]any)["properties"].(map[string]any)
			if props["limit"].(map[string]any)["maximum"] != page {
				t.Fatalf("Schema 与执行档位不一致: %+v", def)
			}
		}
		for _, limit := range []int{1, page, page + 1} {
			z := runTestTool(ctx, r, "read_file", map[string]any{"path": "source.md", "limit": limit})
			if limit > page {
				if z.Status != schema.ToolRejected || z.Read != nil {
					t.Fatalf("超档请求未拒绝: %+v", z)
				}
			} else if z.IsError || z.Read == nil || z.Read.ByteEnd != int64(limit) || len(z.ReadContent) != limit {
				t.Fatalf("读取长度和档位不一致: %+v", z.Read)
			}
		}
		z := runTestTool(ctx, r, "read_file", map[string]any{"path": "source.md"})
		if z.IsError || len(z.ReadContent) != page {
			t.Fatalf("默认页长错误: 配置 %d、返回 %d", configured, len(z.ReadContent))
		}
	}
	defaultProps := reader.Definition().InputSchema.(map[string]any)["properties"].(map[string]any)
	if defaultProps["limit"].(map[string]any)["maximum"] != 8000 {
		t.Fatal("无上下文定义改变了旧默认")
	}
	for _, invalid := range []int{-1, 7999, 8192, 16001} {
		ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir, ReadPageBytes: invalid})
		z := runTestTool(ctx, r, "read_file", map[string]any{"path": "source.md"})
		if z.Status != schema.ToolRejected || z.Read != nil {
			t.Fatalf("非法配置未拒绝: %d %+v", invalid, z)
		}
	}
}

func TestReadPagePolicyUTF8RoundTrip(t *testing.T) {
	for _, page := range []int{8000, 16000} {
		for fixture, source := range []string{
			strings.Repeat("中文\r\n\n", 5000) + "末尾",
			strings.Repeat("长", 12000),
			"",
		} {
			t.Run(fmt.Sprintf("%d/%d", page, fixture), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir, ReadPageBytes: page})
				r := NewRegistry()
				r.Register(NewReadFileTool(dir))
				r.Register(NewReadLinesTool(dir))
				var all []byte
				var offset int64
				for n := 0; ; n++ {
					name := "read_file"
					args := map[string]any{"path": "source.md", "offset": offset, "line_numbers": true}
					if n == 0 {
						name, args = "read_lines", map[string]any{"path": "source.md", "start_line": 1, "end_line": 20000}
					}
					z := runTestTool(ctx, r, name, args)
					if z.IsError || z.Read == nil || z.Read.ByteStart != offset {
						t.Fatalf("分页失败: %+v", z)
					}
					_, body, ok := strings.Cut(z.Output, "\n\n")
					if !ok || len(body) > page || !utf8.ValidString(body) || !utf8.Valid(z.ReadContent) || !bytes.Equal(z.ReadContent, []byte(source[z.Read.ByteStart:z.Read.ByteEnd])) {
						t.Fatalf("UTF-8、编号显示或源范围错误: %+v", z.Read)
					}
					all = append(all, z.ReadContent...)
					if z.Read.ByteEnd == z.Read.TotalBytes {
						break
					}
					if z.Read.ByteEnd <= offset || !strings.Contains(z.Output, fmt.Sprintf("下一段 offset: %d", z.Read.ByteEnd)) {
						t.Fatal("分页没有前进或续读位置错误")
					}
					offset = z.Read.ByteEnd
				}
				if string(all) != source {
					t.Fatal("完整分页后原文不一致")
				}
				if source != "" {
					z := runTestTool(ctx, r, "read_file", map[string]any{"path": "source.md", "offset": 1})
					if z.Status != schema.ToolRejected || !strings.Contains(z.Output, "UTF-8") {
						t.Fatal("字符内部 offset 未明确拒绝")
					}
				}
			})
		}
	}
}
