package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// ReadLinesTool 只暴露行坐标，实际读取、版本和权限检查复用 ReadFileTool。
type ReadLinesTool struct{ reader *ReadFileTool }

func NewReadLinesTool(workDir string) *ReadLinesTool {
	return &ReadLinesTool{reader: NewReadFileTool(workDir)}
}
func (*ReadLinesTool) Name() string { return "read_lines" }
func (t *ReadLinesTool) Definition() schema.ToolDefinition {
	return t.DefinitionFor(context.Background())
}
func (t *ReadLinesTool) DefinitionFor(ctx context.Context) schema.ToolDefinition {
	pageBytes := ReadPageBytes(ctx)
	return definition(t.Name(), fmt.Sprintf("按源文件绝对行号精确读取，默认显示编号；起止行均包含。超过%d字节会截断，续读使用返回的字节offset交给read_file并保留line_numbers=true。", pageBytes), map[string]any{
		"path":         stringParam("工作区相对路径"),
		"start_line":   intParam("起始行，从1开始", 1, 0),
		"end_line":     intParam(fmt.Sprintf("结束行，不小于start_line；两端包含，仍受%d字节输出上限限制", pageBytes), 1, 0),
		"line_numbers": map[string]any{"type": "boolean", "description": "显示源文件绝对行号，默认true"},
		"review":       readReviewParam(),
	}, "path", "start_line", "end_line")
}

func parseReadLines(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path        string             `json:"path"`
		StartLine   int                `json:"start_line"`
		EndLine     int                `json:"end_line"`
		LineNumbers *bool              `json:"line_numbers,omitempty"`
		Review      *schema.ReadReview `json:"review,omitempty"`
	}
	if err := decodeArgs(raw, &input, "path", "start_line", "end_line"); err != nil {
		return nil, reject("参数错误", fmt.Errorf("read_lines 只接受行坐标；示例 {\"path\":\"a.md\",\"start_line\":1,\"end_line\":20}，字节分页请用 read_file: %w", err))
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if _, err := parseRead(ctx, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func (t *ReadLinesTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseReadLines(ctx, args)
	return err
}
func (t *ReadLinesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, _, err := t.executeReadSource(ctx, args)
	return output, err
}
func (t *ReadLinesTool) executeRead(ctx context.Context, args json.RawMessage) (string, *schema.ReadReceipt, error) {
	out, receipt, _, err := t.executeReadSource(ctx, args)
	return out, receipt, err
}
func (t *ReadLinesTool) executeReadSource(ctx context.Context, args json.RawMessage) (string, *schema.ReadReceipt, []byte, error) {
	encoded, err := parseReadLines(ctx, args)
	if err != nil {
		return "", nil, nil, err
	}
	return t.reader.executeReadSource(ctx, encoded)
}
