package tools

import (
	"bytes"
	"context"
	"encoding/json"
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
	return definition(t.Name(), "非递归查看目录的直接子项，仅在目录结构本身需要核对时使用。按文件类型跨目录发现请直接在目标根调用 find_files，无需逐层列目录。仅对目录调用，带 / 后缀的是目录。", map[string]any{
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
		return "", directoryReadError(path, err)
	}
	isDirectory, err := directoryInfo(ctx, root, resolved)
	if err != nil {
		return "", directoryReadError(path, err)
	}
	if !isDirectory {
		return "", rejectPathNotDirectory(path)
	}
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(resolved))
	if err != nil {
		return "", directoryReadError(path, err)
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
	return definition(t.Name(), "在工作区文本正文中按字面搜索，返回相对路径、行号和匹配片段；按文件名发现文件请用 find_files。自动跳过二进制及不支持的编码。", map[string]any{
		"path":      stringParam("文件或目录的相对路径，默认 ."),
		"query":     stringParam("要匹配的文本，区分大小写"),
		"limit":     intParam("最多返回的匹配数，默认 30", 1, 100),
		"name_glob": nameGlobParam("可选文件基本名过滤，例如 *.md；缺省不过滤，不支持目录分隔符或 **"),
	}, "query")
}

type searchTextArgs struct {
	Path     string  `json:"path"`
	Query    string  `json:"query"`
	Limit    int     `json:"limit"`
	NameGlob *string `json:"name_glob"`
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
	if input.NameGlob != nil {
		if err := validateNameGlob(*input.NameGlob); err != nil {
			return input, err
		}
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
	matches, files := 0, 0
	var skipped searchSkipCounts
	var out strings.Builder
	err = fs.WalkDir(root.FS(), filepath.ToSlash(resolved), func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == resolved {
				return walkErr
			}
			skipped.unreadable++
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
		if input.NameGlob != nil && !matchesNameGlob(*input.NameGlob, entry.Name()) {
			return nil
		}
		if files == 2000 {
			return fs.SkipAll
		}
		files++
		data, reason, err := readSearchText(ctx, root, path)
		if err != nil {
			return err
		}
		if reason != "" {
			skipped.add(reason)
			return nil
		}
		// 整份有界内容已确认编码，逐行切片不再制造采样或显示边界的乱码。
		for offset, line := 0, 1; offset < len(data); line++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := bytes.IndexByte(data[offset:], '\n')
			next := len(data)
			if end < 0 {
				end = len(data)
			} else {
				end += offset
				next = end + 1
			}
			lineBytes := data[offset:end]
			offset = next
			if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
				lineBytes = lineBytes[:len(lineBytes)-1]
			}
			if len(lineBytes) > maxSearchLineBytes {
				skipped.longLines++
				continue
			}
			text := string(lineBytes)
			index := strings.Index(text, input.Query)
			if index < 0 {
				continue
			}
			snippet := searchSnippet(text, index)
			fmt.Fprintf(&out, "%s:%d: %s\n", path, line, snippet)
			matches++
			if matches == input.Limit {
				return fs.SkipAll
			}
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
	fmt.Fprintf(&out, "扫描统计（本次已检查范围）：二进制 %d，不支持编码 %d，过大 %d，不可读 %d，超长行 %d。\n", skipped.binary, skipped.encoding, skipped.tooLarge, skipped.unreadable, skipped.longLines)
	return out.String(), nil
}
