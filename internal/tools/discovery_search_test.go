package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func discoveryFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func discoveryRegistry(root string) Registry {
	r := NewRegistry()
	r.Register(NewSearchTextTool(root))
	r.Register(NewFindFilesTool(root))
	return r
}

func TestSearchTextClassifiesWholeFilesBeforeReturningMatches(t *testing.T) {
	dir := t.TempDir()
	discoveryFile(t, dir, "a.md", []byte("# 合法标题\n"))
	discoveryFile(t, dir, "renamed.md", append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("# 二进制命中，不可返回\n")...))
	discoveryFile(t, dir, "paper.pdf", []byte("%PDF-1.7\n# 二进制命中，不可返回\n"))
	discoveryFile(t, dir, "nul.txt", []byte("# 二进制命中，不可返回\x00\n"))
	discoveryFile(t, dir, "bad.txt", []byte{'#', 0xff, 0xfe, 'x'})
	discoveryFile(t, dir, "utf16.txt", []byte{0xff, 0xfe, '#', 0, 'a', 0})
	discoveryFile(t, dir, "large.txt", []byte(strings.Repeat("a", maxSearchFileBytes)+"#"))
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	result := runTestTool(ctx, discoveryRegistry(dir), "search_text", map[string]any{"query": "#", "limit": 100})
	if result.IsError || !strings.Contains(result.Output, "a.md:1: # 合法标题") || strings.Contains(result.Output, "不可返回") || strings.Contains(result.Output, "bad.txt:") || strings.Contains(result.Output, "utf16.txt:") || strings.Contains(result.Output, "large.txt:") {
		t.Fatalf("非文本污染了正文搜索: %+v", result)
	}
	for _, count := range []string{"二进制 3", "不支持编码 2", "过大 1", "不可读 0", "超长行 0"} {
		if !strings.Contains(result.Output, count) {
			t.Fatalf("分类统计不符，缺少 %s: %s", count, result.Output)
		}
	}
}

func TestSearchTextUTF8BoundariesAndLongLines(t *testing.T) {
	dir := t.TempDir()
	discoveryFile(t, dir, "sample.md", []byte(strings.Repeat("a", 8191)+"中文 # 标题\n"))
	line := strings.Repeat("a", 4095) + "\n"
	atLimit := strings.Repeat(line, 1023) + strings.Repeat("a", 4092) + "中\n"
	if len(atLimit) != maxSearchFileBytes {
		t.Fatal("上限夹具长度错误")
	}
	discoveryFile(t, dir, "at-limit.txt", []byte(atLimit))
	discoveryFile(t, dir, "over-limit.txt", []byte(strings.Repeat("a", maxSearchFileBytes-2)+"中"))
	discoveryFile(t, dir, "long.md", []byte(strings.Repeat("x", maxSearchLineBytes+1)+" # 本行跳过\n# 第二行\n"))
	discoveryFile(t, dir, "snippet.md", []byte("ab"+strings.Repeat("中", 100)+"#"+strings.Repeat("文", 200)))
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	r := discoveryRegistry(dir)
	for _, query := range []string{"#", "中"} {
		result := runTestTool(ctx, r, "search_text", map[string]any{"query": query, "limit": 100})
		if result.IsError || !utf8.ValidString(result.Output) || strings.ContainsRune(result.Output, '�') || strings.Contains(result.Output, "over-limit.txt:") || !strings.Contains(result.Output, "不支持编码 0") || !strings.Contains(result.Output, "过大 1") || !strings.Contains(result.Output, "超长行 1") {
			t.Fatalf("UTF-8被截坏或分类错误: %s", result.Output)
		}
		if query == "#" && (!strings.Contains(result.Output, "sample.md:1:") || !strings.Contains(result.Output, "long.md:2: # 第二行") || strings.Contains(result.Output, "本行跳过")) {
			t.Fatalf("逐行读取未跳过长行并保留后续行: %s", result.Output)
		}
		if query == "中" && !strings.Contains(result.Output, "at-limit.txt:1024:") {
			t.Fatalf("有效上限文本被误判: %s", result.Output)
		}
	}
}

func TestSearchNameGlobIsOptionalAndOnlyMatchesBasenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"nested/a.md", "nested/B.MD", "nested/c.txt"} {
		discoveryFile(t, dir, name, []byte("目标\n"))
	}
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	r := discoveryRegistry(dir)
	all := runTestTool(ctx, r, "search_text", map[string]any{"query": "目标"})
	filtered := runTestTool(ctx, r, "search_text", map[string]any{"query": "目标", "name_glob": "*.md"})
	if all.IsError || filtered.IsError || !strings.Contains(all.Output, "c.txt:1:") || !strings.Contains(all.Output, "B.MD:1:") || !strings.Contains(filtered.Output, "a.md:1:") || strings.Contains(filtered.Output, "B.MD:1:") || strings.Contains(filtered.Output, "c.txt:1:") {
		t.Fatalf("缺省兼容或基本名匹配错误: all=%s filtered=%s", all.Output, filtered.Output)
	}
	for _, pattern := range []string{"", "**", "**/*.md", "nested/*.md", "nested\\*.md", "[abc"} {
		for _, tool := range []string{"search_text", "find_files"} {
			args := map[string]any{"name_glob": pattern}
			if tool == "search_text" {
				args["query"] = "目标"
			}
			result := runTestTool(ctx, r, tool, args)
			if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone {
				t.Fatalf("接受非法模式 %q: %+v", pattern, result)
			}
		}
	}
}

func TestFindFilesScopeLinksAndDirectoryNames(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	for _, name := range []string{"visible/a.md", "visible/folder.md/image.png", "private/hidden.md", ".git/hidden.md", ".claw/hidden.md"} {
		discoveryFile(t, dir, name, []byte("内容"))
	}
	discoveryFile(t, outside, "escape.md", []byte("外部"))
	for name, target := range map[string]string{"visible/file-alias.md": "../private/hidden.md", "visible/dir-alias": "../private", "visible/escape.md": filepath.Join(outside, "escape.md")} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	r := discoveryRegistry(dir)
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir, AllowedPaths: []string{"visible"}})
	result := runTestTool(ctx, r, "find_files", map[string]any{"name_glob": "*.md"})
	if result.IsError || !strings.Contains(result.Output, "visible/a.md\n") || !strings.Contains(result.Output, "匹配文件数: 1") || !strings.Contains(result.Output, "截断: false") {
		t.Fatalf("没有正确发现范围内文件: %+v", result)
	}
	for _, hidden := range []string{"private", "folder.md", "alias", "escape.md", ".git", ".claw"} {
		if strings.Contains(result.Output, hidden) {
			t.Fatalf("发现结果泄漏或包含非普通文件 %q: %s", hidden, result.Output)
		}
	}
	for _, path := range []string{"../", "private", "visible/dir-alias"} {
		if result := runTestTool(ctx, r, "find_files", map[string]any{"path": path}); result.Status != schema.ToolRejected {
			t.Fatalf("接受越权路径 %s: %+v", path, result)
		}
	}
	unscoped := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	all := runTestTool(unscoped, r, "find_files", map[string]any{"name_glob": "*.md"})
	if all.IsError || strings.Contains(all.Output, ".git/") || strings.Contains(all.Output, ".claw/") || strings.Contains(all.Output, "alias.md") || strings.Contains(all.Output, "escape.md") {
		t.Fatalf("未保护运行时目录或链接: %s", all.Output)
	}
}

func TestFindFilesBoundsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		discoveryFile(t, dir, fmt.Sprintf("%02d.md", i), []byte("内容"))
	}
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	r := discoveryRegistry(dir)
	limited := runTestTool(ctx, r, "find_files", map[string]any{"name_glob": "*.md", "limit": 2})
	if limited.IsError || !strings.Contains(limited.Output, "匹配文件数: 2") || !strings.Contains(limited.Output, "截断: true") || !strings.Contains(limited.Output, "未列出的范围仍未知") {
		t.Fatalf("结果截断没有明确呈现: %+v", limited)
	}
	for i := 0; i < maxFindEntries; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%04d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	scanned := runTestTool(ctx, r, "find_files", map[string]any{"name_glob": "*.none"})
	if scanned.IsError || !strings.Contains(scanned.Output, "已检查目录项: 2000") || !strings.Contains(scanned.Output, "截断: true") {
		t.Fatalf("遍历没有有界结束: %+v", scanned)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if result := runTestTool(cancelled, r, "find_files", map[string]any{}); result.Status != schema.ToolCancelled {
		t.Fatalf("取消被忽略: %+v", result)
	}
}

func TestFindObservationOnlyIncludesWhitelistedMetadata(t *testing.T) {
	call := schema.ToolCall{ID: "find", Name: "find_files", Arguments: json.RawMessage(`{"path":"docs","name_glob":"*private-pattern*","limit":7}`)}
	payload := observationForTool(call)
	encoded, _ := json.Marshal(payload)
	if payload.Path != "docs" || payload.Limit != 7 || payload.RequestDigest == "" || strings.Contains(string(encoded), "private-pattern") {
		t.Fatalf("发现观测缺少元数据或泄露模式: %s", encoded)
	}
}

type measuredFindDirectory struct {
	findDirectory
	requests, entries, largest, closed *int
	cancel                             context.CancelFunc
}

func (d *measuredFindDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	*d.requests++
	*d.largest = max(*d.largest, n)
	entries, err := d.findDirectory.ReadDir(n)
	*d.entries += len(entries)
	if d.cancel != nil {
		d.cancel()
	}
	return entries, err
}

func (d *measuredFindDirectory) Close() error {
	*d.closed++
	return d.findDirectory.Close()
}

func TestFindFilesReadsHugeDirectoryInBoundedBatches(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxFindEntries+100; i++ {
		discoveryFile(t, dir, fmt.Sprintf("%04d.txt", i), nil)
	}
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	requests, entries, largest, closed := 0, 0, 0, 0
	tool := NewFindFilesTool(dir)
	tool.openDirectory = func(ctx context.Context, root *os.Root, path string) (findDirectory, error) {
		opened, err := openFindDirectory(ctx, root, path)
		if err != nil {
			return nil, err
		}
		return &measuredFindDirectory{findDirectory: opened, requests: &requests, entries: &entries, largest: &largest, closed: &closed}, nil
	}
	r := NewRegistry()
	r.Register(tool)
	result := runTestTool(ctx, r, "find_files", map[string]any{"name_glob": "*.md"})
	if result.IsError || !strings.Contains(result.Output, "已检查目录项: 2000") || !strings.Contains(result.Output, "截断: true") || requests < 2 || largest > 64 || entries > 1999 || closed != 1 {
		t.Fatalf("发现工具无界读取大目录: result=%+v requests=%d entries=%d largest=%d closed=%d", result, requests, entries, largest, closed)
	}
}

func TestFindFilesCancellationClosesActiveDirectory(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		discoveryFile(t, dir, fmt.Sprintf("%03d.md", i), nil)
	}
	ctx, cancel := context.WithCancel(WithScope(context.Background(), ExecutionScope{WorkDir: dir}))
	defer cancel()
	requests, entries, largest, closed := 0, 0, 0, 0
	tool := NewFindFilesTool(dir)
	tool.openDirectory = func(ctx context.Context, root *os.Root, path string) (findDirectory, error) {
		opened, err := openFindDirectory(ctx, root, path)
		if err != nil {
			return nil, err
		}
		return &measuredFindDirectory{findDirectory: opened, requests: &requests, entries: &entries, largest: &largest, closed: &closed, cancel: cancel}, nil
	}
	r := NewRegistry()
	r.Register(tool)
	result := runTestTool(ctx, r, "find_files", map[string]any{"name_glob": "*.md"})
	if result.Status != schema.ToolCancelled || result.Effect != schema.EffectNone || requests != 1 || largest > 64 || closed != 1 {
		t.Fatalf("发现取消后没有停止读取或释放句柄: result=%+v requests=%d largest=%d closed=%d", result, requests, largest, closed)
	}
}
