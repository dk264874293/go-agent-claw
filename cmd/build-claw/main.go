package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/joho/godotenv"
)

const defaultsPackage = "github.com/dk264874293/go-agent-claw/internal/runtimeenv."

type buildSetting struct {
	name, alias, symbol string
}

var buildSettings = []buildSetting{
	{"HTTPS_PROXY", "https_proxy", "DefaultHTTPSProxy"},
	{"HTTP_PROXY", "http_proxy", "DefaultHTTPProxy"},
	{"NO_PROXY", "no_proxy", "DefaultNoProxy"},
	{"OPENAI_BASE_URL", "", "DefaultOpenAIBaseURL"},
	{"ZHIPU_BASE_URL", "", "DefaultZhipuBaseURL"},
	{"DEEPSEEK_BASE_URL", "", "DefaultDeepseekBaseURL"},
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("build-claw", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "用法：go run ./cmd/build-claw [-o claw] [-env-file 路径]")
		fmt.Fprintln(stderr, "  -o 路径        输出二进制路径，默认 claw")
		fmt.Fprintln(stderr, "  -env-file 路径 构建配置文件，默认读取当前目录的 .env")
	}
	output := flags.String("o", "claw", "输出二进制路径")
	envFile := flags.String("env-file", "", "构建配置文件，默认读取当前目录的 .env")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("构建参数无效，请查看 -help")
	}
	if flags.NArg() != 0 || strings.TrimSpace(*output) == "" {
		return errors.New("构建参数无效，请查看 -help")
	}
	path := *envFile
	if path == "" {
		path = ".env"
	}
	fileValues, err := readBuildEnv(path, *envFile != "")
	if err != nil {
		return err
	}
	arguments, names, err := buildArguments(*output, os.LookupEnv, fileValues)
	if err != nil {
		return err
	}
	command := exec.Command("go", arguments...)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("构建失败: %w", err)
	}
	fmt.Fprintln(stdout, "构建完成："+*output)
	if len(names) != 0 {
		fmt.Fprintln(stdout, "已写入默认配置："+strings.Join(names, ", "))
	}
	return nil
}

func readBuildEnv(path string, required bool) (map[string]string, error) {
	values, err := godotenv.Read(path)
	if err == nil {
		return values, nil
	}
	if !required && errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	// 解析错误可能包含原始密钥行，不向终端回显原始错误。
	return nil, fmt.Errorf("无法读取构建配置文件 %q，请检查文件路径和格式", path)
}

func buildArguments(output string, lookup func(string) (string, bool), fileValues map[string]string) ([]string, []string, error) {
	arguments := []string{"build", "-o", output}
	var linkerArguments, names []string
	for _, setting := range buildSettings {
		value := buildValue(setting, lookup, fileValues)
		if value == "" {
			continue
		}
		argument, err := quoteLinkerArgument(defaultsPackage+setting.symbol+"="+value, setting.name)
		if err != nil {
			return nil, nil, err
		}
		linkerArguments = append(linkerArguments, "-X", argument)
		names = append(names, setting.name)
	}
	if len(linkerArguments) != 0 {
		arguments = append(arguments, "-ldflags", strings.Join(linkerArguments, " "))
	}
	return append(arguments, "./cmd/claw"), names, nil
}

func buildValue(setting buildSetting, lookup func(string) (string, bool), fileValues map[string]string) string {
	for _, source := range []func(string) (string, bool){lookup, func(name string) (string, bool) {
		value, exists := fileValues[name]
		return value, exists
	}} {
		if value, exists := source(setting.name); exists {
			return value
		}
		if setting.alias != "" {
			if value, exists := source(setting.alias); exists {
				return value
			}
		}
	}
	return ""
}

// Go 的 -ldflags 引号解析不做反斜杠转义，使用未出现在内容中的引号包裹整个参数。
func quoteLinkerArgument(argument, name string) (string, error) {
	if strings.ContainsRune(argument, '\x00') {
		return "", fmt.Errorf("构建配置 %s 含有不支持的字符", name)
	}
	if !strings.ContainsAny(argument, " \t\r\n\"'") {
		return argument, nil
	}
	if !strings.ContainsRune(argument, '\'') {
		return "'" + argument + "'", nil
	}
	if !strings.ContainsRune(argument, '"') {
		return "\"" + argument + "\"", nil
	}
	return "", fmt.Errorf("构建配置 %s 同时含有单引号和双引号，无法写入构建参数", name)
}
