package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// setEffect 在事件锁内更新，同一动作的确认结果覆盖未知结果而不改变顺序。
func setEffect(s *taskState, receipt EffectReceipt) {
	if s.EffectLedger == nil {
		// 旧日志缺少结构化历史，新记录不能补证过去发生的动作。
		s.EffectLedger = &Delivery{Effects: []EffectReceipt{}}
	}
	for i := range s.EffectLedger.Effects {
		if s.EffectLedger.Effects[i].ActionID == receipt.ActionID {
			s.EffectLedger.Effects[i] = receipt
			return
		}
	}
	s.EffectLedger.Effects = append(s.EffectLedger.Effects, receipt)
}

// deliveryFromState 只投影结构化记录，不重新执行工具或解释自然语言输出。
// 调用方须持有状态锁，或在任务执行结束后调用。
func deliveryFromState(s taskState) *Delivery {
	delivery := &Delivery{Effects: []EffectReceipt{}}
	seen := make(map[string]bool)
	if s.EffectLedger != nil {
		delivery.HistoryComplete = s.EffectLedger.HistoryComplete
		for _, receipt := range s.EffectLedger.Effects {
			if receipt.File != nil {
				file := *receipt.File
				receipt.File = &file
			}
			delivery.Effects = append(delivery.Effects, receipt)
			seen[receipt.ActionID] = true
		}
	}
	var ids []string
	for id := range s.Pending {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		delivery.Effects = append(delivery.Effects, EffectReceipt{
			ActionID: id, Tool: s.Pending[id].Call.Name, Effect: schema.EffectUnknown,
		})
	}
	return delivery
}

func effectReceipt(pending pendingAction, result schema.ToolResult) EffectReceipt {
	receipt := EffectReceipt{ActionID: pending.ID, Tool: pending.Call.Name, Status: result.Status, Effect: result.Effect}
	if result.Effect == schema.EffectConfirmed && pending.Before != nil && pending.Expected != nil &&
		(pending.Call.Name == "write_file" || pending.Call.Name == "edit_file") {
		receipt.File = &FileEffect{
			Path: pending.Before.Path, BeforeVersion: pending.Before.Version, AfterVersion: pending.Expected.Version,
		}
	}
	return receipt
}

// FormatRunResult 由入口在 Run 的持久化收尾完成后调用，统一生成中文终报。
func FormatRunResult(result RunResult) string {
	var out strings.Builder
	out.WriteString(result.Summary())
	if result.StopCode != "" {
		fmt.Fprintf(&out, "\n停止原因：%s", result.StopCode)
	}
	if result.StopCode == "storage_error" {
		out.WriteString("\n任务记录未可靠保存，以下仅为现场执行观察，不能作为已持久化证据。")
	}
	if result.Delivery != nil {
		if !result.Delivery.HistoryComplete {
			out.WriteString("\n历史效果记录不完整，不能据此认定此前没有修改。")
		}
		for _, receipt := range result.Delivery.Effects {
			fmt.Fprintf(&out, "\n动作 %s · %s", receipt.ActionID, receipt.Tool)
			switch {
			case receipt.Effect == schema.EffectUnknown:
				if receipt.Status == schema.ToolSucceeded {
					out.WriteString("：命令已结束，文件效果未核实。")
				} else {
					out.WriteString("：效果未知，须先核实，不能盲目重试。")
				}
			case receipt.File != nil && receipt.Effect == schema.EffectConfirmed:
				if receipt.File.BeforeVersion == receipt.File.AfterVersion {
					fmt.Fprintf(&out, "：%q 确认无变化，版本 %s。", receipt.File.Path, receipt.File.AfterVersion)
				} else {
					fmt.Fprintf(&out, "：%q 当时已确认写入，版本 %s → %s。", receipt.File.Path, receipt.File.BeforeVersion, receipt.File.AfterVersion)
				}
			case receipt.Effect == schema.EffectNone:
				out.WriteString("：确认未产生效果。")
			default:
				fmt.Fprintf(&out, "：状态 %s，效果 %s，缺少可核对的文件版本。", receipt.Status, receipt.Effect)
			}
		}
		if len(result.Delivery.Effects) > 0 {
			out.WriteString("\n以上回执记录动作完成或恢复核实时的效果，不代表文件当前仍存在或任务已验收。")
		}
	}
	if result.Status == Verified && result.Verification != nil && result.Verification.Passed {
		fmt.Fprintf(&out, "\n验收已通过，核对版本 %s。", result.Verification.Version)
	} else if result.Verification != nil {
		out.WriteString("\n现有验收记录不能证明当前任务通过，仍需核实。")
	} else if result.Status != Answered {
		out.WriteString("\n尚未取得有效验收证据。")
	}
	if result.FinalText != "" && (result.Status == Answered || result.Status == Verified || result.Status == NeedsVerification) && result.StopCode != "verification_stale" {
		out.WriteString("\n\n" + result.FinalText)
	}
	return out.String()
}
