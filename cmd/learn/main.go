// learn 使用脚本候选和真实引擎展示 Agent 工程机制，不加载模型配置。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/dk264874293/go-agent-claw/internal/learning"
)

func run(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("learn", flag.ContinueOnError)
	flags.SetOutput(out)
	scenario := flags.String("scenario", "react", "课程：react、budget、permission、context、recovery、plan、extension")
	list := flags.Bool("list", false, "列出课程")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(out, "不接受位置参数，请使用 -scenario 选择课程")
		return 2
	}
	if *list {
		for _, lesson := range learning.Lessons() {
			fmt.Fprintf(out, "%s\t%s\n", lesson.Name, lesson.Title)
		}
		return 0
	}
	if _, err := learning.Run(ctx, *scenario, out); err != nil {
		fmt.Fprintf(out, "课程失败：%v\n", err)
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}
