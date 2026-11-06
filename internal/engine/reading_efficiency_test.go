package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type countedReadRegistry struct {
	tools.Registry
	reads int
}

func (r *countedReadRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	if call.Name == "read_file" || call.Name == "read_lines" {
		r.reads++
	}
	return r.Registry.Execute(ctx, call)
}

func TestReadonlyDuplicateNeedsPurposeAndStillCountsAttempt(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "one.md", "V2 默认值为5。\n下一行。\n")
	registry := &countedReadRegistry{Registry: NewExplorerRegistry(dir)}
	calls := 0
	target := ""
	p := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		for _, def := range defs {
			if def.Name == "spawn_subagent" {
				t.Fatal("小任务错误开放委派")
			}
		}
		switch calls {
		case 1:
			return modelCalls(schema.ToolCall{ID: "find", Name: "find_files", Arguments: json.RawMessage(`{"name_glob":"*.md"}`)})
		case 2:
			return modelCalls(readCall("first", "one.md"))
		case 3:
			target = currentRun(ctx).catalogSnapshot().receipts[0].ActionID
			return modelCalls(readCall("duplicate", "one.md"))
		case 4:
			if !strings.Contains(messages[len(messages)-1].Content, "duplicate_read") {
				t.Fatal("无目的复读未拒绝")
			}
			args, _ := json.Marshal(map[string]any{"path": "one.md", "review": schema.ReadReview{TargetID: target, Question: "核对V2默认值是否为5"}})
			return modelCalls(schema.ToolCall{ID: "verify", Name: "read_file", Arguments: args})
		default:
			return reply("原文说明V2默认值为5；没有执行脚本验证运行行为。")
		}
	})
	e := NewAgentEngine(p, registry, EngineOptions{})
	result, err := e.Run(context.Background(), ctxpkg.NewSession("purpose", dir), TaskSpec{Goal: "核对文档"}, nil)
	if err != nil || result.Status != Answered || calls != 5 || result.Usage.ToolCalls != 4 || registry.reads != 2 {
		t.Fatalf("复核/额度错误：%+v calls=%d reads=%d err=%v", result, calls, registry.reads, err)
	}
	if result.Reading == nil || result.Reading.Status != "complete" || result.Reading.CompleteFiles != 1 {
		t.Fatalf("程序覆盖错误：%+v", result.Reading)
	}
}

func TestReadonlyChangedVersionIsNewReadWithoutReview(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "one.md", "V1\n")
	registry := &countedReadRegistry{Registry: NewExplorerRegistry(dir)}
	calls := 0
	p := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("first", "one.md"))
		}
		if calls == 2 {
			writeFixture(t, dir, "one.md", "V2\n")
			return modelCalls(readCall("changed", "one.md"))
		}
		if !strings.Contains(messages[len(messages)-1].Content, "V2") {
			t.Fatal("文件变化后没有真正读取新版本")
		}
		return reply("新版V2")
	})
	result, err := NewAgentEngine(p, registry, EngineOptions{}).Run(context.Background(), ctxpkg.NewSession("changed", dir), TaskSpec{Goal: "读取"}, nil)
	if err != nil || result.Status != Answered || registry.reads != 2 {
		t.Fatalf("新版误判重复: %+v %v", result, err)
	}
}

func TestReadonlyCoverageReportsExactTailAndEmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "tail.md", strings.Repeat("a", 14038))
	writeFixture(t, dir, "empty.md", "")
	calls := 0
	p := scriptedProvider(func(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(schema.ToolCall{ID: "find", Name: "find_files", Arguments: json.RawMessage(`{"name_glob":"*.md"}`)})
		case 2:
			return modelCalls(schema.ToolCall{ID: "head", Name: "read_file", Arguments: json.RawMessage(`{"path":"tail.md","limit":8000}`)}, readCall("empty", "empty.md"))
		case 3:
			return modelCalls(schema.ToolCall{ID: "next", Name: "read_file", Arguments: json.RawMessage(`{"path":"tail.md","offset":8000,"limit":5685}`)})
		default:
			return reply("读取尚有尾部缺口；空文件已确认。")
		}
	})
	result, err := NewAgentEngine(p, NewExplorerRegistry(dir), EngineOptions{}).Run(context.Background(), ctxpkg.NewSession("tail", dir), TaskSpec{Goal: "检查覆盖"}, nil)
	if err != nil || result.Reading == nil || result.Reading.Status != "partial" || result.Reading.CompleteFiles != 1 || result.Reading.TotalFiles != 2 {
		t.Fatalf("覆盖语义错误: %+v %v", result, err)
	}
	for _, f := range result.Reading.Files {
		if f.Path == "tail.md" && (len(f.Missing) != 1 || f.Missing[0].Start != 13685 || f.Missing[0].End != 14038) {
			t.Fatalf("未精确指出353字节尾部: %+v", f)
		}
	}
	if strings.Contains(FormatRunResult(result), "验收已通过") {
		t.Fatal("读取覆盖被当作验收")
	}
}

func TestReadonlyMaskedOldBodyAndParentFirstReadAreNotBlocked(t *testing.T) {
	ctx, r, messages := analysisContextFixture(t)
	r.state.Spec.Delegation = "on"
	r.state.Analysis = nil
	read := r.analysisRuntime.sources["E1"].Receipt
	result := schema.NewToolResult("first", string(r.analysisRuntime.sources["E1"].Source), schema.ToolSucceeded, schema.EffectNone, "", nil)
	result.Read = &read
	r.mu.Lock()
	r.rememberReadReuseLocked(readCall("first", "E1.md"), result)
	r.mu.Unlock()
	messages[3].ReadActionID = read.ActionID
	messages[3].Content = "被压缩的节选"
	r.markReadExposure(ctx, messages)
	if rejected := r.rejectDuplicateRead(ctx, readCall("again", "E1.md")); rejected != nil {
		t.Fatal("已不在上下文的正文被错误拒绝")
	}
	parent := observe.WithAgent(ctx, "parent", "")
	if rejected := r.rejectDuplicateRead(parent, readCall("parent-first", "E1.md")); rejected != nil {
		t.Fatal("父任务首次核对被全局覆盖误拒绝")
	}
}

func TestReadonlyPrimaryManifestDoesNotShrinkToLaterSubdirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := observe.WithAgent(context.Background(), "root", "")
	r := &runState{}
	root := schema.DiscoveryReceipt{ManifestID: "root", Path: ".", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "one.md", Bytes: 1}, {Path: "sub/two.md", Bytes: 1}}}
	sub := schema.DiscoveryReceipt{ManifestID: "sub", Path: "sub", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "sub/two.md", Bytes: 1}}}
	r.mu.Lock()
	r.rememberDiscoveryLocked(ctx, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &root})
	r.rememberDiscoveryLocked(ctx, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &sub})
	r.mu.Unlock()
	if got := r.primaryDiscovery(ctx); got == nil || got.ManifestID != "root" || len(got.Files) != 2 {
		t.Fatalf("任务清单被缩小: %+v", got)
	}
}

func TestReadonlyCoverageCannotBorrowChildOrWrongGlobManifest(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "one.md", "a\n")
	writeFixture(t, dir, "unread.txt", "b\n")
	ctx := tools.WithScope(observe.WithAgent(context.Background(), "root", ""), tools.ExecutionScope{WorkDir: dir, Mode: tools.ModeReadOnly})
	r := &runState{}
	manifest := schema.DiscoveryReceipt{ManifestID: "child", Path: ".", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "one.md", Bytes: 2}}}
	child := observe.WithAgent(ctx, "child", "root")
	r.mu.Lock()
	r.rememberDiscoveryLocked(child, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &manifest})
	r.mu.Unlock()
	if got := r.readingCoverage(ctx); got.Status != "unknown" {
		t.Fatalf("根借用子清单误报完成: %+v", got)
	}
	if got := r.coverageForPaths(ctx, []string{"unread.txt"}); got.Status != "unknown" || len(got.Files) != 1 || !got.Files[0].Unknown {
		t.Fatalf("不同glob的显式文件被省略: %+v", got)
	}
}

func TestReadonlyCrossCoordinateSameRangeNeedsReview(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "one.md", "alpha\nbeta\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(schema.ToolCall{ID: "lines", Name: "read_lines", Arguments: json.RawMessage(`{"path":"one.md","start_line":1,"end_line":1}`)})
		}
		if calls == 2 {
			return modelCalls(schema.ToolCall{ID: "bytes", Name: "read_file", Arguments: json.RawMessage(`{"path":"one.md","offset":0,"limit":6}`)})
		}
		if !strings.Contains(msgs[len(msgs)-1].Content, "duplicate_read") {
			t.Fatal("跨坐标相同实际区间未拒绝")
		}
		return reply("alpha，beta尚未读取。")
	})
	registry := &countedReadRegistry{Registry: NewExplorerRegistry(dir)}
	result, err := NewAgentEngine(p, registry, EngineOptions{}).Run(context.Background(), ctxpkg.NewSession("coordinate", dir), TaskSpec{Goal: "核对同一行"}, nil)
	if err != nil || result.Status != Answered || registry.reads != 1 || result.Usage.ToolCalls != 2 {
		t.Fatalf("跨坐标复读错误: %+v reads=%d %v", result, registry.reads, err)
	}
}

func TestAnalysisWholeFileClaimNeedsCompleteCoverage(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "one.md", "已读\n尚未读\n")
	calls := 0
	p := scriptedProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(schema.ToolCall{ID: "head", Name: "read_lines", Arguments: json.RawMessage(`{"path":"one.md","start_line":1,"end_line":1}`)})
		}
		entries := analysisIntegrationEvidence(ctx)
		report := AnalysisReport{SchemaVersion: 1, Findings: []AnalysisFinding{{ID: "F1", Kind: "fact", Statement: "整个文件没有其他内容", ScopeRequirement: "file", Evidence: []AnalysisCitation{{EvidenceID: entries[0].ID, StartLine: 1, EndLine: 1, Quote: "已读"}}}}}
		raw, _ := json.Marshal(report)
		return reply(string(raw))
	})
	result, err := NewAgentEngine(p, NewExplorerRegistry(dir), EngineOptions{}).Run(context.Background(), ctxpkg.NewSession("scope", dir), TaskSpec{Goal: "检查文件", Analysis: true}, nil)
	if err != nil || result.Status != Blocked || result.StopCode != "analysis_report_invalid" || result.Analysis == nil || len(result.Analysis.Unverified) == 0 {
		t.Fatalf("未完整读取的全文件结论误通过: %+v %v", result, err)
	}
}
