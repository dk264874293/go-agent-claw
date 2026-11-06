package runtimeenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDotenvMissingAndBrokenFiles(t *testing.T) {
	for _, kind := range []string{"不存在", "断链", "解析错误"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			const secret = "private-key-fixture-never-echo"
			switch kind {
			case "断链":
				if err := os.Symlink("missing-config.env", ".env"); err != nil {
					t.Fatal(err)
				}
			case "解析错误":
				if err := os.WriteFile(".env", []byte("DEEPSEEK_API_KEY=\""+secret+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := LoadDotenv(true)
			if (err == nil) != (kind == "不存在") {
				t.Fatalf("配置错误没有正确处理: kind=%s err=%v", kind, err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatal("配置解析错误泄露了正文")
			}
		})
	}
}

func TestDotenvExternalLinkAndExistingEnvironment(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "环境优先", true: "文件优先"}[overwrite], func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "runtime.env")
			if err := os.WriteFile(source, []byte("CLAW_DOTENV_FIXTURE=new-config\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			if err := os.Symlink(source, ".env"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAW_DOTENV_FIXTURE", "old-env")
			if err := LoadDotenv(overwrite); err != nil {
				t.Fatal(err)
			}
			want := "old-env"
			if overwrite {
				want = "new-config"
			}
			if os.Getenv("CLAW_DOTENV_FIXTURE") != want {
				t.Fatal("链接配置的环境优先级发生变化")
			}
		})
	}
}
