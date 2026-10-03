package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/workspace"
)

type ListFilesTool struct{ workDir string }
type SearchTextTool struct{ workDir string }

func NewListFilesTool(dir string) *ListFilesTool   { return &ListFilesTool{workDir: dir} }
func NewSearchTextTool(dir string) *SearchTextTool { return &SearchTextTool{workDir: dir} }
func (*ListFilesTool) Name() string                { return "list_files" }
func (*SearchTextTool) Name() string               { return "search_text" }
func (*ListFilesTool) ReadOnly() bool              { return true }
func (*SearchTextTool) ReadOnly() bool             { return true }
func (*ListFilesTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace}
}
func (*SearchTextTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace}
}

func (t *ListFilesTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name(), Description: "浏览工作区内一个目录，最多返回 200 个条目；默认根目录，不执行命令。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}}}
}
func (t *SearchTextTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name(), Description: "在工作区文件或目录中搜索字面量文本，不跟随符号链接；最多扫描 1000 个条目、16 层目录、每文件 128KB，最多返回 100 个匹配。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "required": []string{"query"}}}
}
func (t *ListFilesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.ExecuteDetailed(ctx, args)
	return output, err
}
func (t *SearchTextTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := t.ExecuteDetailed(ctx, args)
	return output, err
}

type exploreArgs struct {
	Path  string `json:"path"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func parseExplore(args json.RawMessage, maximum int) (exploreArgs, error) {
	var input exploreArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return input, fmt.Errorf("参数解析失败: %w", err)
	}
	if input.Path == "" {
		input.Path = "."
	}
	if input.Limit == 0 {
		input.Limit = maximum
	}
	if input.Limit < 1 || input.Limit > maximum {
		return input, fmt.Errorf("limit 必须在 1 至 %d 之间", maximum)
	}
	_, err := workspace.Path(input.Path, true)
	return input, err
}
func (t *ListFilesTool) ExecuteDetailed(ctx context.Context, args json.RawMessage) (string, schema.ToolMetadata, error) {
	var meta schema.ToolMetadata
	input, err := parseExplore(args, 200)
	if err != nil {
		return "", meta, err
	}
	root, err := workspace.Open(t.workDir)
	if err != nil {
		return "", meta, err
	}
	defer root.Close()
	dir, err := root.OpenDir(ctx, input.Path)
	if err != nil {
		return "", meta, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(input.Limit + 1)
	if err != nil && err != io.EOF {
		return "", meta, err
	}
	if len(entries) > input.Limit {
		entries = entries[:input.Limit]
		meta.Truncated = true
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var output boundedOutput
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", meta, err
		}
		if _, err := workspace.Path(filepath.Join(input.Path, entry.Name()), true); err != nil {
			continue
		}
		kind := "文件"
		if entry.IsDir() {
			kind = "目录"
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = "符号链接（不跟随）"
		}
		fmt.Fprintf(&output, "%s %q\n", kind, entry.Name())
	}
	text, truncated := output.snapshot()
	meta.Truncated = meta.Truncated || truncated
	if text == "" {
		text = "没有可见条目。"
	}
	if meta.Truncated && !truncated {
		text += "\n[目录条目达到上限，已截断]"
	}
	return text, meta, nil
}
func (t *SearchTextTool) ExecuteDetailed(ctx context.Context, args json.RawMessage) (string, schema.ToolMetadata, error) {
	var meta schema.ToolMetadata
	input, err := parseExplore(args, 100)
	if err != nil {
		return "", meta, err
	}
	if input.Query == "" || len(input.Query) > 1024 {
		return "", meta, fmt.Errorf("query 必须是 1 至 1024 字节的字面量文本")
	}
	root, err := workspace.Open(t.workDir)
	if err != nil {
		return "", meta, err
	}
	defer root.Close()
	var output boundedOutput
	visited, matches := 0, 0
	stopped := func() bool { return visited >= 1000 || matches >= input.Limit || len(output.data) >= maxOutputBytes }
	searchFile := func(path string) error {
		data, truncated, err := root.Read(ctx, path, 128*1024)
		if err != nil {
			return err
		}
		meta.Truncated = meta.Truncated || truncated
		if bytes.Contains(data, []byte{0}) {
			return nil
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.Contains(line, input.Query) {
				fmt.Fprintf(&output, "%q:%d:%s\n", path, lineNumber+1, line)
				matches++
				if stopped() {
					meta.Truncated = true
					break
				}
			}
		}
		return nil
	}
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := root.OpenDir(ctx, path)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, err := dir.ReadDir(64)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				if stopped() {
					meta.Truncated = true
					return nil
				}
				visited++
				child := filepath.Join(path, entry.Name())
				if _, err := workspace.Path(child, true); err != nil || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if entry.IsDir() {
					if depth >= 16 {
						meta.Truncated = true
						continue
					}
					if err := walk(child, depth+1); err != nil {
						return err
					}
				} else if entry.Type().IsRegular() {
					if err := searchFile(child); err != nil {
						return err
					}
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	dir, dirErr := root.OpenDir(ctx, input.Path)
	if dirErr == nil {
		dir.Close()
		err = walk(input.Path, 0)
	} else {
		err = searchFile(input.Path)
	}
	if err != nil {
		return "", meta, err
	}
	text, truncated := output.snapshot()
	meta.Truncated = meta.Truncated || truncated
	if text == "" {
		text = "没有匹配。"
	}
	if meta.Truncated && !truncated {
		text += "\n[搜索达到扫描或匹配上限，结果可能不完整]"
	}
	return text, meta, nil
}

func NewReadOnlyRegistry(dir string) Registry {
	r := NewRegistryWithPolicy(policy.ReadOnly(), nil)
	r.Register(NewReadFileTool(dir))
	r.Register(NewListFilesTool(dir))
	r.Register(NewSearchTextTool(dir))
	return r
}
