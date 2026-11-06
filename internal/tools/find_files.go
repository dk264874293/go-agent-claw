package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

const maxFindEntries = 2000
const maxFindMatches = 200

// FindFilesTool 只发现当前权限内的普通文件，不跟随遍历子项中的符号链接。
type FindFilesTool struct {
	workDir       string
	openDirectory func(context.Context, *os.Root, string) (findDirectory, error)
}

func NewFindFilesTool(workDir string) *FindFilesTool { return &FindFilesTool{workDir: workDir} }
func (t *FindFilesTool) Name() string                { return "find_files" }

func (t *FindFilesTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "按文件基本名递归发现工作区普通文件，仅返回相对路径；发现不代表读取或完成分析。截断时应缩小路径。", map[string]any{
		"path":      stringParam("目录相对路径，默认 ."),
		"name_glob": nameGlobParam("基本名通配模式，默认 *；例如 *.md，大小写敏感，不支持目录分隔符或 **"),
		"limit":     intParam("最多返回的文件数，默认 200，上限 200；遍历最多检查 2000 个目录项", 1, maxFindMatches),
	})
}

type findFilesArgs struct {
	Path     string `json:"path"`
	NameGlob string `json:"name_glob"`
	Limit    int    `json:"limit"`
}

func nameGlobParam(description string) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "description": description}
}

func validateNameGlob(pattern string) error {
	if pattern == "" || strings.ContainsAny(pattern, "/\\") || strings.Contains(pattern, "**") {
		return reject("参数错误", fmt.Errorf("name_glob 必须是非空文件基本名模式，不允许目录分隔符或 **"))
	}
	if _, err := pathpkg.Match(pattern, ""); err != nil {
		return reject("参数错误", fmt.Errorf("name_glob 模式无效: %w", err))
	}
	return nil
}

func matchesNameGlob(pattern, name string) bool {
	matches, _ := pathpkg.Match(pattern, name)
	return matches
}

func parseFindFiles(ctx context.Context, raw json.RawMessage) (input findFilesArgs, err error) {
	input = findFilesArgs{Path: ".", NameGlob: "*", Limit: maxFindMatches}
	if err := decodeArgs(raw, &input); err != nil {
		return input, err
	}
	input.Path, err = filePath(ctx, input.Path, true)
	if err != nil {
		return input, err
	}
	if err := validateNameGlob(input.NameGlob); err != nil {
		return input, err
	}
	if input.Limit < 1 || input.Limit > maxFindMatches {
		return input, reject("参数错误", fmt.Errorf("limit 必须在 1 至 %d 之间", maxFindMatches))
	}
	return input, nil
}

func (t *FindFilesTool) validate(ctx context.Context, raw json.RawMessage) error {
	_, err := parseFindFiles(ctx, raw)
	return err
}

func (t *FindFilesTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	output, _, err := t.executeDiscovery(ctx, raw)
	return output, err
}

func (t *FindFilesTool) executeDiscovery(ctx context.Context, raw json.RawMessage) (string, *schema.DiscoveryReceipt, error) {
	input, err := parseFindFiles(ctx, raw)
	if err != nil {
		return "", nil, err
	}
	root, err := openRoot(ctx, t.workDir)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	resolved, err := resolveReadPath(ctx, root, input.Path, true)
	if err != nil {
		return "", nil, rootError(err)
	}
	info, err := root.Stat(resolved)
	if err != nil {
		return "", nil, rootError(err)
	}
	if !info.IsDir() {
		return "", nil, reject("路径类型错误", fmt.Errorf("find_files 的 path 必须是目录: %s", input.Path))
	}
	if err := checkPathLinks(root, resolved, false); err != nil {
		return "", nil, err
	}
	open := t.openDirectory
	if open == nil {
		open = openFindDirectory
	}
	result, err := walkFindFiles(ctx, root, resolved, input.NameGlob, input.Limit, open)
	if err != nil {
		return "", nil, rootError(err)
	}
	sort.Strings(result.paths)
	sort.Slice(result.files, func(i, j int) bool { return result.files[i].Path < result.files[j].Path })
	receipt := &schema.DiscoveryReceipt{ManifestID: observe.NewID(), Path: input.Path, NameGlob: input.NameGlob, Files: result.files, Truncated: result.truncated, Unreadable: result.unreadable}
	if receipt.Files == nil {
		receipt.Files = []schema.DiscoveredFile{}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "文件发现: %s\n文件名过滤: %s\n已检查目录项: %d\n匹配文件数: %d\n截断: %t\n\n", input.Path, input.NameGlob, result.visited, len(result.paths), result.truncated)
	var total int64
	for _, file := range receipt.Files {
		total += file.Bytes
	}
	fmt.Fprintf(&out, "发现清单: %s\n清单内文件总字节数: %d（发现时大小，不代表内容已读取）\n\n", receipt.ManifestID, total)
	for _, path := range result.paths {
		out.WriteString(path + "\n")
	}
	if len(result.paths) == 0 {
		out.WriteString("没有发现匹配的普通文件。\n")
	}
	if result.unreadable > 0 {
		fmt.Fprintf(&out, "跳过 %d 个不可读或已变化的目录项。\n", result.unreadable)
	}
	if result.truncated {
		out.WriteString("已达到发现上限，请缩小 path 后继续；未列出的范围仍未知。\n")
	}
	out.WriteString("以上仅为文件路径，不证明内容已读取或分析完成。\n")
	return out.String(), receipt, nil
}
