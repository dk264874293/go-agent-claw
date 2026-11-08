package engine

import (
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func linkDisplayResult(text string, paths ...string) RunResult {
	result := RunResult{Status: NeedsVerification, FinalText: text, Delivery: &Delivery{HistoryComplete: true}}
	for i, path := range paths {
		result.Delivery.Effects = append(result.Delivery.Effects, EffectReceipt{
			ActionID: strings.Repeat("a", i+1), Tool: "write_file", Status: schema.ToolSucceeded, Effect: schema.EffectConfirmed,
			File: &FileEffect{Path: path, BeforeVersion: "missing", AfterVersion: strings.Repeat("b", 64)},
		})
	}
	return result
}

func TestDisplayRunFinalTextOnlyMatchesCompleteConfirmedArtifact(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		paths            []string
	}{
		{"错误宿主路径", "已生成 [汇总文档.md](/Users/other/AI Agent/汇总文档.md)。", "已生成 汇总文档.md。", []string{"汇总文档.md"}},
		{"中文空格", "[业务 汇总.md](</Users/other/业务 汇总.md>)", "业务 汇总.md", []string{"业务 汇总.md"}},
		{"转义括号", `[汇总(终版).md](/other/汇总\(终版\).md)`, "汇总(终版).md", []string{"汇总(终版).md"}},
		{"平衡括号", `[汇总.md](/other/汇总(终版).md "文件")`, "汇总.md", []string{"汇总.md"}},
		{"转义名称", `[汇总\[终版\].md](/other/report.md)`, `汇总\[终版\].md`, []string{"汇总[终版].md"}},
		{"文件URI", "[汇总.md](file:///wrong/汇总.md)", "汇总.md", []string{"汇总.md"}},
		{"相对路径", "[目录/汇总.md](../wrong/汇总.md)", "目录/汇总.md", []string{"目录/汇总.md"}},
		{"不猜同名文件", "[汇总.md](/wrong/汇总.md)", "[汇总.md](/wrong/汇总.md)", []string{"甲/汇总.md", "乙/汇总.md"}},
		{"同路径多次确认仍唯一路径", "[汇总.md](/wrong/汇总.md)", "汇总.md", []string{"汇总.md", "汇总.md"}},
		{"未知产物", "[来源.md](/original/来源.md)", "[来源.md](/original/来源.md)", []string{"汇总.md"}},
		{"HTTPS保持", "[汇总.md](https://example.com/汇总.md)", "[汇总.md](https://example.com/汇总.md)", []string{"汇总.md"}},
		{"HTTP大小写保持", "[汇总.md](HTTP://example.com/汇总.md)", "[汇总.md](HTTP://example.com/汇总.md)", []string{"汇总.md"}},
		{"其他协议保持", "[汇总.md](mailto:owner@example.com)", "[汇总.md](mailto:owner@example.com)", []string{"汇总.md"}},
		{"业务JSON保持", `{"result":"[汇总.md](/wrong/汇总.md)","score":25}`, `{"result":"[汇总.md](/wrong/汇总.md)","score":25}`, []string{"汇总.md"}},
		{"图片保持", "![汇总.md](/wrong/汇总.md)", "![汇总.md](/wrong/汇总.md)", []string{"汇总.md"}},
		{"格式名称不猜测", "[**汇总.md**](/wrong/汇总.md)", "[**汇总.md**](/wrong/汇总.md)", []string{"汇总.md"}},
		{"语法不完整", "[汇总.md](/wrong/汇总.md", "[汇总.md](/wrong/汇总.md", []string{"汇总.md"}},
		{"标题不完整", `[汇总.md](/wrong/汇总.md "title)`, `[汇总.md](/wrong/汇总.md "title)`, []string{"汇总.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := linkDisplayResult(tc.text, tc.paths...)
			if got := DisplayRunFinalText(result); got != tc.want {
				t.Fatalf("展示副本错误：得到 %q，期望 %q", got, tc.want)
			}
			if result.FinalText != tc.text {
				t.Fatal("模型原文被修改")
			}
		})
	}
}

func TestDisplayRunFinalTextPreservesCodeAndSurroundingBytes(t *testing.T) {
	link := "[汇总.md](/wrong/汇总.md)"
	for _, code := range []string{
		"```md\n" + link + "\n```",
		"~~~\n" + link + "\n~~~",
		"```md\n" + link,
		"    " + link,
		"\t" + link,
		"`" + link + "`",
		"``内容 ` " + link + "``",
		"> ```md\n> " + link + "\n> ```",
		"- ```md\n  " + link + "\n  ```",
		"1. ```md\n   " + link + "\n   ```",
		"```json\n{\"result\":\"" + link + "\"}\n```",
		"业务数据：\n{\"result\":\"" + link + "\"}",
	} {
		text := "前文\r\n" + code + "\n后文 " + link + "。\n"
		want := "前文\r\n" + code + "\n后文 汇总.md。\n"
		if strings.HasPrefix(code, "```md\n") && !strings.HasSuffix(code, "```") {
			want = text // 未闭合围栏保护后续全部正文。
		}
		if got := DisplayRunFinalText(linkDisplayResult(text, "汇总.md")); got != want {
			t.Fatalf("代码或周边字节被改写：得到 %q，期望 %q", got, want)
		}
	}
}

func TestDisplayRunFinalTextReferenceLinks(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"[汇总.md][产物]\n\n[产物]: /wrong/汇总.md", "汇总.md\n\n[产物]: /wrong/汇总.md"},
		{"[汇总.md][]\n\n[汇总.md]: </wrong/汇总.md>", "汇总.md\n\n[汇总.md]: </wrong/汇总.md>"},
		{"[汇总.md]\n\n[汇总.md]: /wrong/汇总.md", "汇总.md\n\n[汇总.md]: /wrong/汇总.md"},
		{"[汇总.md][ A  B ]\n\n[a b]: /wrong/汇总.md", "汇总.md\n\n[a b]: /wrong/汇总.md"},
		{"[汇总.md][产物]\n\n[产物]: https://example.com/report.md", "[汇总.md][产物]\n\n[产物]: https://example.com/report.md"},
		{"[汇总.md][产物]\n\n[产物]: /one\n[产物]: /two", "[汇总.md][产物]\n\n[产物]: /one\n[产物]: /two"},
		{"`[汇总.md][产物]`\n\n[产物]: /wrong/汇总.md", "`[汇总.md][产物]`\n\n[产物]: /wrong/汇总.md"},
		{"[汇总.md][未知]\n\n[产物]: /wrong/汇总.md", "[汇总.md][未知]\n\n[产物]: /wrong/汇总.md"},
	} {
		if got := DisplayRunFinalText(linkDisplayResult(tc.text, "汇总.md")); got != tc.want {
			t.Fatalf("引用链接处理错误：得到 %q，期望 %q", got, tc.want)
		}
	}
}

func TestDisplayRunFinalTextDoesNotTrustArtifactDeclarations(t *testing.T) {
	text := "[汇总.md](/wrong/汇总.md)"
	result := RunResult{FinalText: text, Artifacts: []ArtifactRef{{Path: "汇总.md", Version: strings.Repeat("a", 64)}}}
	if got := DisplayRunFinalText(result); got != text {
		t.Fatal("外部产物声明被当成可信账本")
	}
	result = linkDisplayResult(text, "汇总.md")
	result.Delivery.Effects[0].Effect = schema.EffectUnknown
	if got := DisplayRunFinalText(result); got != text {
		t.Fatal("未知效果被当成确认产物")
	}
}

func TestFinalResultFormatUsesDisplayWithoutChangingOriginal(t *testing.T) {
	text := "已生成 [汇总.md](/wrong/汇总.md)。"
	result := linkDisplayResult(text, "汇总.md")
	for _, output := range []string{FormatRunResult(result), FormatRunResultForWorkspace(result, "/actual/工作 区")} {
		if strings.Contains(output, "/wrong/") || !strings.Contains(output, "已生成 汇总.md。") {
			t.Fatalf("终报未使用展示副本：%s", output)
		}
	}
	local := FormatRunResultForWorkspace(result, "/actual/工作 区")
	if !strings.Contains(local, "[汇总.md](</actual/工作 区/汇总.md>)") || result.FinalText != text {
		t.Fatalf("可信本地产物链接或模型原文不符：%s", local)
	}
}
