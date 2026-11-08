package provider

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

func newCompatibleProvider(model, apiKey, baseURL string) *OpenAIProvider {
	return newCompatibleProviderForChannel(model, apiKey, baseURL, "openai")
}

func newCompatibleProviderForChannel(model, apiKey, baseURL, channel string) *OpenAIProvider {
	options := []option.RequestOption{option.WithHTTPClient(compatibleHTTPClient())}
	if value, exists := os.LookupEnv("OPENAI_ORG_ID"); exists {
		options = append(options, option.WithOrganization(value))
	}
	if value, exists := os.LookupEnv("OPENAI_PROJECT_ID"); exists {
		options = append(options, option.WithProject(value))
	}
	for _, line := range strings.Split(os.Getenv("OPENAI_CUSTOM_HEADERS"), "\n") {
		if key, value, found := strings.Cut(line, ":"); found {
			options = append(options, option.WithHeader(strings.TrimSpace(key), strings.TrimSpace(value)))
		}
	}
	options = append(options, option.WithAPIKey(apiKey), option.WithBaseURL(baseURL), option.WithMaxRetries(0))
	// 公开 Service 构造函数不读取环境变量，避免 SDK 的 OPENAI_BASE_URL
	// 默认选项先解析失败，干扰已经选定的其他渠道或清理过空白的 URL。
	return &OpenAIProvider{
		chat:      openai.NewChatCompletionService(append(append([]option.RequestOption(nil), options...), option.WithMiddleware(requestEncodingObserver(model, channel, "chat_completions")))...),
		responses: responses.NewResponseService(append(append([]option.RequestOption(nil), options...), option.WithMiddleware(requestEncodingObserver(model, channel, "responses")))...),
		model:     model,
		provider:  channel,
	}
}

func compatibleHTTPClient() *http.Client {
	transport := http.DefaultTransport
	if standard, ok := transport.(*http.Transport); ok {
		copy := standard.Clone()
		// 沿用当前 SDK 的响应头等待上限，任务本身的上下文可提前终止请求。
		copy.ResponseHeaderTimeout = 10 * time.Minute
		transport = copy
	}
	return &http.Client{Transport: transport}
}
