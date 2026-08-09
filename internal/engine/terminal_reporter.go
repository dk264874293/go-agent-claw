/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 19:46:54
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 20:22:22
 * @FilePath: /go-agent-claw/internal/engine/terminal_reporter.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/engine/terminal_reporter.go
package engine

import (
    "context"
    "fmt"
    "strings"
)

// TerminalReporter 实现了 Reporter 接口，用于在终端直观地打印 Agent 的状态
type TerminalReporter struct{}

func NewTerminalReporter() *TerminalReporter {
    return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinking(ctx context.Context) {
    fmt.Printf("\n[🤔 思考中] 模型正在推理...\n")
}

func (r *TerminalReporter) OnToolCall(ctx context.Context, toolName string, args string) {
    fmt.Printf("[🛠️ 调用工具] %s\n", toolName)
    // 截断过长的参数显示，保持终端清爽
    displayArgs := strings.ReplaceAll(args, "\n", "\\n")
    displayArgs = strings.ReplaceAll(displayArgs, "\r", "\\r")
    if len(displayArgs) > 150 {
        displayArgs = displayArgs[:150] + "... (已截断)"
    }
    fmt.Printf("   参数: %s\n", displayArgs)
}

func (r *TerminalReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
    if isError {
        fmt.Printf("[❌ 执行失败] %s\n", toolName)
        // 显示错误信息
        if result != "" {
            fmt.Printf("   错误: %s\n", result)
        }
    } else {
        fmt.Printf("[✅ 执行成功] %s\n", toolName)
    }
}

func (r *TerminalReporter) OnMessage(ctx context.Context, content string) {
    if content == "" {
        return
    }
    fmt.Printf("\n🤖 Agent 回复:\n%s\n\n", content)
}