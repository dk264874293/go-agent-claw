package engine

import (
	"context"
	"encoding/json"
	"errors"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// 非法报告批次整体拒绝，不执行任何Handler；仍记录各次工具准入并补齐配对。
func (e *AgentEngine) rejectReportBatch(ctx context.Context, r *runState, session *ctxpkg.Session, msg *schema.Message) error {
	raw, _ := json.Marshal(msg.ToolCalls)
	err := r.rejectReportSubmission(ctx, raw, "write_report必须为当前响应唯一工具调用")
	session.Append(schema.Message{Role: schema.RoleAssistant, Content: msg.Content, ToolCalls: msg.ToolCalls, ResponseItems: msg.ResponseItems})
	var observations []schema.Message
	for _, call := range msg.ToolCalls {
		result := schema.NewToolResult(call.ID, "非法报告提交批次，未执行任何工具", schema.ToolRejected, schema.EffectNone, "报告契约无效", nil)
		result.ErrorCode = "report_invalid"
		if !errors.Is(err, errStorage) {
			if reserveErr := r.reserve(ctx, "tool"); reserveErr != nil {
				err = reserveErr
				result.Output = reserveErr.Error()
			} else {
				id := newID()
				if saveErr := r.record("action_result", func(s *taskState) error {
					s.LastAction = &actionRecord{ID: id, Tool: call.Name, Digest: actionKey("", call), Result: result}
					r.note(s, "报告批次拒绝，工具未执行："+call.Name)
					return nil
				}); saveErr != nil {
					err = saveErr
				}
			}
		}
		observations = append(observations, schema.Message{Role: schema.RoleUser, ToolCallID: call.ID, ToolStatus: result.Status, Content: observation(result)})
	}
	session.Append(observations...)
	return err
}
