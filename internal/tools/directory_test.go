package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func assertNotDirectoryResult(t *testing.T, result schema.ToolResult) {
	t.Helper()
	if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || result.ErrorKind != "路径类型错误" || result.ErrorCode != ErrPathNotDirectory || result.Retryable == nil || *result.Retryable || !result.IsError {
		t.Fatalf("目录类型错误未结构化或被误标可重试: %+v", result)
	}
	if !strings.Contains(result.Output, "路径不是目录") || !strings.Contains(result.Output, "read_file") {
		t.Fatalf("目录类型错误缺少纠正提示: %s", result.Output)
	}
}

func TestListDirRejectsFilesWithoutRetry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claw"), []byte("合成可执行文件"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	registry := NewRegistry()
	registry.Register(NewListDirTool(dir))
	for _, path := range []string{"claw", "claw/child"} {
		result := runTestTool(ctx, registry, "list_dir", map[string]any{"path": path})
		assertNotDirectoryResult(t, result)
	}
	if _, err := os.Stat(filepath.Join(dir, "claw")); err != nil {
		t.Fatalf("只读错误处理改变了文件: %v", err)
	}
}

func TestDirectoryTypeErrorSurvivesWrappedENOTDIR(t *testing.T) {
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly})
	registry := NewRegistry()
	registry.Register(registryErrorTool{name: "list_dir", run: func(context.Context) error {
		// 模拟目录类型校验完成后，fs.ReadDir 遇到目录被替换为文件的错误。
		err := fmt.Errorf("目录读取失败: %w", &os.PathError{Op: "readdir", Path: "changed", Err: syscall.ENOTDIR})
		return directoryReadError("changed", err)
	}})
	assertNotDirectoryResult(t, runTestTool(ctx, registry, "list_dir", map[string]any{"path": "changed"}))

	permissionErr := directoryReadError("changed", fs.ErrPermission)
	if !errors.Is(permissionErr, fs.ErrPermission) {
		t.Fatalf("其他文件系统错误被改写: %v", permissionErr)
	}
}

func TestDirectoryErrorsDoNotChangeOtherRetryMetadata(t *testing.T) {
	dir := t.TempDir()
	registry := NewRegistry()
	registry.Register(NewListDirTool(dir))
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	for _, args := range []map[string]any{{"path": "missing"}, {"path": "../outside"}, {"path": ".claw/tasks"}, {"path": 1}} {
		result := runTestTool(ctx, registry, "list_dir", args)
		if !result.IsError || result.ErrorCode != "" || result.Retryable != nil {
			t.Fatalf("非目录类型错误被改变重试语义: %+v", result)
		}
	}
	result := runTestTool(ctx, registry, "list_dir", map[string]any{"path": "."})
	if result.IsError || result.ErrorCode != "" || result.Retryable != nil || !strings.HasPrefix(result.Output, "目录: .\n") {
		t.Fatalf("普通目录列表不兼容: %+v", result)
	}
}

func TestPathIsDirectoryChecksCurrentStateAndScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "changing"), []byte("文件"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	if value, err := PathIsDirectory(ctx, "changing"); err != nil || value {
		t.Fatalf("普通文件类型错误: %t %v", value, err)
	}
	if value, err := PathIsDirectory(ctx, "changing/child"); err != nil || value {
		t.Fatalf("文件上级路径类型错误: %t %v", value, err)
	}
	if err := os.Remove(filepath.Join(dir, "changing")); err != nil {
		t.Fatal(err)
	}
	if value, err := PathIsDirectory(ctx, "changing"); value || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("缺失状态不明确: %t %v", value, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "changing"), 0700); err != nil {
		t.Fatal(err)
	}
	if value, err := PathIsDirectory(ctx, "changing"); err != nil || !value {
		t.Fatalf("目录变化被缓存: %t %v", value, err)
	}
	if value, err := PathIsDirectory(ctx, "."); err != nil || !value {
		t.Fatalf("工作区根目录不能观察: %t %v", value, err)
	}
	for _, path := range []string{"../outside", ".claw/tasks", filepath.Join(dir, "changing")} {
		if value, err := PathIsDirectory(ctx, path); err == nil || value {
			t.Fatalf("目录观察允许越界路径 %q: %t %v", path, value, err)
		}
	}
	scoped := WithScope(ctx, ExecutionScope{Mode: ModeReadOnly, WorkDir: dir, AllowedPaths: []string{"allowed"}})
	if value, err := PathIsDirectory(scoped, "changing"); err == nil || value {
		t.Fatalf("目录观察扩大子任务路径权限: %t %v", value, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if value, err := PathIsDirectory(cancelled, "changing"); value || !errors.Is(err, context.Canceled) {
		t.Fatalf("目录观察忽略取消: %t %v", value, err)
	}
}

func TestDirectoryObservationKeepsLinkBoundaries(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"safe-link": "inside", "outside-link": outside, "runtime-link": ".claw"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".claw"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	if value, err := PathIsDirectory(ctx, "safe-link"); err != nil || !value {
		t.Fatalf("原有安全相对链接不可用: %t %v", value, err)
	}
	for _, name := range []string{"outside-link", "runtime-link"} {
		if value, err := PathIsDirectory(ctx, name); err == nil || value {
			t.Fatalf("目录观察暴露越界链接 %s: %t %v", name, value, err)
		}
	}
	scoped := WithScope(ctx, ExecutionScope{Mode: ModeReadOnly, WorkDir: dir, AllowedPaths: []string{"safe-link"}})
	if value, err := PathIsDirectory(scoped, "safe-link"); err == nil || value {
		t.Fatalf("子任务目录观察越过链接限制: %t %v", value, err)
	}
}

func TestToolErrorMetadataPreservesUnknownAndExplicitFalse(t *testing.T) {
	var old schema.ToolResult
	if err := json.Unmarshal([]byte(`{"tool_call_id":"old","status":"FAILED","effect":"NONE","is_error":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ErrorCode != "" || old.Retryable != nil {
		t.Fatalf("旧记录凭空生成错误类别或重试结论: %+v", old)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly})
	result := toolErrorResult(ctx, schema.ToolCall{ID: "new", Name: "list_dir"}, "", nil, rejectPathNotDirectory("claw"))
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"retryable":false`) || !strings.Contains(string(data), `"error_code":"path_not_directory"`) {
		t.Fatalf("显式false在序列化时丢失: %s", data)
	}
	var restored schema.ToolResult
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	assertNotDirectoryResult(t, restored)
}
