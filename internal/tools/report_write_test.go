package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type reportWriteFixture struct {
	path string
	data []byte
}

func (reportWriteFixture) Name() string { return "write_report" }
func (reportWriteFixture) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "write_report"}
}
func (f reportWriteFixture) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	return WriteReportFile(ctx, f.path, f.data)
}

func TestReportScopeAllowsOnlyReadsAndReport(t *testing.T) {
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, ReportOnly: true, WorkDir: t.TempDir()})
	r := NewRegistry()
	for _, name := range []string{"read_file", "write_file", "edit_file", "bash", "spawn_subagent", "custom"} {
		r.Register(registryErrorTool{name: name, run: func(context.Context) error { t.Fatal("拒绝请求进入了Handler"); return nil }})
	}
	r.Register(reportWriteFixture{path: "report.md", data: []byte("报告")})
	defs := r.VisibleTools(ctx)
	if len(defs) != 2 || defs[0].Name != "read_file" || defs[1].Name != "write_report" {
		t.Fatalf("报告工具前沿错误: %+v", defs)
	}
	for _, name := range []string{"write_file", "edit_file", "bash", "spawn_subagent", "custom"} {
		z := runTestTool(ctx, r, name, map[string]any{})
		if z.Status != schema.ToolRejected || z.Effect != schema.EffectNone {
			t.Fatalf("裸变更绕过报告契约: %+v", z)
		}
	}
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspace, ModeTrusted} {
		plain := WithScope(context.Background(), ExecutionScope{Mode: mode, WorkDir: ScopeFromContext(ctx).WorkDir})
		z := runTestTool(plain, r, "write_report", map[string]any{})
		if z.Status != schema.ToolRejected || z.Effect != schema.EffectNone {
			t.Fatalf("普通任务可执行报告入口: %+v", z)
		}
	}
}

func TestReportWriteFrozenBytesAndSingleHook(t *testing.T) {
	dir := t.TempDir()
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, ReportOnly: true, WorkDir: dir})
	data := []byte("# 报告\n原始冻结内容")
	preview, err := PreviewReportWrite(ctx, "report.md", data)
	if err != nil || preview.Before != MissingVersion || preview.After != contentVersion(data) {
		t.Fatalf("预览错误: %+v %v", preview, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("预览产生了文件副作用")
	}
	hooks := 0
	ctx = WithExecutionHook(ctx, func(context.Context) error { hooks++; return nil })
	r := NewRegistry()
	r.Register(reportWriteFixture{path: "report.md", data: data})
	z := runTestTool(ctx, r, "write_report", map[string]any{})
	if z.Status != schema.ToolSucceeded || z.Effect != schema.EffectConfirmed || hooks != 1 {
		t.Fatalf("报告状态或钩子错误: %+v，钩子%d", z, hooks)
	}
	written, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil || !bytes.Equal(written, data) || contentVersion(written) != preview.After {
		t.Fatalf("写入与冻结材料不一致: %v", err)
	}
	z = runTestTool(ctx, r, "write_report", map[string]any{})
	if z.Status != schema.ToolRejected || z.Effect != schema.EffectNone {
		t.Fatalf("重复提交覆盖了已有报告: %+v", z)
	}
}

func TestReportWriteRequiresExistingSafeParent(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "safe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "original.md"), []byte("原文"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, ReportOnly: true, WorkDir: dir})
	for _, path := range []string{"../bad.md", "/tmp/bad.md", ".claw/bad.md", "missing/report.md", "link/report.md", "original.md", "safe/report.txt"} {
		if _, err := PreviewReportWrite(ctx, path, []byte("不可写")); err == nil {
			t.Fatalf("危险预览通过: %s", path)
		}
		if _, err := WriteReportFile(ctx, path, []byte("不可写")); err == nil {
			t.Fatalf("危险写入通过: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("报告创建了额外目录")
	}
	original, _ := os.ReadFile(filepath.Join(dir, "original.md"))
	if string(original) != "原文" {
		t.Fatal("原始数据被覆盖")
	}
	if files, _ := os.ReadDir(outside); len(files) != 0 {
		t.Fatal("写入越过了符号链接")
	}
	ctx = WithExecutionHook(ctx, func(context.Context) error { return os.Remove(filepath.Join(dir, "safe")) })
	if _, err := WriteReportFile(ctx, "safe/report.md", []byte("报告")); err == nil {
		t.Fatal("父目录消失后仍写入")
	}
	if _, err := os.Stat(filepath.Join(dir, "safe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("报告重新创建了消失的父目录")
	}
}

func TestReportConcurrentCreateHasSingleWinner(t *testing.T) {
	dir := t.TempDir()
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, ReportOnly: true, WorkDir: dir})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, text := range []string{"甲报告", "乙报告"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			_, err := WriteReportFile(ctx, "report.md", []byte(text))
			results <- err
		}(text)
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发创建没有唯一胜者: %d", winners)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil || string(data) != "甲报告" && string(data) != "乙报告" {
		t.Fatalf("产物损坏: %q %v", data, err)
	}
}

func TestDirectReportWriteFreezesBeforeExecutionHook(t *testing.T) {
	dir := t.TempDir()
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeWorkspace, ReportOnly: true, WorkDir: dir})
	data := []byte("冻结版本")
	want := bytes.Clone(data)
	preview, err := PreviewReportWrite(ctx, "report.md", data)
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithExecutionHook(ctx, func(context.Context) error {
		copy(data, []byte("后续变更"))
		return nil
	})
	if _, err := WriteReportFile(ctx, "report.md", data); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil || !bytes.Equal(got, want) || contentVersion(got) != preview.After {
		t.Fatalf("执行钩子改变了冻结产物: %q %v", got, err)
	}
	failCtx := WithExecutionHook(ctx, func(context.Context) error { return errors.New("执行前记录失败") })
	if _, err := WriteReportFile(failCtx, "blocked.md", data); err == nil {
		t.Fatal("记录失败后仍然执行")
	}
	if _, err := os.Stat(filepath.Join(dir, "blocked.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("记录失败产生了文件副作用")
	}
}
