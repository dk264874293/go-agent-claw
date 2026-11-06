package observability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type fakeProvider func() (*schema.Message, error)

func (p fakeProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return p()
}

func TestConcurrentUsageWithUnpricedCalls(t *testing.T) {
	t.Setenv("CLAW_MODEL_PRICES", `{"priced":{"currency":"CNY","input_per_million":2,"output_per_million":3},"foreign":{"currency":"USD","input_per_million":1,"output_per_million":1}}`)
	session := ctxpkg.NewSession("用量", t.TempDir())
	sample := fakeProvider(func() (*schema.Message, error) {
		return &schema.Message{Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 200}}, nil
	})
	tracker := NewCostTracker(sample, "priced", session)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, _ = tracker.Generate(context.Background(), nil, nil)
			_ = session.UsageSnapshot()
		})
	}
	wg.Wait()
	got := session.UsageSnapshot()
	if !got.CostKnown || !got.TokensKnown || got.PromptTokens != 1000 || got.CompletionTokens != 2000 || got.CostCNY < 0.00799 || got.CostCNY > 0.00801 {
		t.Fatalf("并发账单不正确: %#v", got)
	}

	_, _ = NewCostTracker(sample, "foreign", session).Generate(context.Background(), nil, nil)
	got = session.UsageSnapshot()
	if got.CostKnown || !got.TokensKnown || got.PromptTokens != 1100 || got.UnknownCostCalls != 1 {
		t.Fatalf("未知币种没有单独计数: %#v", got)
	}
	failed := fakeProvider(func() (*schema.Message, error) { return nil, errors.New("连接中断") })
	_, _ = NewCostTracker(failed, "priced", session).Generate(context.Background(), nil, nil)
	got = session.UsageSnapshot()
	if got.CostKnown || got.TokensKnown || got.UnknownUsageCalls != 1 || got.UnknownCostCalls != 2 {
		t.Fatalf("缺失用量没有单独计数: %#v", got)
	}
}

func TestTraceExportsUniqueRedactedRecords(t *testing.T) {
	root := t.TempDir()
	ctx, span := StartSpan(context.Background(), "运行")
	span.AddAttribute("Prompt", "秘密输入")
	_, child := StartSpan(ctx, "工具执行")
	child.AddAttribute("arguments", "秘密参数")
	child.AddAttribute("output_preview", "秘密输出")
	child.AddAttribute("status", "FAILED")
	child.EndSpan()
	span.EndSpan()
	for range 2 {
		if err := ExportTraceToFile(span, root, "same-id"); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, ".claw", "traces", "*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("导出文件被覆盖: %v, %v", files, err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "秘密") || !strings.Contains(string(data), "FAILED") {
			t.Fatalf("导出的追踪记录不正确: %s, %v", data, err)
		}
	}
}

func TestTraceCannotFollowWorkspaceEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".claw")); err != nil {
		t.Fatal(err)
	}
	_, span := StartSpan(context.Background(), "运行")
	span.EndSpan()
	if err := ExportTraceToFile(span, root, "test"); err == nil {
		t.Fatal("导出追踪时跟随了工作区外的链接")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("工作区外被修改: %v, %v", entries, err)
	}
}
