package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func delegationFixture(t *testing.T, policy string, count int) (context.Context, *runState, schema.DiscoveryReceipt) {
	t.Helper()
	dir := t.TempDir()
	ctx := tools.WithScope(observe.WithAgent(context.Background(), "root", ""), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir})
	spec := TaskSpec{Mode: tools.ModeReadOnly, Kind: TaskAnswer, Delegation: policy}
	ledger, err := initializeDelegation(&spec)
	if err != nil {
		t.Fatal(err)
	}
	r := &runState{state: taskState{Version: 1, Spec: spec, Delegation: ledger, Limits: Budget{}.defaults()}, started: time.Now(), runID: "origin"}
	d := schema.DiscoveryReceipt{ManifestID: "manifest", Path: ".", NameGlob: "*.md"}
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("doc%02d.md", i)
		if err := os.WriteFile(filepath.Join(dir, path), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		d.Files = append(d.Files, schema.DiscoveredFile{Path: path, Bytes: 1})
	}
	r.mu.Lock()
	r.rememberDiscoveryLocked(ctx, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &d})
	r.mu.Unlock()
	return ctx, r, d
}

func TestDelegationAutoUsesCompletePrimaryManifest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count      int
		bytes      int64
		truncated  bool
		unreadable int
		want       bool
	}{
		{"本例小任务", 15, 87105, false, 0, false},
		{"32文件128KiB边界", 32, 128 * 1024, false, 0, false},
		{"文件数超出", 33, 33, false, 0, true},
		{"字节数超出", 1, 128*1024 + 1, false, 0, true},
		{"截断未知", 33, 33, true, 0, false},
		{"不可读未知", 33, 33, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r, d := delegationFixture(t, "auto", tc.count)
			d.Truncated = tc.truncated
			d.Unreadable = tc.unreadable
			d.Files[0].Bytes = tc.bytes - int64(tc.count-1)
			r.discoveries[0] = d
			if got := r.delegationVisible(ctx); got != tc.want {
				t.Fatalf("visible=%t want=%t", got, tc.want)
			}
		})
	}
	ctx, r, d := delegationFixture(t, "auto", 15)
	other := d
	other.ManifestID = "large-other"
	other.NameGlob = "*.png"
	other.Files = append([]schema.DiscoveredFile(nil), d.Files...)
	other.Files[0].Bytes = 1000000
	r.mu.Lock()
	r.rememberDiscoveryLocked(ctx, schema.ToolResult{Status: schema.ToolSucceeded, Discovery: &other})
	r.mu.Unlock()
	if r.delegationVisible(ctx) {
		t.Fatal("大图片清单改变了小Markdown任务的准入")
	}
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"doc00.md"}, ExpectedEvidence: "发现"}, "child"); err == nil {
		t.Fatal("隐藏工具后仍可绕过实际准入")
	}
}

func TestDelegationAutoAdmitsDistinctUnreadFilesAndPersistsBoundary(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "auto", 33)
	for i, path := range []string{"doc00.md", "doc01.md"} {
		if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{path}, ExpectedEvidence: "核对文档约束"}, fmt.Sprintf("child%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if r.delegationVisible(ctx) {
		t.Fatal("达到2个累计子任务仍展示工具")
	}
	raw, _ := json.Marshal(r.state)
	var state taskState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDelegationState(state); err != nil {
		t.Fatal(err)
	}
	r.state = state
	r.runID = "resumed"
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"doc02.md"}, ExpectedEvidence: "恢复后调查"}, "third"); err == nil {
		t.Fatal("恢复重置了累计准入")
	}
	if got := r.admittedSubtaskPaths("child0"); len(got) != 1 || got[0] != "doc00.md" {
		t.Fatalf("丢失实际收窄路径: %v", got)
	}
}

func TestDelegationRejectsOverlapAlreadyReadAndUnboundedScope(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "auto", 33)
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"doc00.md"}, ExpectedEvidence: "核对"}, "child"); err != nil {
		t.Fatal(err)
	}
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"doc00.md"}, ExpectedEvidence: "再核对"}, "overlap"); err == nil {
		t.Fatal("允许重叠自动委派")
	}
	version, err := tools.FileVersion(ctx, "doc01.md")
	if err != nil {
		t.Fatal(err)
	}
	r.reads.receipts = append(r.reads.receipts, schema.ReadReceipt{Path: "doc01.md", Version: version, ByteEnd: 1, TotalBytes: 1, AgentID: "root"})
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"doc01.md"}, ExpectedEvidence: "已读文件"}, "read"); err == nil {
		t.Fatal("再次委派完整读取文件")
	}
	for _, spec := range []tools.SubtaskSpec{{ExpectedEvidence: "缺少范围"}, {AllowedPaths: []string{"doc02.md"}}, {AllowedPaths: []string{"../outside"}, ExpectedEvidence: "越权"}} {
		if err := r.admitSubtask(ctx, spec, "invalid"); err == nil {
			t.Fatal("无效范围或交付被准入")
		}
	}
}

func TestDelegationOnStillChecksScopeAndOffLegacyStayClosed(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "on", 1)
	if !r.delegationVisible(ctx) {
		t.Fatal("on未放宽规模")
	}
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{AllowedPaths: []string{"../escape"}}, "outside"); err == nil {
		t.Fatal("on绕过路径权限")
	}
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{}, "child"); err != nil {
		t.Fatal(err)
	}
	nested := tools.ScopeFromContext(ctx)
	nested.DisableSubagent = true
	if err := r.admitSubtask(tools.WithScope(ctx, nested), tools.SubtaskSpec{}, "nested"); err == nil {
		t.Fatal("子任务再次委派")
	}
	r.state.Spec.Delegation = "off"
	if r.delegationVisible(ctx) {
		t.Fatal("off仍展示委派")
	}
	r.state.Spec.Delegation = "auto"
	r.state.Delegation = nil
	if r.delegationVisible(ctx) {
		t.Fatal("旧记录缺计数时补造准入资格")
	}
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{}, "legacy"); err == nil {
		t.Fatal("旧记录实际准入未关闭")
	}
}

func TestDelegationChildBudgetAtomicWithSharedBudget(t *testing.T) {
	ctx, r, _ := delegationFixture(t, "on", 1)
	if err := r.admitSubtask(ctx, tools.SubtaskSpec{}, "child"); err != nil {
		t.Fatal(err)
	}
	child := observe.WithAgent(ctx, "child", "root")
	for _, kind := range []string{"model", "tool"} {
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for range 24 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := r.reserve(child, kind); err == nil {
					accepted.Add(1)
				} else {
					var limit subtaskLimit
					if !errors.As(err, &limit) {
						t.Errorf("局部错误变成根停止: %v", err)
					}
				}
			}()
		}
		wg.Wait()
		want := int32(subtaskModelLimit)
		if kind == "tool" {
			want = subtaskToolLimit
		}
		if accepted.Load() != want {
			t.Fatalf("%s 准入 %d want %d", kind, accepted.Load(), want)
		}
	}
	if r.state.Usage.ModelCalls != 4 || r.state.Usage.ToolCalls != 8 || r.state.Delegation.Subtasks["child"].ModelCalls != 4 || r.state.Delegation.Subtasks["child"].ToolCalls != 8 {
		t.Fatal("共享和局部计数不同步")
	}
	if err := r.reserve(ctx, "model"); err != nil {
		t.Fatalf("局部额度误停根任务: %v", err)
	}
}

func TestDelegationStateValidationAndNonTargetNoop(t *testing.T) {
	spec := TaskSpec{Kind: TaskChange, Mode: tools.ModeTrusted}
	if ledger, err := initializeDelegation(&spec); err != nil || ledger != nil || spec.Delegation != "" {
		t.Fatal("改变了修改任务默认行为")
	}
	ctx, r, _ := delegationFixture(t, "on", 1)
	r.state.Spec = spec
	r.state.Delegation = nil
	called := false
	r.persist = func(string, taskState) error { called = true; return nil }
	if err := r.finishSubtask(ctx, "old", "finished"); err != nil || called {
		t.Fatal("非目标子任务引入额外持久化")
	}
	_, r, _ = delegationFixture(t, "on", 1)
	r.state.Delegation.Subtasks["bad"] = &SubtaskAdmission{AgentID: "bad", OriginRunID: "run", Paths: []string{"."}, ModelCalls: 5, Status: "done"}
	if err := ValidateDelegationState(r.state); err == nil {
		t.Fatal("损坏的局部计数未识别")
	}
}
