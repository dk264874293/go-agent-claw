package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type ListDirTool struct{ workDir string }

func NewListDirTool(workDir string) *ListDirTool { return &ListDirTool{workDir: workDir} }
func (t *ListDirTool) Name() string              { return "list_dir" }

func (t *ListDirTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "列出工作区目录的直接子项。", map[string]any{
		"path": stringParam("工作区相对路径，默认 ."),
	})
}

type listDirArgs struct {
	Path string `json:"path"`
}

func parseList(ctx context.Context, args json.RawMessage) (string, error) {
	input := listDirArgs{Path: "."}
	if err := decodeArgs(args, &input); err != nil {
		return "", err
	}
	return filePath(ctx, input.Path, true)
}

func (t *ListDirTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseList(ctx, args)
	return err
}

func (t *ListDirTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	path, err := parseList(ctx, args)
	if err != nil {
		return "", err
	}
	root, err := openRoot(ctx, t.workDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	resolved, err := resolveReadPath(ctx, root, path, true)
	if err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(resolved))
	if err != nil {
		return "", rootError(err)
	}
	if err := checkPathLinks(root, resolved, false); err != nil {
		return "", err
	}
	scope := ScopeFromContext(ctx)
	var out strings.Builder
	fmt.Fprintf(&out, "目录: %s\n", path)
	visible := 0
	for _, entry := range entries {
		child := filepath.Join(resolved, entry.Name())
		if protectedRuntimePath(child) || !scope.allowsPath(child, entry.IsDir()) {
			continue
		}
		if visible == 200 {
			out.WriteString("只显示前 200 项，请缩小目录范围。\n")
			break
		}
		visible++
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		}
		fmt.Fprintf(&out, "%s%s\n", entry.Name(), suffix)
	}
	if visible == 0 {
		out.WriteString("目录内没有可见项目。\n")
	}
	return out.String(), nil
}

type SearchTextTool struct{ workDir string }

func NewSearchTextTool(workDir string) *SearchTextTool { return &SearchTextTool{workDir: workDir} }
func (t *SearchTextTool) Name() string                 { return "search_text" }

func (t *SearchTextTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "在工作区内按字面文本搜索，返回相对路径、行号和匹配片段。", map[string]any{
		"path":  stringParam("文件或目录的相对路径，默认 ."),
		"query": stringParam("要匹配的文本，区分大小写"),
		"limit": intParam("最多返回的匹配数，默认 30", 1, 100),
	}, "query")
}

type searchTextArgs struct {
	Path  string `json:"path"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func parseSearch(ctx context.Context, args json.RawMessage) (input searchTextArgs, err error) {
	input = searchTextArgs{Path: ".", Limit: 30}
	if err := decodeArgs(args, &input, "query"); err != nil {
		return input, err
	}
	input.Path, err = filePath(ctx, input.Path, true)
	if err != nil {
		return input, err
	}
	if input.Query == "" || strings.ContainsAny(input.Query, "\r\n") || input.Limit < 1 || input.Limit > 100 {
		return input, reject("参数错误", fmt.Errorf("query 必须是非空单行文本，limit 必须在 1 至 100 之间"))
	}
	return input, nil
}

func (t *SearchTextTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseSearch(ctx, args)
	return err
}

func (t *SearchTextTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	input, err := parseSearch(ctx, args)
	if err != nil {
		return "", err
	}
	root, err := openRoot(ctx, t.workDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	resolved, err := resolveReadPath(ctx, root, input.Path, true)
	if err != nil {
		return "", err
	}
	scope := ScopeFromContext(ctx)
	matches, files, skipped := 0, 0, 0
	var out strings.Builder
	err = fs.WalkDir(root.FS(), filepath.ToSlash(resolved), func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == resolved {
				return walkErr
			}
			skipped++
			return nil
		}
		if protectedRuntimePath(path) || !scope.allowsPath(path, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".claw":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if files == 2000 {
			return fs.SkipAll
		}
		files++
		file, info, err := openRegular(ctx, root, path)
		if err != nil {
			skipped++
			return nil
		}
		defer file.Close()
		if info.Size() > 4*1024*1024 {
			skipped++
			return nil
		}
		scanner := bufio.NewScanner(contextReader{ctx, file})
		scanner.Buffer(make([]byte, 4096), 256*1024)
		for line := 1; scanner.Scan(); line++ {
			text := scanner.Text()
			index := strings.Index(text, input.Query)
			if index < 0 || strings.ContainsRune(text, 0) {
				continue
			}
			start := max(0, index-150)
			end := min(len(text), start+400)
			snippet := strings.ToValidUTF8(text[start:end], "�")
			if start > 0 {
				snippet = "…" + snippet
			}
			if end < len(text) {
				snippet += "…"
			}
			fmt.Fprintf(&out, "%s:%d: %s\n", path, line, snippet)
			matches++
			if matches == input.Limit {
				return fs.SkipAll
			}
		}
		if err := scanner.Err(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			skipped++
		}
		return nil
	})
	if err != nil {
		return out.String(), rootError(err)
	}
	if matches == 0 {
		out.WriteString("没有找到匹配项。\n")
	}
	if matches == input.Limit || files == 2000 {
		out.WriteString("已达到搜索上限，可缩小范围后继续。\n")
	}
	if skipped > 0 {
		fmt.Fprintf(&out, "跳过 %d 个过大或无法读取的文件。\n", skipped)
	}
	return out.String(), nil
}
