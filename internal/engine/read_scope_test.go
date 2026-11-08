package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadScopeNormalizeAndPersistentValidation(t *testing.T) {
	input := &ReadScopeSpec{Paths: []string{"z/", "a/./b.md", "z", "."}, RequireComplete: true}
	spec := TaskSpec{ReadScope: input}
	if err := NormalizeReadScope(&spec); err != nil {
		t.Fatal(err)
	}
	want := &ReadScopeSpec{Version: 1, Paths: []string{".", "a/b.md", "z"}, NameGlob: "*", RequireComplete: true}
	if !reflect.DeepEqual(spec.ReadScope, want) || len(input.Paths) != 4 || input.Version != 0 {
		t.Fatalf("规范化/原配置复制错误：%+v input=%+v", spec.ReadScope, input)
	}
	if err := ValidateReadScopeSpec(spec.ReadScope); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadScopeSpec(input); err == nil {
		t.Fatal("历史配置校验补造了默认值")
	}
	for _, invalid := range []*ReadScopeSpec{
		{Version: 2, Paths: []string{"a"}}, {Paths: nil}, {Paths: []string{"../outside"}}, {Paths: []string{"a/../b"}},
		{Paths: []string{"/absolute"}}, {Paths: []string{".claw/tasks"}}, {Paths: []string{"a\\b"}}, {Paths: []string{"a\nb"}},
		{Paths: []string{"a"}, NameGlob: "**/*.md"}, {Paths: []string{"a"}, NameGlob: "["},
	} {
		candidate := TaskSpec{ReadScope: invalid}
		if err := NormalizeReadScope(&candidate); err == nil {
			t.Errorf("非法配置未拒绝：%+v", invalid)
		}
	}
}

func coverageV2Fixture(t *testing.T, partial bool) (context.Context, *runState, []schema.DiscoveryReceipt) {
	t.Helper()
	dir := t.TempDir()
	ctx := tools.WithScope(observe.WithAgent(context.Background(), "root-v2", ""), tools.ExecutionScope{WorkDir: dir, Mode: tools.ModeReadOnly})
	r := &runState{state: taskState{Spec: TaskSpec{ID: "v2-task", Mode: tools.ModeReadOnly, Kind: TaskAnswer, Delegation: "off", ReadContextVersion: 1}}}
	var discoveries []schema.DiscoveryReceipt
	for d := 1; d <= 5; d++ {
		name := fmt.Sprintf("%02d资料包", d)
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
		manifest := schema.DiscoveryReceipt{ManifestID: fmt.Sprintf("manifest-%d", d), Path: name, NameGlob: "*.md"}
		for f := 1; f <= 3; f++ {
			path := fmt.Sprintf("%s/doc%d.md", name, f)
			body := "V2默认值为5。\n后续资料。\n"
			if d == 5 && f == 3 {
				body = strings.Repeat("a", 14038)
			}
			writeFixture(t, dir, path, body)
			manifest.Files = append(manifest.Files, schema.DiscoveredFile{Path: path, Bytes: int64(len(body))})
			version, err := tools.FileVersion(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			size := int64(len(body))
			end := size
			if partial && d == 5 && f == 3 {
				end = 13685
			}
			r.reads.receipts = append(r.reads.receipts, schema.ReadReceipt{Path: path, Version: version, ByteStart: 0, ByteEnd: end, TotalBytes: size, AgentID: "root-v2", ActionID: path})
		}
		discoveries = append(discoveries, manifest)
	}
	return ctx, r, discoveries
}

func rememberCoverageManifests(ctx context.Context, r *runState, manifests []schema.DiscoveryReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range manifests {
		r.rememberDiscoveryLocked(ctx, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &manifests[i]})
	}
}

func TestCoverageV2UnionsFiveManifestsIndependentOfCompletionOrder(t *testing.T) {
	ctx, r, manifests := coverageV2Fixture(t, true)
	rememberCoverageManifests(ctx, r, manifests)
	first := r.readingCoverage(ctx)
	if first.ScopeKind != "observed" || first.Status != "partial" || first.TotalFiles != 15 || first.CompleteFiles != 14 || len(first.ManifestIDs) != 5 {
		t.Fatalf("不是全观察集合：%+v", first)
	}
	last := first.Files[len(first.Files)-1]
	if len(last.Missing) != 1 || last.Missing[0] != (ByteInterval{Start: 13685, End: 14038}) {
		t.Fatalf("尾页缺口不精确：%+v", last)
	}
	beforeContext := r.readingContext(ctx)
	for _, expected := range []string{"13685", "14038", "next_offset", "05资料包/doc3.md", "scope_kind", "observed"} {
		if !strings.Contains(beforeContext, expected) {
			t.Errorf("提示缺少%s: %s", expected, beforeContext)
		}
	}
	r.discoveries = nil
	r.discoveryOwners = nil
	reversed := append([]schema.DiscoveryReceipt(nil), manifests...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	rememberCoverageManifests(ctx, r, reversed)
	second := r.readingCoverage(ctx)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("独立目录提交顺序影响统计\n%+v\n%+v", first, second)
	}
	if !strings.Contains(readingCoverageText(first), "任务来源范围未声明") || !strings.Contains(readingCoverageText(first), "353") {
		t.Fatal("观察范围缺少边界/缺口提示")
	}
}

func TestCoverageV2DeclaredDirectoriesNeedExactCurrentDiscovery(t *testing.T) {
	ctx, r, manifests := coverageV2Fixture(t, false)
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"01资料包", "02资料包", "03资料包", "04资料包", "05资料包"}, NameGlob: "*.md", RequireComplete: true}
	if got := r.readingCoverage(ctx); got.Status != "unknown" || len(got.UnresolvedPaths) != 5 {
		t.Fatalf("未发现目录被补造完成：%+v", got)
	}
	child := observe.WithAgent(ctx, "child-v2", "root-v2")
	rememberCoverageManifests(child, r, manifests)
	if got := r.readingCoverage(ctx); got.Status != "unknown" || len(got.ManifestIDs) != 0 {
		t.Fatalf("借用子任务发现：%+v", got)
	}
	rememberCoverageManifests(ctx, r, manifests)
	complete := r.readingCoverage(ctx)
	if complete.ScopeKind != "declared" || complete.Status != "complete" || complete.TotalFiles != 15 || len(complete.UnresolvedPaths) != 0 {
		t.Fatalf("完整显式范围错误：%+v", complete)
	}
	// 所有子目录完整，仍不能证明整个父目录的 glob 选择条件完整。
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"."}, NameGlob: "*.md"}
	if got := r.readingCoverage(ctx); got.Status != "unknown" || len(got.UnresolvedPaths) != 1 {
		t.Fatalf("局部清单推断父目录：%+v", got)
	}
	// 其它 glob 的清单也不能满足所声明的目录选择。
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"01资料包"}, NameGlob: "*.txt"}
	if got := r.readingCoverage(ctx); got.Status != "unknown" {
		t.Fatalf("不同过滤被借用：%+v", got)
	}
}

func TestCoverageV2OverlapRefreshAndPartialDiscovery(t *testing.T) {
	ctx, r, manifests := coverageV2Fixture(t, false)
	rememberCoverageManifests(ctx, r, manifests)
	root := schema.DiscoveryReceipt{ManifestID: "root-full", Path: ".", NameGlob: "*.md"}
	for _, m := range manifests {
		root.Files = append(root.Files, m.Files...)
	}
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{root})
	if got := r.readingCoverage(ctx); got.TotalFiles != 15 || got.Status != "complete" {
		t.Fatalf("重叠清单重复计数：%+v", got)
	}
	old := r.readingCoverage(ctx)
	refresh := root
	refresh.ManifestID = "root-refresh"
	refresh.Truncated = true
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{refresh})
	got := r.readingCoverage(ctx)
	if got.Status != "unknown" || !got.DiscoveryIncomplete || containsString(got.ManifestIDs, "root-full") || got.ScopeRevision == old.ScopeRevision {
		t.Fatalf("刷新没有替换/截断没有保留未知：%+v", got)
	}
	refresh.ManifestID = "root-unreadable"
	refresh.Truncated = false
	refresh.Unreadable = 1
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{refresh})
	if got := r.readingCoverage(ctx); got.Status != "unknown" {
		t.Fatalf("不可读清单误报完成：%+v", got)
	}
}

func TestCoverageV2OutputsExcludedButExistingModifiedSourcesRemain(t *testing.T) {
	ctx, r, manifests := coverageV2Fixture(t, false)
	rememberCoverageManifests(ctx, r, manifests)
	dir := tools.ScopeFromContext(ctx).WorkDir
	writeFixture(t, dir, "new-report.md", "产物\n")
	writeFixture(t, dir, "explicit-report.md", "正式报告目标\n")
	for _, path := range []string{"new-report.md", "explicit-report.md"} {
		version, _ := tools.FileVersion(ctx, path)
		r.reads.receipts = append(r.reads.receipts, schema.ReadReceipt{Path: path, Version: version, ByteStart: 0, ByteEnd: 7, TotalBytes: 7, AgentID: "root-v2"})
	}
	r.state.Spec.Report = &ReportSpec{TargetPath: "explicit-report.md"}
	r.state.EffectLedger = &Delivery{Effects: []EffectReceipt{{ActionID: "output", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "new-report.md", BeforeVersion: tools.MissingVersion, AfterVersion: "confirmed"}}}}
	root := schema.DiscoveryReceipt{ManifestID: "with-output", Path: ".", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "new-report.md", Bytes: 7}, {Path: "explicit-report.md", Bytes: 20}}}
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{root})
	got := r.readingCoverage(ctx)
	if got.TotalFiles != 15 {
		t.Fatalf("产物污染输入集合：%+v", got)
	}
	path := "01资料包/doc1.md"
	previous := got.ScopeRevision
	writeFixture(t, dir, path, "原文件已经变化\n")
	r.state.EffectLedger.Effects = append(r.state.EffectLedger.Effects, EffectReceipt{ActionID: "source-edit", Effect: schema.EffectConfirmed, File: &FileEffect{Path: path, BeforeVersion: "old", AfterVersion: "new"}})
	got = r.readingCoverage(ctx)
	if got.TotalFiles != 15 || got.CompleteFiles != 14 || got.Status != "partial" || got.ScopeRevision == previous {
		t.Fatalf("原资料变化没有失效或被排除：%+v", got)
	}
}

func TestCoverageV2ScopeRevisionUnaffectedByProgressAndMissingFileSafe(t *testing.T) {
	ctx, r, manifests := coverageV2Fixture(t, true)
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"05资料包/doc3.md"}, NameGlob: "*.txt", RequireComplete: true}
	rememberCoverageManifests(ctx, r, manifests)
	first := r.readingCoverage(ctx)
	if first.TotalFiles != 1 || first.Status != "partial" {
		t.Fatalf("显式文件被glob遗漏：%+v", first)
	}
	read := r.reads.receipts[len(r.reads.receipts)-1]
	read.ByteStart = 13685
	read.ByteEnd = 14038
	r.reads.receipts = append(r.reads.receipts, read)
	second := r.readingCoverage(ctx)
	if second.Status != "complete" || second.ScopeRevision != first.ScopeRevision || second.ScopeID != first.ScopeID {
		t.Fatalf("阅读进度不应改变成员修订：%+v → %+v", first, second)
	}
	if err := os.Remove(filepath.Join(tools.ScopeFromContext(ctx).WorkDir, "05资料包/doc3.md")); err != nil {
		t.Fatal(err)
	}
	if got := r.readingCoverage(ctx); got.Status != "unknown" {
		t.Fatalf("缺失文件沿用历史读取：%+v", got)
	}
}

func TestCoverageV2EmptyFileRequiresSuccessfulReadAndGapsBounded(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "empty.md", "")
	ctx := tools.WithScope(observe.WithAgent(context.Background(), "root", ""), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir})
	r := &runState{state: taskState{Spec: TaskSpec{ReadContextVersion: 1, ReadScope: &ReadScopeSpec{Version: 1, Paths: []string{"empty.md"}, NameGlob: "*"}}}}
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{{ManifestID: "empty", Path: ".", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "empty.md", Bytes: 0}}}})
	if got := r.readingCoverage(ctx); got.Status == "complete" || got.CompleteFiles != 0 {
		t.Fatalf("未读空文件误报完成：%+v", got)
	}
	version, _ := tools.FileVersion(ctx, "empty.md")
	r.reads.receipts = []schema.ReadReceipt{{Path: "empty.md", Version: version, AgentID: "root", TotalBytes: 0}}
	if got := r.readingCoverage(ctx); got.Status != "complete" || got.CompleteFiles != 1 {
		t.Fatalf("空文件读取未确认：%+v", got)
	}
	// 待处理正文和完整覆盖数据留在原记录，模型提示只省略完整条目并明确数量。
	r.state.Spec.ReadScope = nil
	manifest := schema.DiscoveryReceipt{ManifestID: "many", Path: "many", NameGlob: "*.md"}
	if err := os.Mkdir(filepath.Join(dir, "many"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		path := fmt.Sprintf("many/%02d.md", i)
		writeFixture(t, dir, path, "未读\n")
		manifest.Files = append(manifest.Files, schema.DiscoveredFile{Path: path, Bytes: 7})
	}
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{manifest})
	context := r.readingContext(ctx)
	if !strings.Contains(context, "omitted_incomplete_files") || len(context) > 16*1024 {
		t.Fatalf("缺口提示无界或未标省略：%d %s", len(context), context)
	}
	if got := r.readingCoverage(ctx); got.TotalFiles != 31 {
		t.Fatalf("省略提示丢失完整状态：%+v", got)
	}
	encoded, _ := json.Marshal(r.readingCoverage(ctx))
	if !strings.Contains(string(encoded), "many/29.md") {
		t.Fatal("完整覆盖状态被截断")
	}
	if text := readingCoverageText(&ReadingCoverage{Status: "complete", CompleteFiles: 3, TotalFiles: 3}); !strings.Contains(text, "历史") || !strings.Contains(text, "缺少任务范围身份") {
		t.Fatalf("旧记录升级了来源声明：%s", text)
	}
}

func TestCoverageV2InternalDirectoryAliasDoesNotBecomeEmptyComplete(t *testing.T) {
	ctx, r, _ := coverageV2Fixture(t, false)
	dir := tools.ScopeFromContext(ctx).WorkDir
	if err := os.Symlink("01资料包", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"alias"}, NameGlob: "*.md"}
	result := NewExplorerRegistry(dir).Execute(ctx, schema.ToolCall{ID: "find-alias", Name: "find_files", Arguments: json.RawMessage(`{"path":"alias","name_glob":"*.md"}`)})
	if result.Status != schema.ToolSucceeded || result.Discovery == nil {
		t.Fatalf("内部目录别名发现失败：%+v", result)
	}
	rememberCoverageManifests(ctx, r, []schema.DiscoveryReceipt{*result.Discovery})
	got := r.readingCoverage(ctx)
	if got.TotalFiles != 3 || got.CompleteFiles != 3 || got.Status != "complete" {
		t.Fatalf("别名来源成员被省略：%+v", got)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	r.state.Spec.ReadScope = &ReadScopeSpec{Version: 1, Paths: []string{"outside"}, NameGlob: "*.md"}
	if got := r.readingCoverage(ctx); got.Status != "unknown" || len(got.UnresolvedPaths) != 1 {
		t.Fatalf("越界别名补造了完成：%+v", got)
	}
}
