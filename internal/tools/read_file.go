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

const maxReadBytes = 8000

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
	return definition(t.Name(), "读取工作区文件的一段内容并返回完整文件版本。需要引用行号时启用 line_numbers；编辑只使用源码，不含显示前缀。", map[string]any{
		"path":         stringParam("工作区相对路径，例如 cmd/claw/main.go"),
		"offset":       intParam("源文件字节起点，默认 0，继续读取使用上次返回的下一段起点", 0, 0),
		"limit":        intParam("读取的最大源文件字节数，默认 8000；编号正文另有 8000 字节显示上限", 1, maxReadBytes),
		"line_numbers": map[string]any{"type": "boolean", "description": "显示源文件绝对行号，默认 false；行中起始标注续行"},
	}, "path")
}

type readFileArgs struct {
	Path        string `json:"path"`
	Offset      int64  `json:"offset"`
	Limit       *int   `json:"limit"`
	LineNumbers bool   `json:"line_numbers"`
}

func parseRead(ctx context.Context, args json.RawMessage) (input readFileArgs, err error) {
	if err := decodeArgs(args, &input, "path"); err != nil {
		return input, err
	}
	input.Path, err = filePath(ctx, input.Path, false)
	if err != nil {
		return input, err
	}
	if input.Offset < 0 || (input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxReadBytes)) {
		return input, reject("参数错误", fmt.Errorf("offset 必须非负，limit 必须在 1 至 %d 之间", maxReadBytes))
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
	input, err := parseRead(ctx, args)
	if err != nil {
		return "", err
	}
	root, err := openRoot(ctx, t.workDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, _, err := openRegular(ctx, root, input.Path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	limit := maxReadBytes
	if input.Limit != nil {
		limit = *input.Limit
	}
	// 一次遍历同时计算摘要和收集窗口，内存占用由片段长度决定。
	window := byteWindow{data: make([]byte, 0, limit+1), offset: input.Offset}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(hash, &window), contextReader{ctx, file}); err != nil {
		return "", err
	}
	if input.Offset > window.total {
		return "", reject("参数错误", fmt.Errorf("offset 超过文件长度 %d 字节", window.total))
	}
	if len(window.data) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(window.data[end]) {
			end--
		}
		if end == 0 {
			return "", reject("参数错误", fmt.Errorf("片段上限无法容纳完整字符，请增大 limit 或按返回的 offset 读取"))
		}
		window.data = window.data[:end]
	}
	var body string
	if input.LineNumbers {
		var consumed int
		body, consumed = numberedContent(window.data, window.prefixLines+1, input.Offset > 0 && window.beforeOffset != '\n')
		window.data = window.data[:consumed]
	} else {
		body = string(bytes.ToValidUTF8(window.data, []byte("�")))
	}
	end := input.Offset + int64(len(window.data))
	firstLine, lastLine := 0, 0
	if len(window.data) > 0 {
		firstLine = window.prefixLines + 1
		lastLine = firstLine + bytes.Count(window.data, []byte{'\n'})
		if window.data[len(window.data)-1] == '\n' {
			lastLine--
		}
	}
	output := fmt.Sprintf("文件: %s\n版本: %x\n字节范围: [%d, %d)，共 %d 字节\n行号范围: [%d, %d]\n",
		input.Path, hash.Sum(nil), input.Offset, end, window.total, firstLine, lastLine)
	if end < window.total {
		output += fmt.Sprintf("下一段 offset: %d\n", end)
	}
	return output + "\n" + body, nil
}

// numberedContent 的显示预算包含编号，但 consumed 始终以源文件字节计数。
// 仅在下一字符也能写入时添加编号，避免截断后出现没有源码的空标签。
func numberedContent(data []byte, line int, continued bool) (body string, consumed int) {
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
		if out.Len()+len(prefix)+len(text) > maxReadBytes {
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
