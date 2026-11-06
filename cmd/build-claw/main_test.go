package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

func TestBuildValueSourcePriority(t *testing.T) {
	setting := buildSettings[0]
	for _, tc := range []struct {
		name      string
		env, file map[string]string
		want      string
	}{
		{name: "进程大写优先", env: map[string]string{"HTTPS_PROXY": "upper", "https_proxy": "lower"}, file: map[string]string{"HTTPS_PROXY": "file"}, want: "upper"},
		{name: "进程小写优于文件大写", env: map[string]string{"https_proxy": "lower"}, file: map[string]string{"HTTPS_PROXY": "file"}, want: "lower"},
		{name: "进程显式空值", env: map[string]string{"HTTPS_PROXY": ""}, file: map[string]string{"HTTPS_PROXY": "file"}},
		{name: "进程小写空值", env: map[string]string{"https_proxy": ""}, file: map[string]string{"HTTPS_PROXY": "file"}},
		{name: "文件大写优先", file: map[string]string{"HTTPS_PROXY": "upper", "https_proxy": "lower"}, want: "upper"},
		{name: "文件小写回退", file: map[string]string{"https_proxy": "lower"}, want: "lower"},
		{name: "文件大写空值", file: map[string]string{"HTTPS_PROXY": "", "https_proxy": "lower"}},
		{name: "缺少配置"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if value := buildValue(setting, mapLookup(tc.env), tc.file); value != tc.want {
				t.Fatalf("来源优先级错误: got=%q want=%q", value, tc.want)
			}
		})
	}
	for _, setting := range buildSettings[3:] {
		if value := buildValue(setting, mapLookup(map[string]string{strings.ToLower(setting.name): "unsupported"}), nil); value != "" {
			t.Fatalf("BaseURL 错误读取小写变量: %s", setting.name)
		}
	}
}

func TestBuildArgumentsOnlyEmbedWhitelist(t *testing.T) {
	values := map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:7891", "HTTP_PROXY": "http://127.0.0.1:7892", "NO_PROXY": "localhost, 127.0.0.1",
		"OPENAI_BASE_URL": "https://openai.example/v1/", "ZHIPU_BASE_URL": "https://zhipu.example/v4/", "DEEPSEEK_BASE_URL": "https://deepseek.example/",
		"OPENAI_API_KEY": "sensitive-openai-key", "ZHIPU_API_KEY": "sensitive-zhipu-key", "DEEPSEEK_API_KEY": "sensitive-deepseek-key",
	}
	arguments, names, err := buildArguments("目标 二进制", mapLookup(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "OPENAI_BASE_URL", "ZHIPU_BASE_URL", "DEEPSEEK_BASE_URL"}
	if !reflect.DeepEqual(names, wantNames) || arguments[0] != "build" || arguments[1] != "-o" || arguments[2] != "目标 二进制" || arguments[len(arguments)-1] != "./cmd/claw" {
		t.Fatalf("构建参数或白名单错误: %+v %+v", arguments, names)
	}
	text := strings.Join(arguments, " ")
	if strings.Contains(text, "sensitive-") || strings.Contains(text, "API_KEY") {
		t.Fatal("构建参数包含密钥")
	}
	for _, setting := range buildSettings {
		if !strings.Contains(text, defaultsPackage+setting.symbol+"="+values[setting.name]) {
			t.Fatalf("白名单配置未打包: %s", setting.name)
		}
	}
	if !strings.Contains(arguments[4], "-X '"+defaultsPackage+"DefaultNoProxy=localhost, 127.0.0.1'") {
		t.Fatal("带空格的 NO_PROXY 未正确包裹")
	}
	arguments, names, err = buildArguments("claw", mapLookup(map[string]string{"OPENAI_BASE_URL": ""}), map[string]string{"OPENAI_BASE_URL": "file"})
	if err != nil || len(names) != 0 || !reflect.DeepEqual(arguments, []string{"build", "-o", "claw", "./cmd/claw"}) {
		t.Fatal("空值没有阻止文件回退或仍注入了构建参数")
	}
}

func TestFirstQuotedAssignmentStartsWithUnquotedFlag(t *testing.T) {
	arguments, names, err := buildArguments("claw", mapLookup(map[string]string{
		"NO_PROXY": "localhost, 127.0.0.1",
	}), nil)
	if err != nil || !reflect.DeepEqual(names, []string{"NO_PROXY"}) {
		t.Fatalf("构建配置错误: names=%v err=%v", names, err)
	}
	want := "-X '" + defaultsPackage + "DefaultNoProxy=localhost, 127.0.0.1'"
	if arguments[4] != want {
		t.Fatalf("首个带空格配置的 ldflags 格式错误: got=%q want=%q", arguments[4], want)
	}
}

func TestLinkerArgumentQuotingAndRedactedErrors(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"-X=plain", "-X=plain"},
		{"-X=with space", "'-X=with space'"},
		{"-X=with\"quote", "'-X=with\"quote'"},
		{"-X=with'quote", "\"-X=with'quote\""},
		{`-X=literal\path`, `-X=literal\path`},
	} {
		got, err := quoteLinkerArgument(tc.value, "NO_PROXY")
		if err != nil || got != tc.want {
			t.Fatalf("构建转义错误: got=%q want=%q err=%v", got, tc.want, err)
		}
	}
	for _, value := range []string{"secret-value\x00", "secret-value\"'"} {
		_, err := quoteLinkerArgument(value, "OPENAI_BASE_URL")
		if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
			t.Fatal("非法配置未拒绝或错误回显了配置正文")
		}
	}
}

func TestReadBuildEnvMissingAndInvalidFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if values, err := readBuildEnv(path, false); err != nil || len(values) != 0 {
		t.Fatal("默认配置文件缺失时应允许构建")
	}
	if _, err := readBuildEnv(path, true); err == nil {
		t.Fatal("显式指定的配置文件缺失时没有报错")
	}
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY='secret-unterminated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildEnv(path, true); err == nil || strings.Contains(err.Error(), "secret-unterminated") {
		t.Fatal("解析失败未报告或回显了密钥正文")
	}
}

func TestRunUsesGoArgumentsWithoutShellExpansion(t *testing.T) {
	dir := t.TempDir()
	for _, setting := range buildSettings {
		t.Setenv(setting.name, "")
		if setting.alias != "" {
			t.Setenv(setting.alias, "")
		}
	}
	t.Setenv("OPENAI_BASE_URL", "https://example.invalid/v1/?literal=$(touch_should_not_run)")
	t.Setenv("OPENAI_API_KEY", "secret-runtime-key")
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY=secret-file-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "arguments")
	t.Setenv("BUILD_ARGUMENTS", capture)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BUILD_ARGUMENTS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"-env-file", path, "-o", "目标 二进制"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "$(touch_should_not_run)") || strings.Contains(string(arguments), "secret-") || strings.Contains(output.String(), "secret-") || strings.Contains(output.String(), "example.invalid") {
		t.Fatal("构建参数展开了 shell 内容或输出包含配置值")
	}
}
