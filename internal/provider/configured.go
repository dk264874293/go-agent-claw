package provider

import (
	"fmt"
	"os"
	"strings"
)

const (
	DefaultDeepseekModel = "deepseek-v4.1-flash-1017"
	DefaultModel         = DefaultDeepseekModel
)

// ResolveModelConfig 为默认入口和显式模型选择匹配渠道。
func ResolveModelConfig(channel, model string) (string, string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	model = strings.TrimSpace(model)
	switch channel {
	case "", "auto":
		if model == "" {
			model = DefaultModel
		}
		switch {
		case strings.HasPrefix(strings.ToLower(model), "deepseek"):
			channel = "deepseek"
		case strings.HasPrefix(strings.ToLower(model), "glm-"):
			channel = "zhipu"
		default:
			channel = "openai"
		}
	case "deepseek":
		if model == "" {
			model = DefaultDeepseekModel
		}
	case "openai":
		if model == "" {
			model = DefaultOpenAIModel
		}
	case "zhipu":
		if model == "" {
			return channel, model, fmt.Errorf("请指定 zhipu 渠道的模型名称")
		}
	default:
		return channel, model, fmt.Errorf("未知模型渠道 %q，可选 openai、zhipu、deepseek", channel)
	}
	return channel, model, nil
}

// NewConfiguredProvider 为执行入口选择渠道，校验配置后再创建兼容 Provider。
func NewConfiguredProvider(channel, model string) (LLMProvider, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	model = strings.TrimSpace(model)
	var key string
	var create func(string) *OpenAIProvider
	switch channel {
	case "openai":
		key, create = "OPENAI_API_KEY", NewOpenAIProvider
	case "zhipu":
		key, create = "ZHIPU_API_KEY", NewZhipuOpenAIProvider
	case "deepseek":
		key, create = "DEEPSEEK_API_KEY", NewDeepseekOpenAIProvider
	default:
		return nil, fmt.Errorf("未知模型渠道 %q，可选 openai、zhipu、deepseek", channel)
	}
	if model == "" {
		return nil, fmt.Errorf("请指定 %s 渠道的模型名称", channel)
	}
	if strings.TrimSpace(os.Getenv(key)) == "" {
		return nil, fmt.Errorf("请设置 %s 环境变量", key)
	}
	return create(model), nil
}
