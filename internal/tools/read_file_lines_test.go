package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestLineReadUsesInclusiveSourceLinesAndRawReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, source, want, body string
		start, end, byteStart    int
		firstLine, lastLine      int
	}{
		{name: "中文及无末尾换行", source: "甲\n乙丙\n丁", start: 2, end: 3, byteStart: 4, firstLine: 2, lastLine: 3, want: "乙丙\n丁", body: "2 | 乙丙\n3 | 丁"},
		{name: "CRLF不改写", source: "甲\r\n乙\r\n丙\r\n", start: 2, end: 2, byteStart: 5, firstLine: 2, lastLine: 2, want: "乙\r\n", body: "2 | 乙\r\n"},
		{name: "空行", source: "一\n\n三\n", start: 2, end: 2, byteStart: 4, firstLine: 2, lastLine: 2, want: "\n", body: "2 | \n"},
		{name: "空文件", start: 1, end: 2},
		{name: "EOF空页", source: "一\n二\n", start: 4, end: 4, byteStart: 8},
		{name: "末尾换行没有额外行", source: "一\n", start: 2, end: 3, byteStart: 4},
		{name: "结束超出EOF", source: "一\n二", start: 2, end: 100, byteStart: 4, firstLine: 2, lastLine: 2, want: "二", body: "2 | 二"},
		{name: "跨扫描块定位", source: strings.Repeat("甲\n", 15000), start: 9999, end: 10001, byteStart: 4 * 9998, firstLine: 9999, lastLine: 10001, want: "甲\n甲\n甲\n", body: "9999 | 甲\n10000 | 甲\n10001 | 甲\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, registry := numberedReadFixture(t, tc.source)
			result := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "start_line": tc.start, "end_line": tc.end})
			if result.Status != schema.ToolSucceeded || result.Read == nil {
				t.Fatalf("按行读取失败: %+v", result)
			}
			_, body, ok := strings.Cut(result.Output, "\n\n")
			read := result.Read
			if !ok || body != tc.body || string(result.ReadContent) != tc.want || read.ByteStart != int64(tc.byteStart) || read.ByteEnd != int64(tc.byteStart+len(tc.want)) || read.FirstLine != tc.firstLine || read.LastLine != tc.lastLine || read.TotalBytes != int64(len(tc.source)) || read.Version != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.source))) {
				t.Fatalf("按行结果、源字节或回执错误: 输出=%q 回执=%+v 源字节=%q", result.Output, read, result.ReadContent)
			}
			if strings.Contains(string(result.ReadContent), " | ") {
				t.Fatal("结构化原文含展示前缀")
			}
		})
	}
}

func TestLineReadExplicitRawAndByteModeDefaultsRemainCompatible(t *testing.T) {
	ctx, registry := numberedReadFixture(t, "甲\n乙\n丙")
	result := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "start_line": 2, "end_line": 2, "line_numbers": false})
	if result.IsError || !strings.HasSuffix(result.Output, "\n\n乙\n") || string(result.ReadContent) != "乙\n" {
		t.Fatalf("行模式未尊重显式原始显示: %+v", result)
	}
	result = runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "offset": 4, "limit": 4})
	if result.IsError || !strings.HasSuffix(result.Output, "\n\n乙\n") || string(result.ReadContent) != "乙\n" {
		t.Fatalf("字节模式默认显示或源字节改变: %+v", result)
	}
}

func TestLineReadRejectsInvalidAndMixedCoordinates(t *testing.T) {
	ctx, registry := numberedReadFixture(t, "甲\n乙\n")
	for _, raw := range []string{
		`{"path":"source.go","start_line":1}`,
		`{"path":"source.go","end_line":2}`,
		`{"path":"source.go","start_line":0,"end_line":2}`,
		`{"path":"source.go","start_line":3,"end_line":2}`,
		`{"path":"source.go","start_line":1,"end_line":2,"offset":0}`,
		`{"path":"source.go","start_line":1,"end_line":2,"limit":8000}`,
		`{"path":"source.go","start_line":1.5,"end_line":2}`,
		`{"path":"source.go","start_line":1,"end_line":null}`,
		`{"path":"source.go","start_line":1,"end_line":2,"line_numbers":null}`,
	} {
		result := registry.Execute(ctx, schema.ToolCall{ID: "invalid", Name: "read_file", Arguments: json.RawMessage(raw)})
		if result.Status != schema.ToolRejected || result.ErrorKind != "参数错误" || result.Read != nil || len(result.ReadContent) != 0 {
			t.Fatalf("非法范围没有在执行前拒绝: 参数=%s 结果=%+v", raw, result)
		}
	}
}

func TestReadRejectsOffsetInsideUTF8WithCorrection(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		for _, tc := range []struct {
			source       string
			offset, next int
		}{
			{source: "甲乙", offset: 1, next: 3},
			{source: "甲乙", offset: 2, next: 3},
			{source: "😀尾", offset: 1, next: 4},
			{source: "😀", offset: 3, next: 4},
			{source: strings.Repeat("a", 32767) + "😀尾", offset: 32768, next: 32771},
		} {
			ctx, registry := numberedReadFixture(t, tc.source)
			result := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "offset": tc.offset, "limit": 1, "line_numbers": numbered})
			if result.Status != schema.ToolRejected || result.Read != nil || !strings.Contains(result.Output, "UTF-8 字符中间") || !strings.Contains(result.Output, fmt.Sprintf("offset %d（下一完整字符起点）", tc.next)) || strings.Contains(result.Output, "�") {
				t.Fatalf("字符内起点未拒绝或纠正位置不准: %+v", result)
			}
			result = runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "offset": tc.next, "line_numbers": numbered})
			if result.IsError || !utf8.ValidString(result.Output) {
				t.Fatalf("提示的纠正位置仍不可读取: %+v", result)
			}
		}
	}
}

func TestLineReadTruncationPagesPreserveSourceAndContinueLongLine(t *testing.T) {
	for _, selected := range []string{strings.Repeat("中", 6000) + "\n尾\r\n", strings.Repeat("\n", 4000) + "末行"} {
		source := "跳过\n" + selected
		ctx, registry := numberedReadFixture(t, source)
		result := runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "start_line": 2, "end_line": 5000})
		if result.IsError || result.Read == nil || !strings.Contains(result.Output, "受输出上限截断") {
			t.Fatalf("行模式未显示截断: %+v", result)
		}
		var rebuilt bytes.Buffer
		for page := 0; ; page++ {
			_, body, _ := strings.Cut(result.Output, "\n\n")
			read := result.Read
			if len(body) > maxReadBytes || !utf8.Valid(result.ReadContent) || !bytes.Equal(result.ReadContent, []byte(source[read.ByteStart:read.ByteEnd])) || read.ByteEnd <= read.ByteStart {
				t.Fatalf("显示上限或实际消费范围错误: %+v", read)
			}
			rebuilt.Write(result.ReadContent)
			if read.ByteEnd == int64(len(source)) {
				break
			}
			if page > 50 {
				t.Fatal("分页未取得进展")
			}
			previous := read
			result = runTestTool(ctx, registry, "read_file", map[string]any{"path": "source.go", "offset": previous.ByteEnd, "line_numbers": true})
			if result.IsError || result.Read == nil {
				t.Fatalf("按字节续读失败: %+v", result)
			}
			if source[previous.ByteEnd-1] != '\n' && !strings.Contains(result.Output, "（续行） | ") {
				t.Fatal("超长行续读缺少续行标记")
			}
		}
		if rebuilt.String() != selected {
			t.Fatal("行读取和字节续读拼接改变了原始材料")
		}
	}
}

func TestRawReadContentIsRuntimeOnly(t *testing.T) {
	result := schema.ToolResult{ReadContent: []byte("仅供证据落盘的正文"), Read: &schema.ReadReceipt{EvidenceID: "E1", ArtifactID: "artifact"}}
	encoded, err := json.Marshal(result)
	if err != nil || bytes.Contains(encoded, []byte("仅供证据落盘的正文")) || bytes.Contains(encoded, []byte("ReadContent")) || !bytes.Contains(encoded, []byte(`"evidence_id":"E1"`)) || !bytes.Contains(encoded, []byte(`"artifact_id":"artifact"`)) {
		t.Fatalf("原文重复序列化或引用身份未保留: %s / %v", encoded, err)
	}
}

func TestUTF8StreamCheckHandlesSplitRunesAndInvalidSource(t *testing.T) {
	for _, source := range [][]byte{[]byte("中文😀\n末尾"), {0xff, 'a'}, {0xe4, 0xb8}, {0xe4, 'x', 0xad}} {
		for step := 1; step < 6; step++ {
			check := &utf8StreamCheck{}
			for offset := 0; offset < len(source); offset += step {
				_, _ = check.Write(source[offset:min(offset+step, len(source))])
			}
			if check.valid() != utf8.Valid(source) {
				t.Fatalf("跨块 UTF-8 校验不一致，步长=%d 源字节=%x", step, source)
			}
		}
	}
}
