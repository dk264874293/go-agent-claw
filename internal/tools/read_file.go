/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 09:53:23
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 09:56:43
 * @FilePath: /go-agent-claw/internal/tools/read_file.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/tools/read_file.go
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

const maxReadBytes = DefaultReadPageBytes

type ReadFileTool struct{ workDir string }

func NewReadFileTool(workDir string) *ReadFileTool { return &ReadFileTool{workDir: workDir} }

func (t *ReadFileTool) Name() string { return "read_file" }

func definition(name, description string, props map[string]any, required ...string) schema.ToolDefinition {
	inputSchema := map[string]any{
		"type": "object", "properties": props, "additionalProperties": false,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	return schema.ToolDefinition{Name: name, Description: description, InputSchema: inputSchema}
}

func stringParam(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intParam(description string, min, max int) map[string]any {
	param := map[string]any{"type": "integer", "description": description, "minimum": min}
	if max > 0 {
		param["maximum"] = max
	}
	return param
}

func (t *ReadFileTool) Definition() schema.ToolDefinition {
	return t.DefinitionFor(context.Background())
}

func (t *ReadFileTool) DefinitionFor(ctx context.Context) schema.ToolDefinition {
	pageBytes := ReadPageBytes(ctx)
	return definition(t.Name(), "按字节分页读取工作区文件并返回完整文件版本。offset 不是行号；按行定位请用 read_lines。首次阅读需要引用时启用 line_numbers，不要为补编号全文再读。", map[string]any{
		"path":         stringParam("工作区相对路径，例如 cmd/claw/main.go"),
		"offset":       intParam("源文件字节起点，默认 0，继续读取使用上次返回的下一段起点", 0, 0),
		"limit":        intParam(fmt.Sprintf("读取的最大源文件字节数，默认 %d；编号正文显示上限同为 %d 字节", pageBytes, pageBytes), 1, pageBytes),
		"line_numbers": map[string]any{"type": "boolean", "description": "显示源文件绝对行号，默认 false；首次阅读需引用时设 true，行中起始标注续行"},
		"review":       readReviewParam(),
	}, "path")
}

type readFileArgs struct {
	Path        string             `json:"path"`
	Offset      int64              `json:"offset"`
	Limit       *int               `json:"limit"`
	LineNumbers *bool              `json:"line_numbers"`
	StartLine   *int               `json:"start_line"`
	EndLine     *int               `json:"end_line"`
	Review      *schema.ReadReview `json:"review"`
}

func readReviewParam() map[string]any {
	return map[string]any{"type": "object", "description": "确需重复核对时填写既有动作、证据或发现ID与具体问题；不会绕过权限或预算", "properties": map[string]any{"target_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "本任务已有动作、证据或发现身份"}, "question": map[string]any{"type": "string", "minLength": 1, "maxLength": 600, "description": "本次要核对的具体问题"}}, "required": []string{"target_id", "question"}, "additionalProperties": false}
}

func validateReadReview(review *schema.ReadReview) error {
	if review == nil {
		return nil
	}
	if strings.TrimSpace(review.TargetID) == "" || len(review.TargetID) > 128 || strings.TrimSpace(review.Question) == "" || len(review.Question) > 600 {
		return reject("参数错误", fmt.Errorf("review 需要有效 target_id（1至128字节）和具体 question（1至600字节）；例如 review={\"target_id\":\"已有动作ID\",\"question\":\"核对默认值是否为5\"}"))
	}
	return nil
}

func parseRead(ctx context.Context, args json.RawMessage) (input readFileArgs, err error) {
	if err := ValidateReadPageBytes(ScopeFromContext(ctx).ReadPageBytes); err != nil {
		return input, reject("参数错误", err)
	}
	if err := decodeArgs(args, &input, "path"); err != nil {
		return input, err
	}
	if err := validateReadReview(input.Review); err != nil {
		return input, err
	}
	input.Path, err = filePath(ctx, input.Path, false)
	if err != nil {
		return input, err
	}
	if input.Offset < 0 || (input.Limit != nil && (*input.Limit < 1 || *input.Limit > ReadPageBytes(ctx))) {
		return input, reject("参数错误", fmt.Errorf("offset 必须非负，limit 必须在 1 至 %d 之间", ReadPageBytes(ctx)))
	}
	if (input.StartLine == nil) != (input.EndLine == nil) {
		return input, reject("参数错误", fmt.Errorf("start_line 与 end_line 必须同时提供，行号从 1 开始且两端包含；请用 read_lines，例如 {\"path\":\"a.md\",\"start_line\":1,\"end_line\":20}"))
	}
	if input.StartLine != nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(args, &fields) // decodeArgs 已完成对象与字段类型校验。
		if fields["offset"] != nil || fields["limit"] != nil {
			return input, reject("参数错误", fmt.Errorf("行范围 start_line/end_line 不能与字节参数 offset/limit 混用；按行请用 read_lines {\"path\":\"a.md\",\"start_line\":1,\"end_line\":20}，续读请用 read_file {\"path\":\"a.md\",\"offset\":返回的下一段起点,\"line_numbers\":true}"))
		}
		if *input.StartLine < 1 || *input.EndLine < *input.StartLine {
			return input, reject("参数错误", fmt.Errorf("start_line 必须至少为 1，end_line 必须不小于 start_line（两端包含）"))
		}
	}
	return input, nil
}

func (t *ReadFileTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseRead(ctx, args)
	return err
}

type byteWindow struct {
	data         []byte
	offset       int64
	total        int64
	prefixLines  int
	beforeOffset byte
}

// lineWindow 按行定位，同时只保留最多一个输出窗口；不将整个文件读入内存。
type lineWindow struct {
	data                    []byte
	from, through, line     int
	total, offset, rangeEnd int64
	found                   bool
}

func (w *lineWindow) Write(p []byte) (int, error) {
	for start := 0; start < len(p); {
		end := len(p)
		newline := bytes.IndexByte(p[start:], '\n')
		if newline >= 0 {
			end = start + newline + 1
		}
		if w.line >= w.from && w.line <= w.through {
			if !w.found {
				w.offset, w.found = w.total+int64(start), true
			}
			count := min(end-start, cap(w.data)-len(w.data))
			w.data = append(w.data, p[start:start+count]...)
			w.rangeEnd = w.total + int64(end)
		}
		if newline >= 0 {
			w.line++
		}
		start = end
	}
	w.total += int64(len(p))
	return len(p), nil
}

// utf8StreamCheck 跨 io.Copy 分块验证全文，最多暂存一个未完成字符。
type utf8StreamCheck struct {
	tail    []byte
	invalid bool
}

func (v *utf8StreamCheck) Write(p []byte) (int, error) {
	if v.invalid {
		return len(p), nil
	}
	data := p
	if len(v.tail) > 0 {
		data = append(v.tail, p...)
	}
	v.tail = nil
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			v.tail = append([]byte(nil), data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			v.invalid = true
			break
		}
		data = data[size:]
	}
	return len(p), nil
}

func (v *utf8StreamCheck) valid() bool { return !v.invalid && len(v.tail) == 0 }

func (w *byteWindow) Write(p []byte) (int, error) {
	start := int(min(int64(len(p)), max(int64(0), w.offset-w.total)))
	if start > 0 {
		w.prefixLines += bytes.Count(p[:start], []byte{'\n'})
		w.beforeOffset = p[start-1]
	}
	if start < len(p) && len(w.data) < cap(w.data) {
		count := min(len(p)-start, cap(w.data)-len(w.data))
		w.data = append(w.data, p[start:start+count]...)
	}
	w.total += int64(len(p))
	return len(p), nil
}

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.executeRead(ctx, args)
	return output, err
}

func (t *ReadFileTool) executeRead(ctx context.Context, args json.RawMessage) (string, *schema.ReadReceipt, error) {
	output, read, _, err := t.executeReadSource(ctx, args)
	return output, read, err
}

func (t *ReadFileTool) executeReadSource(ctx context.Context, args json.RawMessage) (string, *schema.ReadReceipt, []byte, error) {
	input, err := parseRead(ctx, args)
	if err != nil {
		return "", nil, nil, err
	}
	root, err := openRoot(ctx, t.workDir)
	if err != nil {
		return "", nil, nil, err
	}
	defer root.Close()
	file, _, err := openRegular(ctx, root, input.Path)
	if err != nil {
		return "", nil, nil, err
	}
	defer file.Close()

	limit := ReadPageBytes(ctx)
	if input.Limit != nil {
		limit = *input.Limit
	}
	// 一次遍历同时计算摘要和收集窗口，内存占用由片段长度决定。
	window := byteWindow{data: make([]byte, 0, limit+utf8.UTFMax), offset: input.Offset}
	var selected *lineWindow
	var collector io.Writer = &window
	if input.StartLine != nil {
		selected = &lineWindow{data: make([]byte, 0, limit+utf8.UTFMax), from: *input.StartLine, through: *input.EndLine, line: 1}
		collector = selected
	}
	hash := sha256.New()
	check := &utf8StreamCheck{}
	if _, err := io.Copy(io.MultiWriter(hash, collector, check), contextReader{ctx, file}); err != nil {
		return "", nil, nil, err
	}
	if selected != nil {
		window.data, window.total = selected.data, selected.total
		window.offset, window.prefixLines = selected.offset, selected.from-1
		if !selected.found {
			window.offset = selected.total
		}
	}
	if window.offset > window.total {
		return "", nil, nil, reject("参数错误", fmt.Errorf("offset 超过文件长度 %d 字节", window.total))
	}
	if check.valid() && len(window.data) > 0 && !utf8.RuneStart(window.data[0]) {
		next := 0
		for next < len(window.data) && !utf8.RuneStart(window.data[next]) {
			next++
		}
		return "", nil, nil, reject("参数错误", fmt.Errorf("offset %d 位于 UTF-8 字符中间，请改用 offset %d（下一完整字符起点），或使用 start_line/end_line", window.offset, window.offset+int64(next)))
	}
	if len(window.data) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(window.data[end]) {
			end--
		}
		if end == 0 {
			return "", nil, nil, reject("参数错误", fmt.Errorf("片段上限无法容纳完整字符，请增大 limit 或按返回的 offset 读取"))
		}
		window.data = window.data[:end]
	}
	var body string
	numbered := selected != nil
	if input.LineNumbers != nil {
		numbered = *input.LineNumbers
	}
	if numbered {
		var consumed int
		body, consumed = numberedContentWithin(window.data, window.prefixLines+1, selected == nil && window.offset > 0 && window.beforeOffset != '\n', ReadPageBytes(ctx))
		window.data = window.data[:consumed]
	} else {
		body = string(bytes.ToValidUTF8(window.data, []byte("�")))
	}
	end := window.offset + int64(len(window.data))
	firstLine, lastLine := 0, 0
	if len(window.data) > 0 {
		firstLine = window.prefixLines + 1
		lastLine = firstLine + bytes.Count(window.data, []byte{'\n'})
		if window.data[len(window.data)-1] == '\n' {
			lastLine--
		}
	}
	output := fmt.Sprintf("文件: %s\n版本: %x\n字节范围: [%d, %d)，共 %d 字节\n行号范围: [%d, %d]\n",
		input.Path, hash.Sum(nil), window.offset, end, window.total, firstLine, lastLine)
	if selected != nil {
		if selected.found && end < selected.rangeEnd {
			output += fmt.Sprintf("请求行范围: [%d, %d]，受输出上限截断；请单独使用下一段 offset（字节）续读\n", selected.from, selected.through)
			if len(window.data) > 0 && window.data[len(window.data)-1] != '\n' {
				output += fmt.Sprintf("末尾第 %d 行未完整读取\n", lastLine)
			}
		} else {
			output += fmt.Sprintf("请求行范围: [%d, %d]，已读取至请求末行或 EOF\n", selected.from, selected.through)
		}
	}
	if end < window.total {
		output += fmt.Sprintf("下一段 offset: %d\n", end)
	}
	output += fmt.Sprintf("EOF: %t\n", end >= window.total)
	return output + "\n" + body, &schema.ReadReceipt{
		Path: input.Path, Version: fmt.Sprintf("%x", hash.Sum(nil)),
		ByteStart: window.offset, ByteEnd: end, FirstLine: firstLine, LastLine: lastLine, TotalBytes: window.total, Review: input.Review,
		LineBoundary: &schema.ReadBoundary{StartsLine: selected != nil || window.offset == 0 || window.beforeOffset == '\n', EndsLine: end == window.total || len(window.data) > 0 && window.data[len(window.data)-1] == '\n'},
	}, window.data, nil
}

// numberedContent 的显示预算包含编号，但 consumed 始终以源文件字节计数。
// 仅在下一字符也能写入时添加编号，避免截断后出现没有源码的空标签。
func numberedContent(data []byte, line int, continued bool) (body string, consumed int) {
	return numberedContentWithin(data, line, continued, DefaultReadPageBytes)
}

func numberedContentWithin(data []byte, line int, continued bool, pageBytes int) (body string, consumed int) {
	var out strings.Builder
	lineStart := true
	for consumed < len(data) {
		prefix := ""
		if lineStart {
			if continued {
				prefix = fmt.Sprintf("%d（续行） | ", line)
			} else {
				prefix = fmt.Sprintf("%d | ", line)
			}
		}
		r, size := utf8.DecodeRune(data[consumed:])
		text := string(data[consumed : consumed+size])
		if r == utf8.RuneError && size == 1 {
			text = "�"
		}
		if out.Len()+len(prefix)+len(text) > pageBytes {
			break
		}
		out.WriteString(prefix)
		out.WriteString(text)
		consumed += size
		lineStart = r == '\n'
		if lineStart {
			line++
		}
		continued = false
	}
	return out.String(), consumed
}
