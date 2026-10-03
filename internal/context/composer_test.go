package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalPromptSourcesAreBoundedAndRooted(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "instructions")
	if err := os.WriteFile(secret, []byte("工作区外测试秘密"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, ".claw", "skills", "test")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	prompt := NewPromptComposer(dir, false).Build().Content
	if strings.Contains(prompt, "工作区外测试秘密") {
		t.Fatal("外部链接被加载")
	}
	if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("用户配置的项目规则"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: 测试技能\ndescription: 测试\n---\n"+strings.Repeat("内容", 20000)), 0600); err != nil {
		t.Fatal(err)
	}
	prompt = NewPromptComposer(dir, false).Build().Content
	if !strings.Contains(prompt, "外部项目指南") || !strings.Contains(prompt, "用户配置的项目规则") || !strings.Contains(prompt, "外部技能来源") || len(prompt) > 100000 {
		t.Fatal("来源标记或长度约束错误")
	}
}
