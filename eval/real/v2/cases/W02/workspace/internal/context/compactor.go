package context

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// Compactor 按请求材料的字节预算筛选上下文。
type Compactor struct {
	MaxChars       int
	RetainLastMsgs int
}

func NewCompactor(maxChars int, retainLastMsgs int) *Compactor {
	return &Compactor{MaxChars: maxChars, RetainLastMsgs: retainLastMsgs}
}

// Compact 为旧调用者保留软预算行为。需要硬上限时使用 CompactWithin。
func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	size, err := contextSize(msgs, nil)
	if err == nil && size <= c.MaxChars {
		return msgs
	}
	return c.mask(msgs)
}

// CompactWithin 保留系统提示、最近的用户输入及完整工具批次，并限制请求材料大小。
func (c *Compactor) CompactWithin(msgs []schema.Message, tools []schema.ToolDefinition) ([]schema.Message, error) {
	groups, err := messageGroups(msgs)
	if err != nil {
		return nil, err
	}
	withPrefix := func(input []schema.Message) []schema.Message {
		for i, msg := range input {
			if msg.Role == schema.RoleSystem {
				continue
			}
			if msg.Role == schema.RoleUser && msg.ToolCallID == "" {
				return input
			}
			output := append([]schema.Message(nil), input[:i]...)
			output = append(output, schema.Message{Role: schema.RoleUser, Content: syntheticContextNotice})
			return append(output, input[i:]...)
		}
		return input
	}
	withinBudget := func(input []schema.Message) (bool, error) {
		size, err := contextSize(input, tools)
		return size <= c.MaxChars, err
	}
	candidate := withPrefix(msgs)
	if fits, err := withinBudget(candidate); fits || err != nil {
		return candidate, err
	}
	compacted := c.mask(msgs)
	candidate = withPrefix(compacted)
	if fits, err := withinBudget(candidate); fits || err != nil {
		return candidate, err
	}

	latestUser := -1
	for i, group := range groups {
		msg := compacted[group.start]
		if msg.Role == schema.RoleUser && msg.ToolCallID == "" {
			latestUser = i
		}
	}
	removed := make([]bool, len(groups))
	for i, group := range groups {
		if i == latestUser || compacted[group.start].Role == schema.RoleSystem {
			continue
		}
		removed[i] = true
		var candidate []schema.Message
		for j, remaining := range groups {
			if !removed[j] {
				candidate = append(candidate, compacted[remaining.start:remaining.end]...)
			}
		}
		candidate = withPrefix(candidate)
		if fits, err := withinBudget(candidate); fits || err != nil {
			return candidate, err
		}
	}
	return nil, fmt.Errorf("必要上下文与工具定义超过 %d 字节，无法继续请求模型", c.MaxChars)
}

type messageGroup struct{ start, end int }

// messageGroups 将助手工具调用和它的所有观察值视为一个不可拆分的组。
func messageGroups(msgs []schema.Message) ([]messageGroup, error) {
	groups := make([]messageGroup, 0, len(msgs))
	for i := 0; i < len(msgs); {
		msg := msgs[i]
		if msg.ToolCallID != "" {
			return nil, fmt.Errorf("上下文中有孤立的工具结果")
		}
		end := i + 1
		if msg.Role == schema.RoleAssistant && len(msg.ToolCalls) > 0 {
			pending := make(map[string]bool, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				if call.ID == "" || pending[call.ID] {
					return nil, fmt.Errorf("上下文中有空白或重复的工具调用 ID")
				}
				pending[call.ID] = true
			}
			for len(pending) > 0 {
				if end >= len(msgs) || msgs[end].Role != schema.RoleUser || !pending[msgs[end].ToolCallID] {
					return nil, fmt.Errorf("上下文中的工具调用缺少匹配结果")
				}
				delete(pending, msgs[end].ToolCallID)
				end++
			}
		}
		groups = append(groups, messageGroup{start: i, end: end})
		i = end
	}
	return groups, nil
}

func (c *Compactor) mask(msgs []schema.Message) []schema.Message {
	compacted := append([]schema.Message(nil), msgs...)
	protectFrom := len(msgs) - c.RetainLastMsgs
	for i := range compacted {
		msg := &compacted[i]
		switch {
		case msg.Role == schema.RoleUser && msg.ToolCallID != "":
			if i < protectFrom && len(msg.Content) > 200 {
				msg.Content = fmt.Sprintf("[早期工具输出已折叠，原始大小 %d 字节]", len(msg.Content))
			} else if len(msg.Content) > 1000 {
				text := strings.ToValidUTF8(msg.Content, "�")
				if len(text) <= 1000 {
					msg.Content = text
					continue
				}
				head, tail := 500, len(text)-500
				for !utf8.RuneStart(text[head]) {
					head--
				}
				for !utf8.RuneStart(text[tail]) {
					tail++
				}
				msg.Content = fmt.Sprintf("%s\n[中间 %d 字节已折叠]\n%s", text[:head], tail-head, text[tail:])
			}
		case msg.Role == schema.RoleAssistant && i < protectFrom && len(msg.Content) > 200:
			msg.Content = "[早期助手文本已折叠]"
			// 仍需回传工具结果时保留 Responses 输出项，随后只按完整批次移除。
			if len(msg.ToolCalls) == 0 {
				msg.ResponseItems = nil
			}
		}
	}
	return compacted
}

func contextSize(msgs []schema.Message, tools []schema.ToolDefinition) (int, error) {
	type serializedCall struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type serializedMessage struct {
		schema.Message
		ToolCalls []serializedCall `json:"tool_calls,omitempty"`
	}
	items := make([]serializedMessage, 0, len(msgs))
	for _, msg := range msgs {
		item := serializedMessage{Message: msg}
		for _, call := range msg.ToolCalls {
			// 模型有时生成无效参数。按原始文本计量，以便工具仍能返回可纠正的错误。
			item.ToolCalls = append(item.ToolCalls, serializedCall{ID: call.ID, Name: call.Name, Arguments: string(call.Arguments)})
		}
		items = append(items, item)
	}
	body, err := json.Marshal(struct {
		Messages []serializedMessage     `json:"messages"`
		Tools    []schema.ToolDefinition `json:"tools"`
	}{items, tools})
	if err != nil {
		return 0, fmt.Errorf("无法计算模型请求材料大小: %w", err)
	}
	return len(body), nil
}
