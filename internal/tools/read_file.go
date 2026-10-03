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
	"context"
	"encoding/json"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/workspace"
)

// ReadFileTool 实现了读取本地文件内容的工具
type ReadFileTool struct {
	// 将引擎的 WorkDir 注入给工具，限制它只能在此目录及其子目录下操作
	workDir string
}

func NewReadFileTool(workDir string) *ReadFileTool {
	return &ReadFileTool{workDir: workDir}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}

// Definition 向大模型清晰地描述这个工具的用途和参数格式
func (t *ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "读取指定路径的文件内容。请提供相对工作区的路径。",
		// 遵循 JSON Schema 规范定义参数
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要读取的文件路径，如 cmd/claw/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

// readFileArgs 内部定义用于反序列化的结构体
type readFileArgs struct {
	Path string `json:"path"`
}

// ReadOnly 声明此工具允许与同批只读调用并发。
func (t *ReadFileTool) ReadOnly() bool { return true }

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.ExecuteDetailed(ctx, args)
	return output, err
}

func (t *ReadFileTool) ExecuteDetailed(ctx context.Context, args json.RawMessage) (string, schema.ToolMetadata, error) {
	var input readFileArgs
	var meta schema.ToolMetadata
	if err := ctx.Err(); err != nil {
		return "", meta, err
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", meta, fmt.Errorf("参数解析失败: %w", err)
	}
	if input.Path == "" {
		return "", meta, fmt.Errorf("path 不能为空")
	}
	root, err := workspace.Open(t.workDir)
	if err != nil {
		return "", meta, fmt.Errorf("打开工作目录失败: %w", err)
	}
	defer root.Close()
	data, truncated, err := root.Read(ctx, input.Path, maxOutputBytes)
	if err != nil {
		return "", meta, fmt.Errorf("读取文件失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", meta, err
	}
	meta.Truncated = truncated
	return outputText(data, meta.Truncated), meta, nil
}
