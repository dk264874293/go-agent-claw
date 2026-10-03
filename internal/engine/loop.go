package engine

import (
	"context"
	"fmt"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
	PlanMode       bool                    // 暴露给外部的计划模式开关
	compactor      *ctxpkg.Compactor       // 压缩器实例
	recovery       *ctxpkg.RecoveryManager // 自愈管理器
}

// 移除了 Engine 层级的 WorkDir，因为 WorkDir 现在应该跟随 Session 走
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		PlanMode:       planMode,
		// 并保护最近的 6 条消息（大约两轮 Turn 的交互）
		compactor: ctxpkg.NewCompactor(300000, 10),
		recovery:  ctxpkg.NewRecoveryManager(), // 初始化 Recovery }
	}
}

// RunSub 是专为 Subagent 拉起的一次性探索循环。
// 它不依赖外部 Session，打完就跑。
// Reporter：为了让用户在终端看到子智能体的工作轨迹，我们将主线程的 Reporter 透传进来，并打上特殊标记。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {
	ctx = policy.Restrict(ctx, policy.ReadOnly())

	// 【核心优化】：子智能体极其容易偷懒。我们必须在 System Prompt 中严厉警告它必须使用工具！
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。

【核心纪律】
1. 你必须依靠 read_file、list_files、search_text 等只读工具寻找答案，不得写入文件、执行命令或递归委派。绝对不允许凭空捏造或猜测！
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
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

		if err := ctx.Err(); err != nil {
			return "", err
		}
		if actionResp == nil || actionResp.Role != schema.RoleAssistant {
			return "", fmt.Errorf("子任务模型响应无效")
		}
		contextHistory = append(contextHistory, *actionResp)

		// 【核心退出条件】：子智能体一旦不调用工具了，说明它做好了总结汇报
		if len(actionResp.ToolCalls) == 0 {
			// 直接将它的这段汇报内容剥离出来返回给上层
			return actionResp.Content, nil
		}

		r, _ := reporter.(Reporter)
		for _, call := range actionResp.ToolCalls {
			if r != nil {
				r.OnToolCall(ctx, "[Subagent] "+call.Name, string(call.Arguments))
			}
		}
		results := tools.ExecuteBatch(ctx, readOnlyRegistry, actionResp.ToolCalls)
		observationMsgs := make([]schema.Message, len(results))
		for i, result := range results {
			call := actionResp.ToolCalls[i]
			if result.IsError {
				result.Output = e.recovery.AnalyzeAndInject(call.Name, result.Output)
			}
			if r != nil {
				r.OnToolResult(ctx, "[Subagent] "+call.Name, toolPreview(result.Output), result.IsError)
			}
			meta := result.ToolMetadata
			observationMsgs[i] = schema.Message{Role: schema.RoleUser, Content: externalObservation(result.Output), ToolCallID: call.ID, ToolMetadata: &meta}
		}
		contextHistory = append(contextHistory, observationMsgs...)
	}
}
