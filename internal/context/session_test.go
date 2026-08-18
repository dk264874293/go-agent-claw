package context

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// buildHistory 模拟主循环写入的历史结构：
// 首条是任务指令 (普通 user)，之后每一轮 Turn 追加 assistant(tool_calls) → tool 结果。
func buildHistory(turns int) []schema.Message {
	msgs := []schema.Message{{Role: schema.RoleUser, Content: "修复并发问题"}}
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs,
			schema.Message{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{{
					ID:        id,
					Name:      "bash",
					Arguments: json.RawMessage(`{"command":"ls"}`),
				}},
			},
			schema.Message{Role: schema.RoleUser, ToolCallID: id, Content: "ok"},
		)
	}
	return msgs
}

// assertValidSequence 校验大模型 API 的两道硬性约束：
// 1. 首条消息必须是普通 user 消息 (智谱 GLM 实测 system 后直接跟 assistant 会报 400 code=1214)；
// 2. 每条 tool 结果的 ToolCallID 必须能在其之前出现的某条 assistant.tool_calls 中找到宿主。
func assertValidSequence(t *testing.T, msgs []schema.Message) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("工作记忆为空")
	}
	if first := msgs[0]; first.Role != schema.RoleUser || first.ToolCallID != "" {
		t.Fatalf("首条消息必须是普通 user 消息，实际为 role=%s toolCallID=%q", first.Role, first.ToolCallID)
	}
	seenCalls := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			seenCalls[tc.ID] = true
		}
		if m.ToolCallID != "" && !seenCalls[m.ToolCallID] {
			t.Fatalf("工具结果 %s 的宿主 assistant(tool_calls) 不在窗口内，配对断裂", m.ToolCallID)
		}
	}
}

func TestGetWorkingMemory_UnderLimitReturnsFull(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(3)...) // 7 条

	got := s.GetWorkingMemory(20)
	if len(got) != 7 {
		t.Fatalf("历史未超限应全量返回 7 条，实际 %d 条", len(got))
	}
	assertValidSequence(t, got)
}

// 复现线上崩溃场景：历史 22 条、limit 20，旧实现会把孤儿 tool 结果丢弃，
// 导致序列以 assistant 开头被智谱 API 拒绝 (1214)。
func TestGetWorkingMemory_WindowSlidesBackToUserBoundary(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(10)...) // 21 条，结构与崩溃会话一致

	got := s.GetWorkingMemory(20)
	// 起点 (索引1) 落在 Turn-1 的 tool 结果上，必须回退到索引0 的任务指令 → 全量 21 条
	if len(got) != 21 {
		t.Fatalf("期望回退后全量返回 21 条，实际 %d 条", len(got))
	}
	assertValidSequence(t, got)
}

// 会话中段的 reminder (普通 user 消息) 是更近的块边界，
// 回退应停在它身上，而不是一路退到最初的任务指令。
func TestGetWorkingMemory_ReminderActsAsAnchor(t *testing.T) {
	hist := buildHistory(14) // 29 条
	reminder := schema.Message{Role: schema.RoleUser, Content: "[SYSTEM REMINDER] 打破死循环"}
	withReminder := append(append(append([]schema.Message{}, hist[:9]...), reminder), hist[9:]...) // 共 30 条，reminder 在索引 9

	s := NewSession("s1", "/tmp")
	s.Append(withReminder...)

	got := s.GetWorkingMemory(20)
	// 起点 (索引10) 落在 assistant 上，回退到索引9 的 reminder 即停 → 返回 21 条
	if len(got) != 21 {
		t.Fatalf("期望回退到 reminder 锚点后返回 21 条，实际 %d 条", len(got))
	}
	if got[0].Content != reminder.Content {
		t.Fatalf("期望首条为 reminder 消息，实际为 %q", got[0].Content)
	}
	assertValidSequence(t, got)
}

// 回退补数恰好等于预算上限 (3 条) 时，仍应走方案 B 直接补齐。
func TestGetWorkingMemory_BacktrackAtBudgetBoundary(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(11)...) // 23 条，起点索引3 距任务指令恰好 3 条

	got := s.GetWorkingMemory(20)
	if len(got) != 23 {
		t.Fatalf("回退补数为 3 条 (未超预算) 应走方案 B 全量返回 23 条，实际 %d 条", len(got))
	}
	if got[0].Content != "修复并发问题" {
		t.Fatalf("方案 B 应以原始任务指令开头，实际为 %q", got[0].Content)
	}
	assertValidSequence(t, got)
}

// 回退补数超过 3 条且窗口头部是 assistant 时，应改走方案 A：
// 注入合成 user 前缀垫场，保持窗口大小不再向远古历史扩张。
func TestGetWorkingMemory_BacktrackOverBudgetUsesSyntheticPrefix(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(14)...) // 29 条，起点索引9 是 assistant，回退到任务指令需 9 条 > 3

	got := s.GetWorkingMemory(20)
	if len(got) != 21 {
		t.Fatalf("方案 A 应返回 合成前缀1条 + 原窗口20条 = 21 条，实际 %d 条", len(got))
	}
	if got[0].Content != syntheticContextNotice {
		t.Fatalf("首条应为合成 user 前缀，实际为 %q", got[0].Content)
	}
	if got[1].Role != schema.RoleAssistant {
		t.Fatalf("合成前缀之后应紧跟原窗口头部的 assistant 消息，实际为 %s", got[1].Role)
	}
	assertValidSequence(t, got)
}

// 回退超预算且窗口头部是孤儿 tool 结果时，方案 A 必须将其剔除
// (宿主 assistant 已被截掉，留着必破配对)，再垫合成前缀。
func TestGetWorkingMemory_SyntheticPrefixDropsOrphanToolResults(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(15)...) // 31 条，limit 19 → 起点索引12 恰是 tool 结果

	got := s.GetWorkingMemory(19)
	// 剔除索引12 的孤儿后从索引13 (assistant) 起算: 合成前缀1 + 18 = 19 条
	if len(got) != 19 {
		t.Fatalf("方案 A 剔除孤儿后应返回 合成前缀1条 + 18条 = 19 条，实际 %d 条", len(got))
	}
	if got[0].Content != syntheticContextNotice {
		t.Fatalf("首条应为合成 user 前缀，实际为 %q", got[0].Content)
	}
	if got[1].Role != schema.RoleAssistant {
		t.Fatalf("孤儿剔除后应从 assistant 起算，实际为 %s", got[1].Role)
	}
	assertValidSequence(t, got)
}

// 返回值必须是拷贝：调用方修改结果不得污染 Session 内部历史。
func TestGetWorkingMemory_ReturnsCopy(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(buildHistory(10)...)

	got := s.GetWorkingMemory(20)
	got[0].Content = "被污染"
	if again := s.GetWorkingMemory(20); again[0].Content == "被污染" {
		t.Fatal("GetWorkingMemory 泄露了内部切片引用，调用方修改污染了会话历史")
	}
}

// /clear 语义：MessageCount 随 Append 递增；ResetHistory 清空上下文但保留账单，
// 且清空后的会话可继续正常追加（GetWorkingMemory 首条仍是普通 user 消息）。
func TestSession_ResetHistoryAndMessageCount(t *testing.T) {
	s := NewSession("s1", "/tmp")
	if n := s.MessageCount(); n != 0 {
		t.Fatalf("新会话应为 0 条消息，实际 %d 条", n)
	}

	s.Append(buildHistory(3)...) // 7 条
	s.RecordUsage(100, 200, 0.5)
	if n := s.MessageCount(); n != 7 {
		t.Fatalf("追加 7 条后应为 7 条消息，实际 %d 条", n)
	}

	s.ResetHistory()
	if n := s.MessageCount(); n != 0 {
		t.Fatalf("清空后应为 0 条消息，实际 %d 条", n)
	}
	if s.TotalPromptTokens != 100 || s.TotalCompletionTokens != 200 || s.TotalCostCNY != 0.5 {
		t.Fatalf("清空上下文不应清零账单，实际: ¥%.6f, In %d, Out %d",
			s.TotalCostCNY, s.TotalPromptTokens, s.TotalCompletionTokens)
	}

	// 清空后继续对话：结构必须仍满足 API 的两道硬性校验
	s.Append(buildHistory(1)...)
	got := s.GetWorkingMemory(20)
	if len(got) != 3 {
		t.Fatalf("清空后重新追加 3 条应全量返回，实际 %d 条", len(got))
	}
	assertValidSequence(t, got)
}
