package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestAcceptanceMulticurrencyAndClear(t *testing.T) {
	s := NewSession("验收", t.TempDir())
	s.Append(schema.Message{Role: schema.RoleUser, Content: "旧消息"})
	s.RecordUsage(&schema.Usage{PromptTokens: 3, CompletionTokens: 2}, "USD", 1250000, true)
	s.RecordUsage(&schema.Usage{PromptTokens: 5, CompletionTokens: 4}, "CNY", 2500000, true)
	s.RecordUsage(nil, "", 0, false)
	s.RecordModelCall(ModelCall{RunID: "保留账单"})
	u := s.UsageSnapshot()
	if u.CostLabel() != "CNY 2.500000；USD 1.250000；1 笔费用未知" {
		t.Fatal(u.CostLabel())
	}
	u.KnownCostMicros["USD"] = 0
	s.ResetHistory()
	u = s.UsageSnapshot()
	if s.MessageCount() != 0 || u.KnownCostMicros["USD"] != 1250000 || u.TotalPromptTokens != 8 || u.TotalCompletionTokens != 6 || u.UnknownUsageRequests != 1 || len(s.ModelCalls()) != 1 {
		t.Fatalf("清空对话丢失账单或副本不独立: %+v", u)
	}
}

func acceptanceSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, ".claw", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceSkillLimits(t *testing.T) {
	t.Run("每文件及UTF8", func(t *testing.T) {
		dir := t.TempDir()
		acceptanceSkill(t, dir, "one", strings.Repeat("中", 6000)+"不得读取尾部")
		text := NewSkillLoader(dir).LoadAll()
		if !utf8.ValidString(text) || strings.Contains(text, "不得读取尾部") || len(text) > 17*1024 {
			t.Fatalf("技能截断错误: valid=%v bytes=%d", utf8.ValidString(text), len(text))
		}
	})
	t.Run("总长度", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			acceptanceSkill(t, dir, fmt.Sprint(i), strings.Repeat("中", 6000))
		}
		text := NewSkillLoader(dir).LoadAll()
		if !utf8.ValidString(text) || len(text) > 64*1024+200 || len(text) < 60*1024 {
			t.Fatalf("技能总长度: %d", len(text))
		}
	})
	t.Run("文件数量", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 40; i++ {
			acceptanceSkill(t, dir, fmt.Sprint(i), "数量标记")
		}
		if n := strings.Count(NewSkillLoader(dir).LoadAll(), "数量标记"); n != 32 {
			t.Fatalf("技能数量: %d", n)
		}
	})
	t.Run("深度", func(t *testing.T) {
		dir := t.TempDir()
		acceptanceSkill(t, dir, strings.Repeat("d/", 8), "边界可见")
		acceptanceSkill(t, dir, strings.Repeat("d/", 9), "越界不可见")
		text := NewSkillLoader(dir).LoadAll()
		if !strings.Contains(text, "边界可见") || strings.Contains(text, "越界不可见") {
			t.Fatal("目录深度约束失效")
		}
	})
	t.Run("扫描数量", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, ".claw", "skills")
		if err := os.MkdirAll(base, 0700); err != nil {
			t.Fatal(err)
		}
		// 每个分支含 40 个非技能文件，确保访问预算先于 32 个技能上限生效。
		for i := 0; i < 12; i++ {
			name := filepath.Join(base, fmt.Sprintf("d%03d", i))
			if err := os.MkdirAll(name, 0700); err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 40; j++ {
				if err := os.WriteFile(filepath.Join(name, fmt.Sprint(j)), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(name, "SKILL.md"), []byte("扫描标记"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if n := strings.Count(NewSkillLoader(dir).LoadAll(), "扫描标记"); n == 0 || n > 7 {
			t.Fatalf("扫描预算未生效: %d", n)
		}
	})
	t.Run("项目指南UTF8", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(strings.Repeat("中", 12000)+"不可加载尾部"), 0600); err != nil {
			t.Fatal(err)
		}
		text := NewPromptComposer(dir, false).Build().Content
		if !utf8.ValidString(text) || strings.Contains(text, "不可加载尾部") || len(text) > 40000 {
			t.Fatal("项目指南截断错误")
		}
	})
}
