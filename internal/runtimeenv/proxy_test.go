package runtimeenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

func cleanProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "OPENAI_BASE_URL", "ZHIPU_BASE_URL", "DEEPSEEK_BASE_URL"} {
		value, exists := os.LookupEnv(name)
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	previousHTTPS, previousHTTP, previousNoProxy := DefaultHTTPSProxy, DefaultHTTPProxy, DefaultNoProxy
	previousOpenAI, previousZhipu, previousDeepseek := DefaultOpenAIBaseURL, DefaultZhipuBaseURL, DefaultDeepseekBaseURL
	t.Cleanup(func() {
		DefaultHTTPSProxy, DefaultHTTPProxy, DefaultNoProxy = previousHTTPS, previousHTTP, previousNoProxy
		DefaultOpenAIBaseURL, DefaultZhipuBaseURL, DefaultDeepseekBaseURL = previousOpenAI, previousZhipu, previousDeepseek
	})
	DefaultHTTPSProxy = "http://127.0.0.1:7891"
	DefaultHTTPProxy = "http://127.0.0.1:7892"
	DefaultNoProxy = "localhost,127.0.0.1,::1"
	DefaultOpenAIBaseURL, DefaultZhipuBaseURL, DefaultDeepseekBaseURL = "", "", ""
}

func TestProxyDefaultsPreserveRuntimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		want   string
		exists bool
	}{
		{name: "使用构建默认值", want: "http://127.0.0.1:7891", exists: true},
		{name: "大写配置覆盖", env: map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9991"}, want: "http://127.0.0.1:9991", exists: true},
		{name: "小写配置覆盖", env: map[string]string{"https_proxy": "http://127.0.0.1:9992"}},
		{name: "大写空值关闭默认", env: map[string]string{"HTTPS_PROXY": ""}, exists: true},
		{name: "小写空值关闭默认", env: map[string]string{"https_proxy": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanProxyEnvironment(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			if err := ApplyDefaults(); err != nil {
				t.Fatal(err)
			}
			value, exists := os.LookupEnv("HTTPS_PROXY")
			if value != tc.want || exists != tc.exists {
				t.Fatalf("HTTPS_PROXY=%q，存在=%v", value, exists)
			}
			for name, want := range tc.env {
				if value, exists := os.LookupEnv(name); !exists || value != want {
					t.Fatalf("运行时配置被覆盖: %s=%q", name, value)
				}
			}
			if os.Getenv("HTTP_PROXY") != DefaultHTTPProxy || os.Getenv("NO_PROXY") != DefaultNoProxy {
				t.Fatal("其他代理默认配置缺失")
			}
		})
	}
}

func TestProxyDefaultsAllowDotenvOverride(t *testing.T) {
	cleanProxyEnvironment(t)
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("HTTPS_PROXY=http://127.0.0.1:9981\nHTTP_PROXY=\nNO_PROXY=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := godotenv.Overload(path); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDefaults(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HTTPS_PROXY") != "http://127.0.0.1:9981" || os.Getenv("HTTP_PROXY") != "" || os.Getenv("NO_PROXY") != "" {
		t.Fatal(".env 没有覆盖构建默认值")
	}
}

func TestOrdinaryBuildHasNoProxyDefaults(t *testing.T) {
	cleanProxyEnvironment(t)
	DefaultHTTPSProxy, DefaultHTTPProxy, DefaultNoProxy = "", "", ""
	if err := ApplyDefaults(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if _, exists := os.LookupEnv(name); exists {
			t.Fatalf("普通构建增加了代理配置: %s", name)
		}
	}
}
