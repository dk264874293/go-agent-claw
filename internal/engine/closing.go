package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// BudgetClosing 的准入标记先于请求持久化。同一工具额度边界即使中断也不重发。
type BudgetClosing struct {
	OriginRunID string `json:"origin_run_id"`
	ToolLimit   int    `json:"tool_limit"`
	Status      string `json:"status"`
	Text        string `json:"text,omitempty"`
	ErrorKind   string `json:"error_kind,omitempty"`
}

var errClosingUsed = errors.New("当前工具额度边界已经准入过收尾")

func (r *runState) toolsExhausted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Usage.ToolCalls >= r.state.Limits.MaxToolCalls
}

func toolLimit() error {
	return budgetLimit{code: "tool_limit", label: "工具调用次数"}
}

// reserveClosing 一次提交轮次、模型额度和已准入标记；调用方不能再扣模型次数。
func (r *runState) reserveClosing(ctx context.Context) error {
	if id, ok := ctx.Value(readonlyClosingKey{}).(string); ok {
		return r.reserveReadonlyClosing(ctx, id)
	}
	return r.record("budget_closing_started", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Usage.ToolCalls < s.Limits.MaxToolCalls {
			return errors.New("工具额度尚未耗尽")
		}
		if s.BudgetClosing != nil && s.BudgetClosing.ToolLimit == s.Limits.MaxToolCalls {
			return errClosingUsed
		}
		if s.Usage.ModelCalls >= s.Limits.MaxModelCalls {
			return budgetLimit{code: "model_limit", label: "模型调用次数"}
		}
		if s.Usage.Turns >= s.Limits.MaxTurns {
			return budgetLimit{code: "turn_limit", label: "主循环轮数"}
		}
		elapsed := r.elapsed
		if !r.started.IsZero() {
			elapsed += time.Since(r.started)
		}
		if elapsed >= s.Limits.Timeout {
			return context.DeadlineExceeded
		}
		s.Usage.ModelCalls++
		s.Usage.Turns++
		s.BudgetClosing = &BudgetClosing{OriginRunID: r.runID, ToolLimit: s.Limits.MaxToolCalls, Status: "started"}
		return nil
	})
}

func (r *runState) closingAtBoundary() *BudgetClosing {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.state.BudgetClosing; c != nil && c.ToolLimit == r.state.Limits.MaxToolCalls {
		copy := *c
		return &copy
	}
	return nil
}

func (r *runState) recordedPartial(reason, extra string) *PartialReport {
	r.mu.Lock()
	usage := r.state.Usage
	unknown := make(map[string]bool)
	for id := range r.state.Pending {
		unknown[id] = true
	}
	if ledger := r.state.EffectLedger; ledger != nil {
		for _, effect := range ledger.Effects {
			if effect.Effect == schema.EffectUnknown {
				unknown[effect.ActionID] = true
			}
		}
	}
	progress := append([]string(nil), r.state.Progress...)
	closing := r.state.BudgetClosing
	var interrupted bool
	if closing != nil && closing.ToolLimit == r.state.Limits.MaxToolCalls && closing.Status == "started" {
		interrupted = true
	}
	r.mu.Unlock()
	var text strings.Builder
	fmt.Fprintf(&text, "任务尚未完成。停止原因：%s。\n已记录 %d 次模型调用、%d 次工具尝试；%d 个动作效果仍待核实。", reason, usage.ModelCalls, usage.ToolCalls, len(unknown))
	if interrupted {
		text.WriteString("\n此前收尾已准入但结果未确认，本次不重新调用模型。")
	}
	if extra != "" {
		text.WriteString("\n收尾情况：" + extra)
	}
	if r.evidenceEnabled() {
		text.WriteString(r.recordedAnalysisSummary())
	}
	if coverage := r.recordedReadSummary(); coverage != "" {
		text.WriteString("\n" + coverage)
	}
	if len(progress) > 0 {
		text.WriteString("\n最近记录的动作与观察（不代表任务完成）：")
		start := max(0, len(progress)-6)
		for _, p := range progress[start:] {
			text.WriteString("\n" + truncateUTF8(p, 700))
		}
	}
	text.WriteString("\n尚需完成目标结论核对及未核实项检查；读取覆盖和动作回执不能替代验收。")
	content := text.String()
	if len(content) > 16000 {
		content = truncateUTF8(content, 15800) + "\n（记录摘要已节选，完整分析及读取证据仍保存在任务记录中。）"
	}
	return &PartialReport{Text: content, Source: "recorded", OriginRunID: r.runID}
}

// partialForStop 只在工具额度耗尽时尝试一次模型汇总，其他出口仅解释已记录的事实。
func (e *AgentEngine) partialForStop(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, result RunResult) (*PartialReport, error) {
	if r.qualityEnabled() {
		return r.recordedPartial(result.Reason, "质量任务未完成；不追加预算收尾或格式修复请求。"), nil
	}
	if result.Status == Blocked && result.StopCode == "readonly_repeat" {
		return e.partialForReadonlyStop(ctx, r, session, system, result)
	}
	if result.Status != BudgetExhausted || result.StopCode != "tool_limit" || ctx.Err() != nil {
		return r.recordedPartial(result.Reason, ""), nil
	}
	if c := r.closingAtBoundary(); c != nil {
		if c.Status == "completed" && strings.TrimSpace(c.Text) != "" {
			return &PartialReport{Text: c.Text, Source: "model", OriginRunID: c.OriginRunID}, nil
		}
		return r.recordedPartial(result.Reason, "当前额度边界已有收尾记录，未再次请求。"), nil
	}
	msg, requestErr := e.requestPartial(ctx, r, session, system, "工具额度已经耗尽。")
	if errors.Is(requestErr, errStorage) {
		return r.recordedPartial(result.Reason, "收尾记录未可靠保存。"), requestErr
	}
	// 未获准入的请求没有收费；已经获准入的请求无论结果如何都要保存结果状态。
	closing := r.closingAtBoundary()
	if closing == nil {
		return r.recordedPartial(result.Reason, "剩余额度、时间或上下文不足，未调用模型收尾。"), nil
	}
	text, errorKind := partialResponse(msg, requestErr)
	if err := r.record("budget_closing_finished", func(s *taskState) error {
		if text != "" {
			s.BudgetClosing.Status, s.BudgetClosing.Text = "completed", text
		} else {
			s.BudgetClosing.Status, s.BudgetClosing.ErrorKind = "failed", errorKind
		}
		return nil
	}); err != nil {
		return r.recordedPartial(result.Reason, "收尾结果未可靠保存。"), err
	}
	if text != "" {
		return &PartialReport{Text: text, Source: "model", OriginRunID: r.runID}, nil
	}
	return r.recordedPartial(result.Reason, "模型未产生合格的禁工具汇总，已采用记录摘要（"+errorKind+"）。"), nil
}

func (e *AgentEngine) requestPartial(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, cause string) (*schema.Message, error) {
	if system.Content == "" {
		system = schema.Message{Role: schema.RoleSystem, Content: "根据已有执行记录交付部分分析。"}
	}
	system.Content += "\n\n" + cause + "现在仅汇总已有证据，不得请求任何工具。明确说明任务未完成，分别列出已有发现、引用来源和未核实项；不得声称已验收或所有工作已完成。"
	r.mu.Lock()
	goal := r.state.Spec.Goal
	r.mu.Unlock()
	system.Content += "\n任务目标：" + goal
	r.mu.Lock()
	needsHistorical := r.state.LastResult != nil && r.state.LastResult.RunID != r.runID && len(r.analysisRuntime.sources) == 0
	r.mu.Unlock()
	if !r.evidenceEnabled() || needsHistorical {
		system.Content += r.persistedPartialContext()
	}
	messages := append([]schema.Message{system}, session.GetWorkingMemory(20)...)
	if r.readMemoryEnabled() {
		messages = append([]schema.Message{system}, r.taskHistory(session)...)
	} else if r.analysisEnabled() {
		messages = append([]schema.Message{system}, analysisHistory(session)...)
	}
	msg, err := e.generateRequest(context.WithValue(ctx, modelStageKey{}, "closing"), r, messages, nil, true)
	if r.analysisEnabled() && err == nil && msg != nil && len(msg.ToolCalls) == 0 {
		report, saveErr := r.acceptAnalysis(ctx, msg.Content, false)
		if saveErr != nil {
			return nil, saveErr
		}
		copy := *msg
		msg = &copy
		if report == nil {
			msg.Content = ""
		} else {
			msg.Content = RenderAnalysis(*report)
		}
	}
	return msg, err
}

// persistedPartialContext 使跨进程恢复的首次收尾也能看到证据线索；历史材料不提升为当前读取。
func (r *runState) persistedPartialContext() string {
	r.mu.Lock()
	progress := append([]string(nil), r.state.Progress...)
	previous, origin := "", ""
	if last := r.state.LastResult; last != nil && last.PartialReport != nil {
		previous, origin = last.PartialReport.Text, last.PartialReport.OriginRunID
	}
	r.mu.Unlock()
	if previous == "" && len(progress) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n\n持久记录节选（仅说明历史观察，未重新核对当前文件版本，不是当前验收证明）：\n")
	appendText := func(label, text string, maxBytes int) {
		text = truncateUTF8(text, maxBytes)
		for text != "" {
			encoded, _ := json.Marshal(text)
			if out.Len()+len(label)+len(encoded)+1 <= 16*1024 {
				out.WriteString(label)
				out.Write(encoded)
				out.WriteByte('\n')
				return
			}
			text = truncateUTF8(text, len(text)/2)
		}
	}
	if previous != "" {
		appendText("历史部分报告（来源运行 "+origin+"）：", previous, 6000)
	}
	for i := max(0, len(progress)-10); i < len(progress); i++ {
		appendText("已记录动作观察：", progress[i], 700)
	}
	return out.String()
}

func partialResponse(msg *schema.Message, err error) (string, string) {
	switch {
	case err != nil:
		return "", observationError(err)
	case msg == nil || strings.TrimSpace(msg.Content) == "":
		return "", "empty_response"
	case len(msg.ToolCalls) != 0:
		return "", "unexpected_tool_call"
	default:
		return strings.TrimSpace(msg.Content), ""
	}
}
