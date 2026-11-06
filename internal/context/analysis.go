package context

import (
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// CompactAnalysisWithin 只核对配对与硬预算，不用头尾掩蔽或消息数窗口丢弃未处理材料。
// 证据笔记与旧正文的安全折叠由引擎按结构化来源完成。
func CompactAnalysisWithin(msgs []schema.Message, tools []schema.ToolDefinition, maxBytes int) ([]schema.Message, error) {
	if _, err := messageGroups(msgs); err != nil {
		return nil, err
	}
	result := append([]schema.Message(nil), msgs...)
	for i, msg := range result {
		if msg.Role == schema.RoleSystem {
			continue
		}
		if msg.Role != schema.RoleUser || msg.ToolCallID != "" {
			result = append(result[:i:i], append([]schema.Message{{Role: schema.RoleUser, Content: syntheticContextNotice}}, result[i:]...)...)
		}
		break
	}
	size, err := contextSize(result, tools)
	if err != nil {
		return nil, err
	}
	if size > maxBytes {
		return nil, fmt.Errorf("必要分析上下文与工具定义共 %d 字节，超过硬上限 %d；未丢弃尚未处理的材料", size, maxBytes)
	}
	return result, nil
}
