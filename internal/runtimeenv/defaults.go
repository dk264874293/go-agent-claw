// Package runtimeenv 初始化执行入口的可选环境默认值。
package runtimeenv

import (
	"fmt"
	"os"
)

// 通过 go build -ldflags -X 注入非敏感的网络默认配置。
// 普通构建保持为空，运行时环境变量和 .env 可覆盖构建默认值。
var (
	DefaultHTTPSProxy      string
	DefaultHTTPProxy       string
	DefaultNoProxy         string
	DefaultOpenAIBaseURL   string
	DefaultZhipuBaseURL    string
	DefaultDeepseekBaseURL string
)

// ApplyDefaults 须在加载 .env 后、首次 HTTP 请求前调用。
// 运行时的显式空值也优先于构建默认值，代理变量兼容大小写。
func ApplyDefaults() error {
	for _, setting := range []struct {
		name, alias, value string
	}{
		{"HTTPS_PROXY", "https_proxy", DefaultHTTPSProxy},
		{"HTTP_PROXY", "http_proxy", DefaultHTTPProxy},
		{"NO_PROXY", "no_proxy", DefaultNoProxy},
		{"OPENAI_BASE_URL", "", DefaultOpenAIBaseURL},
		{"ZHIPU_BASE_URL", "", DefaultZhipuBaseURL},
		{"DEEPSEEK_BASE_URL", "", DefaultDeepseekBaseURL},
	} {
		if setting.value == "" {
			continue
		}
		if _, exists := os.LookupEnv(setting.name); exists {
			continue
		}
		if setting.alias != "" {
			if _, exists := os.LookupEnv(setting.alias); exists {
				continue
			}
		}
		if err := os.Setenv(setting.name, setting.value); err != nil {
			return fmt.Errorf("无法设置构建默认配置 %s: %w", setting.name, err)
		}
	}
	return nil
}
