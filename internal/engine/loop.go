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


// RunSub 是专为 Subagent 拉起的一次性受限循环。
// 它不依赖外部 Session，打完就跑。
// Reporter：为了让用户在终端看到子智能体的工作轨迹，我们将主线程的 Reporter 透传进来，并打上特殊标记。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {

    // 【核心优化】：子智能体极其容易偷懒。我们必须在 System Prompt 中严厉警告它必须使用工具！
    contextHistory := []schema.Message{
        {
            Role: schema.RoleSystem,
            Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。

【核心纪律】
1. 你必须、且只能依靠内置工具（如 bash 的 find/grep，或 read_file）去寻找答案。绝对不允许凭空捏造或猜测！
2. 如果你没有找到确切的答案，你必须继续使用工具深入搜索。
3. 当且仅当你找到了确切的线索后，停止调用工具，直接输出一段纯文本作为你的终极汇报。主架构师会根据你的汇报来做下一步决策。`,
        },
        {
            Role:    schema.RoleUser,
            Content: taskPrompt,
        },
    }

    // 限制子智能体最多只能跑 10 个 Turn，防止它自己卡死
    const maxSubTurns = 10
    turnCount := 0

    for {
        turnCount++
        if turnCount > maxSubTurns {
            return "", fmt.Errorf("子智能体探索过于深入，超过 %d 轮被强制召回，请主 Agent 给它更明确的指令", maxSubTurns)
        }

        // 【驾驭底线】：子智能体仅能获取传入的只读工具注册表
        availableTools := readOnlyRegistry.GetAvailableTools()

        compactedContext := e.compactor.Compact(contextHistory)

        // 子任务要求急速响应，强制关闭主体的慢思考，直接预测行动
        actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
        if err != nil {
            return "", fmt.Errorf("子智能体推理失败: %w", err)
        }

        contextHistory = append(contextHistory, *actionResp)

        // 【核心退出条件】：子智能体一旦不调用工具了，说明它做好了总结汇报
        if len(actionResp.ToolCalls) == 0 {
            // 直接将它的这段汇报内容剥离出来返回给上层
            return actionResp.Content, nil
        }

        // 执行只读工具的并发循环
        observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
        var wg sync.WaitGroup

        for i, toolCall := range actionResp.ToolCalls {
            wg.Add(1)
            go func(idx int, call schema.ToolCall) {
                defer wg.Done()

                // 【可视化的关键】：让终端用户看到 Subagent 正在干嘛
                var r Reporter
                if reporter != nil {
                    r = reporter.(Reporter)
                    r.OnToolCall(ctx, fmt.Sprintf("[Subagent] %s", call.Name), string(call.Arguments))
                }

                result := readOnlyRegistry.Execute(ctx, call)

                finalOutput := result.Output
                if result.IsError {
                    finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
                }

                if reporter != nil {
                    display := finalOutput
                    if len(display) > 200 {
                        display = display[:200] + "... (已截断)"
                    }
                    r.OnToolResult(ctx, fmt.Sprintf("[Subagent] %s", call.Name), display, result.IsError)
                }

                observationMsgs[idx] = schema.Message{
                    Role:       schema.RoleUser,
                    Content:    finalOutput,
                    ToolCallID: call.ID,
                }
            }(i, toolCall)
        }

        wg.Wait()
        contextHistory = append(contextHistory, observationMsgs...)
    }
}