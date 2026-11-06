package runtimeenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

func TestChannelBaseURLBuildDefaultsAndRuntimeOverrides(t *testing.T) {
	for _, channel := range []string{"OPENAI", "ZHIPU", "DEEPSEEK"} {
		for _, mode := range []string{"构建默认", "运行时覆盖", "显式空值", ".env覆盖"} {
			t.Run(channel+"/"+mode, func(t *testing.T) {
				cleanProxyEnvironment(t)
				DefaultOpenAIBaseURL = "https://openai.example.invalid/v1/"
				DefaultZhipuBaseURL = "https://zhipu.example.invalid/api/paas/v4/"
				DefaultDeepseekBaseURL = "https://deepseek.example.invalid/v1/"
				name := channel + "_BASE_URL"
				want := map[string]string{
					"OPENAI": DefaultOpenAIBaseURL, "ZHIPU": DefaultZhipuBaseURL, "DEEPSEEK": DefaultDeepseekBaseURL,
				}[channel]
				switch mode {
				case "运行时覆盖":
					want = "https://runtime.example.invalid/custom/v1/"
					t.Setenv(name, want)
				case "显式空值":
					want = ""
					t.Setenv(name, want)
				case ".env覆盖":
					want = "https://dotenv.example.invalid/custom/v1/"
					path := filepath.Join(t.TempDir(), ".env")
					if err := os.WriteFile(path, []byte(name+"="+want+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := godotenv.Overload(path); err != nil {
						t.Fatal(err)
					}
				}
				if err := ApplyDefaults(); err != nil {
					t.Fatal(err)
				}
				if value, exists := os.LookupEnv(name); !exists || value != want {
					t.Fatalf("%s=%q，预期 %q", name, value, want)
				}
			})
		}
	}
}
