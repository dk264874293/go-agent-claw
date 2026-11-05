package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func numberedReadFixture(t *testing.T, source string) (context.Context, Registry) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.Register(NewReadFileTool(dir))
	return WithScope(context.Background(), ExecutionScope{WorkDir: dir}), r
}

func TestNumberedReadDisplaysAbsoluteSourceLines(t *testing.T) {
	for _, tc := range []struct {
		name, source, body, lines string
		offset, limit             int
	}{
		{name: "空文件", lines: "[0, 0]"},
		{name: "中文空行及无末尾换行", source: "甲\n\n乙丙", body: "1 | 甲\n2 | \n3 | 乙丙", lines: "[1, 3]"},
		{name: "末尾换行不产生额外编号", source: "甲\n乙\n", body: "1 | 甲\n2 | 乙\n", lines: "[1, 2]"},
		{name: "正常行起点", source: "甲\n乙丙\n丁", offset: 4, body: "2 | 乙丙\n3 | 丁", lines: "[2, 3]"},
		{name: "行内起点", source: "甲\n乙丙\n丁", offset: 7, body: "2（续行） | 丙\n3 | 丁", lines: "[2, 3]"},
		{name: "按源字节限制并保留完整中文", source: "甲\n乙丙\n丁", offset: 4, limit: 4, body: "2 | 乙", lines: "[2, 2]"},
		{name: "文件末尾", source: "甲\n", offset: 4, lines: "[0, 0]"},
		{name: "Windows换行", source: "甲\r\n乙", body: "1 | 甲\r\n2 | 乙", lines: "[1, 2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r := numberedReadFixture(t, tc.source)
			args := map[string]any{"path": "source.go", "offset": tc.offset, "line_numbers": true}
			if tc.limit > 0 {
				args["limit"] = tc.limit
			}
			result := runTestTool(ctx, r, "read_file", args)
			if result.IsError {
				t.Fatalf("编号读取失败: %+v", result)
			}
			header, body, ok := strings.Cut(result.Output, "\n\n")
			if !ok || body != tc.body || !strings.Contains(header, "行号范围: "+tc.lines) {
				t.Fatalf("编号内容或源行号错误: %q", result.Output)
			}
			if !strings.Contains(header, fmt.Sprintf("版本: %x", sha256.Sum256([]byte(tc.source)))) {
				t.Fatalf("编号改变了源文件版本: %q", header)
			}
			if tc.limit == 4 && !strings.Contains(header, "下一段 offset: 7") {
				t.Fatalf("下一段没有按源字节计数: %q", header)
			}
		})
	}
}

func TestNumberedReadPagesRoundTripWithinDisplayBudget(t *testing.T) {
	for _, source := range []string{
		strings.Repeat("甲乙丙\n\n", 1700) + "最后一行",
		strings.Repeat("长", 15000) + "\n尾行\n",
		strings.Repeat("\n", 18000),
	} {
		ctx, r := numberedReadFixture(t, source)
		var rebuilt strings.Builder
		prefix := regexp.MustCompile(`^[0-9]+(?:（续行）)? \| `)
		offset, pages := 0, 0
		for offset < len(source) {
			result := runTestTool(ctx, r, "read_file", map[string]any{
				"path": "source.go", "offset": offset, "line_numbers": true,
			})
			if result.IsError {
				t.Fatalf("分页读取失败: %+v", result)
			}
			header, body, ok := strings.Cut(result.Output, "\n\n")
			if !ok || len(body) > maxReadBytes || !utf8.ValidString(body) {
				t.Fatalf("显示字节数或 UTF-8 错误: 字节数 %d", len(body))
			}
			line := strings.Count(source[:offset], "\n") + 1
			wantPrefix := fmt.Sprintf("%d | ", line)
			if offset > 0 && source[offset-1] != '\n' {
				wantPrefix = fmt.Sprintf("%d（续行） | ", line)
			}
			if !strings.HasPrefix(body, wantPrefix) {
				t.Fatalf("分页首行不是源文件绝对位置 %q: %q", wantPrefix, body[:min(len(body), 50)])
			}
			start, end, total := -1, -1, -1
			for _, line := range strings.Split(header, "\n") {
				if strings.HasPrefix(line, "字节范围: ") {
					if _, err := fmt.Sscanf(line, "字节范围: [%d, %d)，共 %d 字节", &start, &end, &total); err != nil {
						t.Fatal(err)
					}
				}
			}
			if start != offset || end <= offset || end > len(source) || total != len(source) {
				t.Fatalf("源字节范围错误: %q", header)
			}
			var page strings.Builder
			for _, line := range strings.SplitAfter(body, "\n") {
				if line != "" && !prefix.MatchString(line) {
					t.Fatalf("显示行缺少编号: %q", line)
				}
				page.WriteString(prefix.ReplaceAllString(line, ""))
			}
			if page.String() != source[offset:end] {
				t.Fatal("编号、分页或截断改变了源文件内容")
			}
			if end < len(source) && !strings.HasSuffix(header, "下一段 offset: "+strconv.Itoa(end)) {
				t.Fatalf("下一段起点与返回字节不一致: %q", header)
			}
			rebuilt.WriteString(page.String())
			offset = end
			pages++
		}
		if rebuilt.String() != source || pages < 2 {
			t.Fatalf("跨页回读失败，页数 %d", pages)
		}
	}
}

func TestNumberedReadPreservesRawDefaultAndValidation(t *testing.T) {
	source := "甲\n乙丙\n丁"
	ctx, r := numberedReadFixture(t, source)
	args := map[string]any{"path": "source.go", "offset": 4, "limit": 4}
	defaultResult := runTestTool(ctx, r, "read_file", args)
	args["line_numbers"] = false
	rawResult := runTestTool(ctx, r, "read_file", args)
	want := fmt.Sprintf("文件: source.go\n版本: %x\n字节范围: [4, 7)，共 %d 字节\n行号范围: [2, 2]\n下一段 offset: 7\n\n乙", sha256.Sum256([]byte(source)), len(source))
	if defaultResult.IsError || rawResult.IsError || defaultResult.Output != want || rawResult.Output != want {
		t.Fatalf("默认原始读取格式发生变化: 默认 %+v，显式 false %+v", defaultResult, rawResult)
	}
	for _, invalid := range []any{"true", 1, nil} {
		args["line_numbers"] = invalid
		result := runTestTool(ctx, r, "read_file", args)
		if result.Status != schema.ToolRejected || result.ErrorKind != "参数错误" {
			t.Fatalf("非法编号参数未拒绝: %+v", result)
		}
	}
}
