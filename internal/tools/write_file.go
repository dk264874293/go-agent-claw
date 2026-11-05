/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 11:20:50
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 11:20:57
 * @FilePath: /go-agent-claw/internal/tools/write_file.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/tools/write_file.go
package tools

import (
	"context"
	"encoding/json"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type WriteFileTool struct{ workDir string }

func NewWriteFileTool(workDir string) *WriteFileTool { return &WriteFileTool{workDir: workDir} }

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "创建或覆盖工作区文件。覆盖时必须提供 read_file 返回的版本。", map[string]any{
		"path":             stringParam("工作区相对路径"),
		"content":          stringParam("完整文件内容"),
		"expected_version": stringParam("覆盖时填 read_file 返回的 SHA-256，创建时填 missing 或省略"),
	}, "path", "content")
}

type writeFileArgs struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	ExpectedVersion string `json:"expected_version"`
}

func parseWrite(ctx context.Context, args json.RawMessage) (input writeFileArgs, err error) {
	if err := decodeArgs(args, &input, "path", "content"); err != nil {
		return input, err
	}
	input.Path, err = writablePath(ctx, input.Path)
	if err != nil {
		return input, err
	}
	if err := expectedVersion(input.ExpectedVersion, true); err != nil {
		return input, err
	}
	if input.ExpectedVersion == "" {
		input.ExpectedVersion = MissingVersion
	}
	return input, nil
}

func (t *WriteFileTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseWrite(ctx, args)
	return err
}

func (t *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return mutateFile(ctx, t.workDir, t.Name(), args)
}
