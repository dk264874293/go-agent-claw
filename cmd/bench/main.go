package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/eval"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/runtimeenv"
)

func main() {
	model := flag.String("model", provider.DefaultModel, "评测模型")
	channelName := flag.String("provider", "auto", "模型渠道：auto、openai、zhipu、deepseek")
	readPageBytes := flag.Int("read-page-bytes", 8000, "单次读取档位：8000 或 16000")
	deliveryReview := flag.String("delivery-review", "off", "普通任务交付前处理：off（默认）、on；用例显式设置优先")
	flag.String("source-views", "", "来源视图实验：off（默认）、on")
	flag.String("local-checks", "", "局部核对实验：off（默认）、on")
	flag.String("claim-focus", "", "短结论核对提示实验：off（默认）、on")
	flag.Parse()
	switches := map[string]string{}
	deliveryReviewExplicit := false
	flag.Visit(func(f *flag.Flag) {
		deliveryReviewExplicit = deliveryReviewExplicit || f.Name == "delivery-review"
		switch f.Name {
		case "source-views", "local-checks", "claim-focus":
			switches[strings.ReplaceAll(f.Name, "-", "_")] = f.Value.String()
		}
	})
	features, featureErr := engine.ParseReadFeatureSwitches(switches)
	if featureErr != nil {
		log.Fatal(featureErr)
	}
	deliveryDefault, deliveryErr := benchmarkDeliveryReviewSwitch(*deliveryReview, deliveryReviewExplicit)
	if deliveryErr != nil {
		log.Fatal(deliveryErr)
	}
	if *readPageBytes != 8000 && *readPageBytes != 16000 {
		log.Fatal("读取档位必须为 8000 或 16000 字节")
	}
	if err := runtimeenv.LoadDotenv(false); err != nil {
		log.Fatal(err)
	}
	if err := runtimeenv.ApplyDefaults(); err != nil {
		log.Fatal(err)
	}
	*model = strings.TrimSpace(*model)
	modelExplicit := false
	flag.Visit(func(f *flag.Flag) { modelExplicit = modelExplicit || f.Name == "model" })
	requestedModel := *model
	if !modelExplicit {
		requestedModel = ""
	} else if requestedModel == "" {
		log.Fatal("请提供有效的模型名称")
	}
	channel, resolvedModel, err := provider.ResolveModelConfig(*channelName, requestedModel)
	if err != nil {
		log.Fatal(err)
	}
	*model = resolvedModel
	llm, err := provider.NewConfiguredProvider(channel, *model)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	testcases := []observability.TestCase{
		{
			ID: "test_001_edit", Name: "修改版本号",
			SetupScript:    `printf '%s\n' '{"name": "tiny-claw", "version": "v1.0.0"}' > config.json`,
			TaskPrompt:     "将 config.json 中的 version 改为 v2.0.0。保留其他字段和文件。",
			ValidateScript: `test "$(cat config.json)" = '{"name": "tiny-claw", "version": "v2.0.0"}' && test "$(find . -path './.claw' -prune -o -type f -print)" = './config.json'`,
		},
		{
			ID: "test_002_code_gen", Name: "编写并运行单元测试",
			SetupScript:    `printf 'module bench\n\ngo 1.25\n' > go.mod; printf 'package math\n\nfunc Multiply(a, b int) int { return a * b }\n' > math.go`,
			TaskPrompt:     "为 math.go 中的 Multiply 创建 math_test.go，编写并运行名为 TestMultiply 的单元测试。保留 math.go。",
			ValidateScript: `set -e; test -f math_test.go; output=$(go test -count=1 -v -run '^TestMultiply$' .); printf '%s\n' "$output"; printf '%s\n' "$output" | grep -E '^--- PASS: TestMultiply '`,
		},
	}
	applyBenchmarkDeliveryReviewDefaults(testcases, deliveryDefault)
	for i := range testcases {
		if testcases[i].ReadFeatures == nil && features != nil {
			copy := *features
			testcases[i].ReadFeatures = &copy
		}
		if testcases[i].ReadPageBytes == 0 {
			testcases[i].ReadPageBytes = *readPageBytes
		}
	}
	ok := observability.NewBenchmarkRunner(*model, llm).RunSuite(ctx, testcases)
	stop()
	if !ok {
		os.Exit(1)
	}
}
