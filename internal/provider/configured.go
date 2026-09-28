package provider

import (
	"fmt"
	"os"
)

const (
	DefaultGatewayURL = "https://syaiapihub.shuyun.cn/v1/"
	ZhipuURL          = "https://open.bigmodel.cn/api/paas/v4/"
	DeepseekOpenAIURL = "https://api.deepseek.com"
	DeepseekClaudeURL = "https://api.deepseek.com/anthropic"
)

// DefaultURL 集中保存现有入口的供应商地址。
func DefaultURL(kind string) string {
	switch kind {
	case "openai":
		return DefaultGatewayURL
	case "zhipu-openai", "zhipu-anthropic":
		return ZhipuURL
	case "deepseek-openai":
		return DeepseekOpenAIURL
	case "deepseek-anthropic":
		return DeepseekClaudeURL
	default:
		return ""
	}
}

// NewConfigured 在构造时一次性读取对应密钥，并把错误交给入口处理。
func NewConfigured(kind, model, baseURL string) (LLMProvider, error) {
	var keyName string
	switch kind {
	case "openai":
		keyName = "OPENAI_API_KEY"
	case "zhipu-openai", "zhipu-anthropic":
		keyName = "ZHIPU_API_KEY"
	case "deepseek-openai", "deepseek-anthropic":
		keyName = "DEEPSEEK_API_KEY"
	default:
		return nil, fmt.Errorf("不支持的 Provider: %s", kind)
	}
	key := os.Getenv(keyName)
	if key == "" {
		return nil, fmt.Errorf("请设置 %s 环境变量", keyName)
	}
	if model == "" {
		return nil, fmt.Errorf("模型不能为空")
	}
	if baseURL == "" {
		baseURL = DefaultURL(kind)
	}
	switch kind {
	case "openai", "zhipu-openai", "deepseek-openai":
		return newOpenAIProvider(model, baseURL, key, kind == "openai"), nil
	default:
		return newClaudeProvider(model, baseURL, key), nil
	}
}
