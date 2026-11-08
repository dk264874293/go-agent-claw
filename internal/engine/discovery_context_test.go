package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestDiscoveryContextUsesCurrentOwnedLatestManifestWithoutScanning(t *testing.T) {
	ctx := observe.WithAgent(context.Background(), "root", "")
	r := &runState{state: taskState{Spec: TaskSpec{ReadContextVersion: 2}}, discoveryOwners: map[string]string{"old": "root", "new": "root", "foreign": "child"}}
	r.discoveries = []schema.DiscoveryReceipt{
		{ManifestID: "old", Path: "missing-directory", NameGlob: "*.md", Files: []schema.DiscoveredFile{{Path: "old-secret-name.md"}}},
		{ManifestID: "new", Path: "missing-directory", NameGlob: "*.md", Truncated: true, Unreadable: 1},
		{ManifestID: "foreign", Path: "foreign-private-path", NameGlob: "*.md"},
	}
	got := r.discoveryContext(ctx)
	if !strings.Contains(got, `"manifest_id":"new"`) || !strings.Contains(got, `"discovery_complete":false`) || strings.Contains(got, "old-secret-name") || strings.Contains(got, "foreign-private-path") {
		t.Fatalf("清单范围或刷新语义错误: %s", got)
	}
	// 上下文构建不能依赖这些并不存在的目录，更不能补扫描产生新清单。
	if len(r.discoveries) != 3 {
		t.Fatal("发现投影改变了原记录")
	}
	for _, v := range []int{0, 1} {
		r.state.Spec.ReadContextVersion = v
		if r.discoveryContext(ctx) != "" {
			t.Fatal("历史任务被升级新发现策略")
		}
	}
}

func TestDiscoveryContextBoundsWholeManifestEntries(t *testing.T) {
	ctx := observe.WithAgent(context.Background(), "root", "")
	r := &runState{state: taskState{Spec: TaskSpec{ReadContextVersion: 2}}, discoveryOwners: map[string]string{}}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("manifest-%02d", i)
		r.discoveryOwners[id] = "root"
		r.discoveries = append(r.discoveries, schema.DiscoveryReceipt{ManifestID: id, Path: fmt.Sprintf("folder-%02d", i), NameGlob: "*.md"})
	}
	got := r.discoveryContext(ctx)
	if strings.Count(got, `"manifest_id"`) != 6 || !strings.Contains(got, `"omitted":6`) || len(got) > 3500 {
		t.Fatalf("发现投影未按完整项限长: %s", got)
	}
}
