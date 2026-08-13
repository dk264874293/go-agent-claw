package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)


type AgentEngine struct {
    provider       provider.LLMProvider
    registry       tools.Registry
    EnableThinking bool
	PlanMode       bool // 暴露给外部的计划模式开关
	compactor *ctxpkg.Compactor // 压缩器实例
	recovery *ctxpkg.RecoveryManager // 自愈管理器
	injector *ReminderInjector // 提醒注入器
}

// 移除了 Engine 层级的 WorkDir，因为 WorkDir 现在应该跟随 Session 走
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool,planMode bool) *AgentEngine {
    return &AgentEngine{
        provider:       p,
        registry:       r,
        EnableThinking: enableThinking,
		PlanMode:       planMode,
        // 并保护最近的 6 条消息（大约两轮 Turn 的交互）
        compactor:      ctxpkg.NewCompactor(300000, 10),
		recovery: 		ctxpkg.NewRecoveryManager(), // 初始化 Recovery }
		injector: 		NewReminderInjector(), // 【初始化注入器】
    }
}

// Run 启动 Agent 的生命周期
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	// 根据当前 Session 的工作区，动态组装最新的 System Prompt、
	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
    systemMsg := composer.Build()

	// 2. The Main Loop: 心跳开始 (标准的 ReAct 循环)
	for {
		// 获取当前挂载的所有工具定义
		availableTools := e.registry.GetAvailableTools()

		// 1. 【上下文组装】: System Prompt + 截取最近的 6 条消息作为 Working Memory
        // 在实际业务中，由于工具返回结果可能很长，短期工作记忆往往设为 6-10 条足以维系连贯对话
        workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
        contextHistory = append(contextHistory, systemMsg)
        contextHistory = append(contextHistory, workingMemory...)
		
		// 2. 【核心注入点】: 在向 Provider 发起推理前，过一遍内存压缩器！
        // 无论你带出了多少上下文，如果字符总数超标，早期日志将被掩码化，超大日志将被掐头去尾
        compactedContext := e.compactor.Compact(contextHistory)

		var currentTurnThinkingContent string
		// ====================================================================
		// Phase 1: 慢思考阶段 (Thinking) - 剥夺工具，强制规划
		// ====================================================================
		if e.EnableThinking {
			if reporter != nil {
				// 【触发 Reporter】: 开始慢思考
				reporter.OnThinking(ctx)
			}
			thinkResp, err := e.provider.Generate(ctx, compactedContext, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				currentTurnThinkingContent = thinkResp.Content
				// 将思考过程持久化到 Session 中！
                session.Append(*thinkResp)
                // 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
                compactedContext = append(compactedContext, *thinkResp)
			}
		}
 
		// 模型会顺着自己的逻辑，结合恢复的 availableTools 发起精准的工具调用。
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}
		finalAssistantMsg := schema.Message{
            Role:      schema.RoleAssistant,
            Content:   strings.TrimSpace(currentTurnThinkingContent + "\n" + actionResp.Content),
            ToolCalls: actionResp.ToolCalls,
        }
		// 将大模型的行动响应持久化到 Session 中 
		session.Append(finalAssistantMsg)
		// 将模型的响应完整追加到上下文历史中
		// compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
            // 【触发 Reporter】: 输出阶段性总结或最终回复
            reporter.OnMessage(ctx, actionResp.Content)
        }

		if len(actionResp.ToolCalls) == 0 {
			log.Println("[Engine] 模型未请求调用工具，任务宣告完成。")
			break
		}

		//  ================= 并发执行底层工具 =================
        observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
        var wg sync.WaitGroup

		// 用于收集本轮执行的最后一个工具，供 Reminder 探测器分析
        // (在真实的工业级架构中，如果并发调用了多个工具，我们可以逐个分析或仅分析报错的那个。这里简化为取第一个)
        var lastToolCall schema.ToolCall
        var lastToolResult schema.ToolResult


        for i, toolCall := range actionResp.ToolCalls {
            wg.Add(1) // 增加计数器
            // 开启协程。注意：一定要将索引 i 和 toolCall 作为参数传入匿名函数，防止闭包变量捕获陷阱！
            go func(idx int, call schema.ToolCall) {
                defer wg.Done() // 协程结束时计数器减一
                log.Printf("  -> [Go-%d] 🛠️ 触发并行执行: %s\n", idx, call.Name)
				if reporter != nil {
                    // 【触发 Reporter】: 报告即将在底层执行的工具
                    reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
                }
                // 调用底层 Registry 执行工具（物理操作）
                result := e.registry.Execute(ctx, call)

				// 【核心拦截与注入】
                finalOutput := result.Output
                if result.IsError {
                    // 发生错误，交由 RecoveryManager 诊断并注入“锦囊妙计”
                    finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
                    log.Printf("  -> [Go-%d] ❌ 注入救援指南: %s\n", idx, finalOutput)
                } else {
                    log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
                }

                if reporter != nil {
                    // 为了防止大文件读取导致飞书消息过长被截断，我们仅汇报工具执行状态
                    // 注意：传递给大模型的 observationMsgs 依然是完整数据，只是人类看到的 Reporter 是缩略版
                    displayOutput := result.Output
                    if len(displayOutput) > 200 {
                        displayOutput = displayOutput[:200] + "... (已截断)"
                    }
                    // 【触发 Reporter】: 汇报工具物理执行的结果
                    reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
                }

                // 将执行结果封装为一条用户消息 (RoleUser)
                obsMsg := schema.Message{
                    Role:       schema.RoleUser,
                    Content:    result.Output,
                    ToolCallID: call.ID,
                }
                // 【线程安全】: 由于每个 Goroutine 操作的是预分配切片的不同索引，
                // 这里不需要加锁 (Mutex)，性能极高！
                observationMsgs[idx] = obsMsg

				if idx == 0 { 
					lastToolCall = call 
					lastToolResult = result 
				}
            }(i, toolCall) // 闭包传参
        }

		// 4. Join 阻塞等待：主循环挂起，直到所有的并发协程全部执行完毕
        wg.Wait()
		log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")
		// 将所有的工具执行结果（Observation）持久化到 Session 中，开启下一轮的复盘与推理
		// 1. 先将普通的工具执行结果存入 Session
        session.Append(observationMsgs...)
        // 2. 【核心防线】：在准备进入下一轮之前，进行死循环探测！
        reminderMsg := e.injector.CheckAndInject(lastToolCall, lastToolResult)
        if reminderMsg != nil {
            // 如果触发了干预规则，将这条严厉的提醒作为 User 消息，强制追加到 Session 的最末尾！
            // 大模型在下一轮被唤醒时，第一眼就会看到这句话，从而打破局部执念。
            session.Append(*reminderMsg)
        }

		// 循环回到开头，模型将带着新加入的 Observation 继续它的下一轮思考...
	}

	return nil
}
