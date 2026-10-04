package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// responseUsage 只有两个计数字段完整且合法时才把账单标记为已知。
// SDK 的整数零值不能区分供应商返回零与供应商未返回字段。
func responseUsage(rawJSON, inputField, outputField string) *schema.Usage {
	var raw struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal([]byte(rawJSON), &raw) != nil {
		return nil
	}
	var input, output *int
	if json.Unmarshal(raw.Usage[inputField], &input) != nil || json.Unmarshal(raw.Usage[outputField], &output) != nil || input == nil || output == nil || *input < 0 || *output < 0 {
		return nil
	}
	return &schema.Usage{PromptTokens: *input, CompletionTokens: *output}
}

func validateResponse(msg *schema.Message) error {
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
		return fmt.Errorf("API 返回了空的 assistant 响应")
	}
	seen := make(map[string]bool)
	for _, call := range msg.ToolCalls {
		if call.ID == "" || call.Name == "" || seen[call.ID] {
			return fmt.Errorf("API 返回了缺少标识、名称或重复标识的工具调用")
		}
		seen[call.ID] = true
	}
	return nil
}
