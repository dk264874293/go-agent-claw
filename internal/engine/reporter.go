/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 16:29:16
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 16:32:32
 * @FilePath: /go-agent-claw/internal/engine/reporter.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/engine/reporter.go
package engine

import "context"

// Reporter 定义了 Agent 引擎向外界输出信息的规范。
// 这使得引擎可以无缝切换终端 (CLI)、飞书、钉钉甚至 WebUI 等不同的展现层。
type Reporter interface {
    // OnThinking 当模型开始进行慢思考 (Reasoning) 时调用
    OnThinking(ctx context.Context)

    // OnToolCall 当模型决定并发调用工具时调用
    OnToolCall(ctx context.Context, toolName string, args string)

    // OnToolResult 当工具在底层执行完毕并返回结果时调用
    OnToolResult(ctx context.Context, toolName string, result string, isError bool)

    // OnMessage 当模型宣告任务完成，向用户输出最终纯文本回答时调用
    OnMessage(ctx context.Context, content string)
}