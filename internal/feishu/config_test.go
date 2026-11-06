package feishu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventLedgerAndWorkspaceCannotOverlap(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"同一目录", alias},
		{"台账在工作区内部", filepath.Join(alias, "uncreated", "ledger")},
		{"工作区在台账内部", root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := Config{EventLedger: tc.path, Chats: map[string]ChatBinding{
				"群聊": {WorkDir: workspace, AllowedUsers: []string{"用户"}},
			}}
			if err := config.validate(); err == nil || !strings.Contains(err.Error(), "不能重叠") {
				t.Fatalf("配置未拦截台账目录 %q: %v", tc.path, err)
			}
		})
	}
	config := Config{EventLedger: filepath.Join(root, "work-ledger", "uncreated"), Chats: map[string]ChatBinding{
		"群聊": {WorkDir: alias, AllowedUsers: []string{"用户"}},
	}}
	if err := config.validate(); err != nil {
		t.Fatalf("并列的台账与工作区应可配置: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config.EventLedger, filepath.Join(resolved, "work-ledger", "uncreated"); got != want {
		t.Fatalf("台账路径未规范化: %s，期望 %s", got, want)
	}
}
