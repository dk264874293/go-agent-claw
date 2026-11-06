package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestCompactWithinDropsWholeToolBatchAndKeepsGoal(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "任务目标：保持调用顺序"},
		{Role: schema.RoleUser, Content: "用户边界"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "old", Name: "read_file", Arguments: []byte(strings.Repeat("a", 2000))}}},
		{Role: schema.RoleUser, ToolCallID: "old", Content: "旧观察"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "new", Name: "read_file", Arguments: []byte(`{"path":"doc"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "new", Content: "最近观察"},
	}
	tools := []schema.ToolDefinition{{Name: "read_file", Description: "读取文件"}}
	want := []schema.Message{msgs[0], msgs[1], msgs[4], msgs[5]}
	limit, err := contextSize(want, tools)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewCompactor(limit, 10).CompactWithin(msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Content != msgs[0].Content || got[1].Content != msgs[1].Content ||
		len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "new" || got[3].ToolCallID != "new" {
		t.Fatalf("上下文丢失目标或破坏工具配对: %#v", got)
	}
	if _, err := NewCompactor(limit, 10).CompactWithin(msgs, []schema.ToolDefinition{
		{Name: "read_file", Description: strings.Repeat("字", 1000)},
	}); err == nil {
		t.Fatal("工具定义超预算时仍允许模型请求")
	}
}

func TestCompactorPreservesUTF8(t *testing.T) {
	for _, content := range []string{strings.Repeat("中", 1000), strings.Repeat("\xff", 1100)} {
		msgs := []schema.Message{{Role: schema.RoleUser, ToolCallID: "call", Content: content}}
		out := NewCompactor(100, 10).Compact(msgs)
		if !utf8.ValidString(out[0].Content) || out[0].ToolCallID != "call" || out[0].Content == content {
			t.Fatalf("截断后的观察值不合法: %#v", out)
		}
	}
}

func TestPromptComposerIndexesSkillsWithoutLoadingBody(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".claw", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: 测试技能\ndescription: 只展示元数据\n---\n不应自动加载的正文\n"), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(external, []byte("工作区外项目规范"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	prompt := NewPromptComposer(root, true).Build().Content
	if !strings.Contains(prompt, "测试技能") || !strings.Contains(prompt, ".claw/skills/demo/SKILL.md") {
		t.Fatalf("技能索引缺失: %s", prompt)
	}
	for _, invalid := range []string{"不应自动加载的正文", "工作区外项目规范", "PLAN.md", "TODO.md"} {
		if strings.Contains(prompt, invalid) {
			t.Fatalf("意外加载内容 %q", invalid)
		}
	}
}
