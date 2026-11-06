package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func runTestTool(ctx context.Context, r Registry, name string, args map[string]any) schema.ToolResult {
	data, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return r.Execute(ctx, schema.ToolCall{ID: "调用", Name: name, Arguments: data})
}

type registryErrorTool struct {
	name string
	run  func(context.Context) error
}

func (t registryErrorTool) Name() string { return t.name }
func (t registryErrorTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.name}
}
func (t registryErrorTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	return "保留的工具输出", t.run(ctx)
}

type registryRequestTimeout struct{}

func (registryRequestTimeout) Error() string   { return "请求超时" }
func (registryRequestTimeout) Timeout() bool   { return true }
func (registryRequestTimeout) Temporary() bool { return true }

func TestRegistryRequestTimeoutDoesNotCancelTool(t *testing.T) {
	dialTimeout := &url.Error{Op: "Post", URL: "https://example.invalid", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: fmt.Errorf("连接失败: %w", context.DeadlineExceeded),
	}}
	for _, tc := range []struct {
		name, tool string
		err        error
		kind       string
		effect     schema.EffectStatus
	}{
		{name: "子任务连接超时", tool: "spawn_subagent", err: dialTimeout, kind: "超时", effect: schema.EffectNone},
		{name: "只读请求超时", tool: "read_file", err: context.DeadlineExceeded, kind: "超时", effect: schema.EffectNone},
		{name: "网络超时独立类型", tool: "timeout_fixture", err: registryRequestTimeout{}, kind: "超时", effect: schema.EffectUnknown},
		{name: "局部请求取消", tool: "spawn_subagent", err: context.Canceled, kind: "执行失败", effect: schema.EffectNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted})
			registry := NewRegistry()
			registry.Register(registryErrorTool{name: tc.tool, run: func(context.Context) error { return tc.err }})
			result := runTestTool(ctx, registry, tc.tool, map[string]any{})
			if result.Status != schema.ToolFailed || result.Effect != tc.effect || result.ErrorKind != tc.kind || !result.IsError {
				t.Fatalf("请求错误分类不符: %+v", result)
			}
			if ctx.Err() != nil || !strings.Contains(result.Output, "保留的工具输出") {
				t.Fatalf("请求错误改变执行上下文或丢失输出: %+v", result)
			}
		})
	}
}

func TestRegistryExecutionContextCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			parent := WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted})
			var ctx context.Context
			var cancel context.CancelFunc
			wantKind := "取消"
			if deadline {
				ctx, cancel = context.WithTimeout(parent, 20*time.Millisecond)
				wantKind = "超时"
			} else {
				ctx, cancel = context.WithCancel(parent)
			}
			defer cancel()
			registry := NewRegistry()
			registry.Register(registryErrorTool{name: "timeout_fixture", run: func(executionCtx context.Context) error {
				if !deadline {
					cancel()
				}
				<-executionCtx.Done()
				return executionCtx.Err()
			}})
			result := runTestTool(ctx, registry, "timeout_fixture", map[string]any{})
			if result.Status != schema.ToolCancelled || result.Effect != schema.EffectUnknown || result.ErrorKind != wantKind {
				t.Fatalf("执行上下文结束分类不符: %+v", result)
			}
		})
	}
}

func TestRegistryExplicitExecutionErrorKeepsPriority(t *testing.T) {
	ctx, cancel := context.WithCancel(WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted}))
	defer cancel()
	registry := NewRegistry()
	registry.Register(registryErrorTool{name: "timeout_fixture", run: func(context.Context) error {
		cancel()
		return reject("参数错误", context.DeadlineExceeded)
	}})
	result := runTestTool(ctx, registry, "timeout_fixture", map[string]any{})
	if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || result.ErrorKind != "参数错误" {
		t.Fatalf("结构化拒绝被上下文错误覆盖: %+v", result)
	}

	// Bash 的独立执行时限已由其结构化回执确认，父上下文仍可继续使用。
	live := WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted})
	registry.Register(registryErrorTool{name: "bash", run: func(context.Context) error {
		return bashCancellation(context.DeadlineExceeded, schema.EffectUnknown)
	}})
	result = runTestTool(live, registry, "bash", map[string]any{})
	if result.Status != schema.ToolCancelled || result.Effect != schema.EffectUnknown || result.ErrorKind != "超时" || live.Err() != nil {
		t.Fatalf("已确认的命令超时回执被覆盖: %+v", result)
	}
}

func TestRegistryRejectsHiddenToolsAndInvalidArgsBeforeMiddleware(t *testing.T) {
	dir := t.TempDir()
	registry := NewRegistry()
	registry.Register(NewWriteFileTool(dir))
	registry.Register(NewBashTool(dir))
	registry.Register(NewReadFileTool(dir))
	middlewareCalls := 0
	registry.Use(func(context.Context, schema.ToolCall) (bool, string) {
		middlewareCalls++
		return true, ""
	})
	if defs := registry.VisibleTools(context.Background()); len(defs) != 1 || defs[0].Name != "read_file" {
		t.Fatalf("只读工具集: %+v", defs)
	}
	result := runTestTool(context.Background(), registry, "write_file", map[string]any{"path": "a", "content": "禁止"})
	if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || !result.IsError {
		t.Fatalf("只读写入: %+v", result)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted, WorkDir: dir})
	for _, args := range []map[string]any{{"command": ""}, {"command": 7}, {"command": nil}, {"command": "true", "unknown": true}} {
		result := runTestTool(ctx, registry, "bash", args)
		if result.Status != schema.ToolRejected || result.ErrorKind != "参数错误" {
			t.Fatalf("非法参数: %+v", result)
		}
	}
	if middlewareCalls != 0 {
		t.Fatalf("不应触发审批中间件，实际 %d 次", middlewareCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("只读工具生成文件: %v", err)
	}
}

func TestRootedToolsBlockTraversalAndExternalSymlinks(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "work")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.WriteFile(outside, []byte("工作区外"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".claw"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".claw", filepath.Join(dir, "runtime_alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claw", "other-task"), []byte("其他任务数据"), 0600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, ".claw", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("技能正文"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".claw/skills/demo/SKILL.md", filepath.Join(dir, "local_skill")); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Register(NewReadFileTool(dir))
	registry.Register(NewWriteFileTool(dir))
	registry.Register(NewListDirTool(dir))
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, WorkDir: dir})
	for _, tc := range []struct{ tool, path string }{
		{"read_file", "../outside"}, {"read_file", outside}, {"read_file", "link"},
		{"write_file", "up/new"}, {"write_file", "link"}, {"list_dir", "up"},
		{"write_file", ".claw/taskstore.lock"}, {"write_file", "runtime_alias/taskstore.lock"},
		{"read_file", ".claw/other-task"}, {"read_file", "runtime_alias/other-task"},
		{"list_dir", "runtime_alias"},
	} {
		args := map[string]any{"path": tc.path}
		if tc.tool == "write_file" {
			args["content"] = "禁止"
		}
		result := runTestTool(ctx, registry, tc.tool, args)
		if result.Status != schema.ToolRejected || strings.Contains(result.Output, "工作区外\n") {
			t.Errorf("%s %s 未被拦截: %+v", tc.tool, tc.path, result)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "工作区外" {
		t.Fatalf("工作区外文件发生变化: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(parent, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("出现越界创建文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claw", "taskstore.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("普通文件工具写入了运行目录: %v", err)
	}
	for _, path := range []string{".claw/skills/demo/SKILL.md", "local_skill"} {
		result := runTestTool(ctx, registry, "read_file", map[string]any{"path": path})
		if result.IsError || !strings.Contains(result.Output, "技能正文") {
			t.Fatalf("技能或工作区内的安全链接不可读: %+v", result)
		}
	}
}

func TestWritePreviewAndVersionRejectConcurrentStaleUpdate(t *testing.T) {
	dir := t.TempDir()
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, WorkDir: dir})
	r := NewRegistry()
	r.Register(NewReadFileTool(dir))
	r.Register(NewWriteFileTool(dir))
	r.Register(NewEditFileTool(dir))
	if result := runTestTool(ctx, r, "write_file", map[string]any{"path": "a", "content": "原文"}); result.Status != schema.ToolSucceeded {
		t.Fatalf("创建失败: %+v", result)
	}
	version, err := FileVersion(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if result := runTestTool(ctx, r, "read_file", map[string]any{"path": "a", "offset": 3, "limit": 3}); !strings.Contains(result.Output, version) || !strings.HasSuffix(result.Output, "文") {
		t.Fatalf("分段读取结果: %+v", result)
	}
	args, _ := json.Marshal(map[string]any{"path": "a", "old_text": "原文", "new_text": "新内容", "expected_version": version})
	call := schema.ToolCall{ID: "编辑", Name: "edit_file", Arguments: args}
	preview, err := PreviewWrite(ctx, call)
	if err != nil || preview.Before != version || preview.After == version {
		t.Fatalf("预览结果: %+v, %v", preview, err)
	}

	var wg sync.WaitGroup
	results := make([]schema.ToolResult, 2)
	for i := range results {
		wg.Go(func() { results[i] = r.Execute(ctx, call) })
	}
	wg.Wait()
	succeeded, rejected := 0, 0
	for _, result := range results {
		if result.Status == schema.ToolSucceeded && result.Effect == schema.EffectConfirmed {
			succeeded++
		}
		if result.Status == schema.ToolRejected && result.ErrorKind == "版本冲突" {
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("竞争结果: %+v", results)
	}
	if actual, err := FileVersion(ctx, "a"); err != nil || actual != preview.After {
		t.Fatalf("写入后的版本不同于预览: %s, %v", actual, err)
	}
}

func TestSearchAndListingStayWithinSubtaskScope(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"visible", "private"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "file"), []byte("标记 "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../private/file", filepath.Join(dir, "visible", "alias")); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir, AllowedPaths: []string{"visible"}})
	r := NewRegistry()
	r.Register(NewReadFileTool(dir))
	r.Register(NewListDirTool(dir))
	r.Register(NewSearchTextTool(dir))
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_dir", map[string]any{}}, {"search_text", map[string]any{"query": "标记"}},
	} {
		result := runTestTool(ctx, r, tc.tool, tc.args)
		if result.IsError || !strings.Contains(result.Output, "visible") || strings.Contains(result.Output, "private") {
			t.Fatalf("子任务发现工具范围: %+v", result)
		}
	}
	result := runTestTool(ctx, r, "read_file", map[string]any{"path": "visible/alias"})
	if result.Status != schema.ToolRejected {
		t.Fatalf("子任务符号链接绕过路径约束: %+v", result)
	}
}

func TestReadFileLineRangeReflectsUTF8Boundary(t *testing.T) {
	dir := t.TempDir()
	body := "甲\n乙丙\n丁"
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.Register(NewReadFileTool(dir))
	ctx := WithScope(context.Background(), ExecutionScope{WorkDir: dir})
	result := runTestTool(ctx, r, "read_file", map[string]any{"path": "a", "offset": 4, "limit": 4})
	if result.IsError || !strings.Contains(result.Output, "行号范围: [2, 2]") ||
		!strings.Contains(result.Output, "下一段 offset: 7") || !strings.HasSuffix(result.Output, "乙") {
		t.Fatalf("分段读取证据范围: %+v", result)
	}
	result = runTestTool(ctx, r, "read_file", map[string]any{"path": "a", "offset": len(body)})
	if result.IsError || !strings.Contains(result.Output, "行号范围: [0, 0]") {
		t.Fatalf("空片段的证据范围: %+v", result)
	}
}

func TestBashReportsNonZeroExitAndKeepsOutput(t *testing.T) {
	dir := t.TempDir()
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted, WorkDir: dir})
	r := NewRegistry()
	r.Register(NewBashTool(dir))
	result := runTestTool(ctx, r, "bash", map[string]any{"command": "printf '输出'; printf '错误' >&2; exit 7"})
	if result.Status != schema.ToolFailed || !result.IsError || result.Effect != schema.EffectUnknown ||
		result.ExitCode == nil || *result.ExitCode != 7 || !strings.Contains(result.Output, "输出错误") {
		t.Fatalf("Bash 失败结果: %+v", result)
	}
}

func TestBashCancellationStopsDescendants(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(WithScope(context.Background(), ExecutionScope{Mode: ModeTrusted, WorkDir: dir}))
	defer cancel()
	r := NewRegistry()
	r.Register(NewBashTool(dir))
	done := make(chan schema.ToolResult, 1)
	go func() {
		done <- runTestTool(ctx, r, "bash", map[string]any{"command": "(touch started; sleep 0.3; touch escaped) & wait"})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Bash 子进程未启动")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != schema.ToolCancelled || result.Effect != schema.EffectUnknown {
			t.Fatalf("取消结果: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后工具未结束")
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("取消后子进程仍执行了后续操作: %v", err)
	}
}
