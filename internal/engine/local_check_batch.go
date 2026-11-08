package engine

import (
	"context"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type localCheckBatchKey struct{}

// 核对得到的观察只能在后续响应采用，同批写入尚未见到核对结果。
func (r *runState) rejectLocalCheckCall(ctx context.Context, call schema.ToolCall) *schema.ToolResult {
	code, reason := "", ""
	if call.Name == "check_evidence" && !r.localChecksEnabled() {
		code, reason = "local_check_unavailable", "当前任务未开启局部核对；新普通任务须显式启用local_checks，历史任务沿原契约恢复"
	}
	checking, _ := ctx.Value(localCheckBatchKey{}).(bool)
	if checking && (call.Name == "write_file" || call.Name == "edit_file" || call.Name == "write_report" || call.Name == "bash") {
		code, reason = "check_result_pending", "本批包含check_evidence，写入未执行；请先观察核对结果，在下一正常响应修正或列待核实后再提交写入"
	}
	if code == "" {
		return nil
	}
	result := schema.NewToolResult(call.ID, reason, schema.ToolRejected, schema.EffectNone, reason, nil)
	result.ErrorCode = code
	return &result
}

const ordinarySourceQualityPrompt = `来源复用策略3：先利用当前有效的原文、来源视图和笔记；需要复核时用review绑定已有证据或动作并说明具体问题。read_evidence的offset是不可变材料内的字节位置，不是源文件行号；原文位置以结构化回执为准。不要为重新搬运整份资料反复读取证据。
普通汇总仍使用用户要求的自然语言或业务JSON。写入前在当前生成轮核对：对象归属与数量分类；数值、公式、默认值及分支条件；版本演进和比较两端；原文转述、静态推导与实际运行的边界。重要结论就地引用完整相对路径与行号。明确JSON字段存在/缺失、可表达的算术及默认代入，优先使用check_evidence核对已读材料；选择具体对象范围，不从另一对象或版本移植结论。局部检查只证明所选主体和指定关系，不证明前提、复杂代码语义或全部系统成立；未支持的结论保留待核实。
check_evidence计正常工具预算，可与独立读取同批，不与写入同批；下一正常响应使用检查结果，不为检查增加专用评审或摘要轮。工作笔记优先保留对象、关键数字、默认值、版本条件、精确引用和未解决项。覆盖完整不自动解除暂缓理由，已有摘要不能冒充结论验收。
已有确认产物且目标已满足时，依据文件回执交付可信相对路径；若需继续核查，说明具体目标并做定向复核。覆盖完成或一次Markdown写入不强制结束必要编辑和验证。`
