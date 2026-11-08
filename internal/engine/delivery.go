package engine

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
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
		(pending.Call.Name == "write_file" || pending.Call.Name == "edit_file" || pending.Call.Name == "write_report") {
		receipt.File = &FileEffect{
			Path: pending.Before.Path, BeforeVersion: pending.Before.Version, AfterVersion: pending.Expected.Version,
		}
	}
	return receipt
}

// FormatRunResult 由入口在 Run 的持久化收尾完成后调用，统一生成中文终报。
func FormatRunResult(result RunResult) string {
	return formatRunResult(result, "")
}

// FormatRunResultForWorkspace 只供本地入口从真实工作区生成可点击产物路径。
// 工作区不写入通用回执，飞书使用相对路径，观测台继续使用服务端ID。
func FormatRunResultForWorkspace(result RunResult, workDir string) string {
	return formatRunResult(result, workDir)
}

func artifactsFromDelivery(delivery *Delivery) []ArtifactRef {
	if delivery == nil {
		return nil
	}
	var result []ArtifactRef
	seen := make(map[string]bool)
	for _, effect := range delivery.Effects {
		if effect.Effect != schema.EffectConfirmed || effect.File == nil || effect.ActionID == "" || !filepath.IsLocal(effect.File.Path) || !validReportVersion(effect.File.AfterVersion) {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(effect.File.Path))
		key := effect.ActionID + "\x00" + path + "\x00" + effect.File.AfterVersion
		if seen[key] {
			continue
		}
		seen[key] = true
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:32]
		result = append(result, ArtifactRef{ID: id, ActionID: effect.ActionID, Path: path, Version: effect.File.AfterVersion})
	}
	return result
}

func formatRunResult(result RunResult, workDir string) string {
	var out strings.Builder
	out.WriteString(result.Summary())
	if result.StopCode == "model_content_rejected" {
		out.WriteString("\n模型服务报告输入内容被拒绝；未自动重试，具体命中位置未知。")
		if len(artifactsFromDelivery(result.Delivery)) > 0 {
			out.WriteString(" 已有确认保存的产物，详见文件回执；产物尚未因此获得验收。")
		}
	}
	if result.StopCode != "" {
		fmt.Fprintf(&out, "\n停止原因：%s", result.StopCode)
	}
	if result.Reading != nil {
		out.WriteString("\n" + readingCoverageText(result.Reading))
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
		out.WriteString("\n\n" + DisplayRunFinalText(result))
	}
	// 可信产物仅来自效果账本，忽略模型文本和外部传入的Artifacts声明。
	artifacts := artifactsFromDelivery(result.Delivery)
	if len(artifacts) > 0 {
		out.WriteString("\n\n已确认产物（动作当时的版本）：")
		for _, artifact := range artifacts {
			fmt.Fprintf(&out, "\n- %q", artifact.Path)
			if workDir != "" && filepath.IsAbs(workDir) && !strings.ContainsAny(workDir+artifact.Path, "<>\r\n") {
				absolute := filepath.Join(workDir, filepath.FromSlash(artifact.Path))
				fmt.Fprintf(&out, " · [%s](<%s>)", escapeAnalysisText(artifact.Path), absolute)
			}
			fmt.Fprintf(&out, " · 版本 %s", artifact.Version)
		}
	}
	if report := result.Report; report != nil {
		fmt.Fprintf(&out, "\n\n证据报告 %s · %s\n引用检查：%s；语义评估：%s。", report.ReportID, report.Status, report.ReferenceResult, report.SemanticResult)
		if report.Status == "stale" {
			out.WriteString("\n报告源或产物版本已经变化，历史发布不证明当前有效。")
		}
		if report.Status == "invalid" || report.Status == "prepared" {
			out.WriteString("\n报告尚未正式发布，以下仅为部分材料。")
		}
		for _, issue := range report.Issues {
			out.WriteString("\n- " + issue)
		}
		if report.Text != "" {
			out.WriteString("\n\n" + report.Text)
		}
	}
	if quality := result.Quality; quality != nil {
		fmt.Fprintf(&out, "\n\n质量核验 v%d · %s\n草稿：%s；本地检查：%s；风险复核：%s；作者修订：%s。", quality.Version, quality.Status, quality.DraftID, quality.CheckResult, quality.ReviewStatus, quality.RevisionStatus)
		out.WriteString("\n本地检查仅证明指定关系，模型复核不代表独立语义验收或运行验收。")
		for _, issue := range quality.Unresolved {
			out.WriteString("\n- 待核实：" + issue)
		}
	}
	if review := result.DeliveryReview; review != nil {
		if review.RecordedOnly {
			out.WriteString("\n\n以下交付前处理来自历史运行，未重新核对当前文件。")
		}
		fmt.Fprintf(&out, "\n\n交付前处理 v%d · %s\n准入次数：%d；文档处置声明：共%d，有来源发现%d，不相关%d，待核实%d，未声明%d，待重新处理%d。", review.Version, deliveryReviewStatusText(review.Status), review.AdmissionCount, review.Documents, review.Addressed, review.NotRelevant, review.Unverified, review.Undeclared, review.ReadyToResolve)
		if review.OriginRunID != "" {
			fmt.Fprintf(&out, "\n处理来源运行：%s", review.OriginRunID)
			if result.RunID != "" && review.OriginRunID != result.RunID {
				out.WriteString("（历史记录，不代表本次新请求）")
			}
		}
		out.WriteString("\n上述只表示处理机会及文档声明，不证明重要问题无遗漏、结论正确或任务已验收。")
		for _, comparison := range review.ContentComparisons {
			label := "终答"
			if comparison.Kind == "file" {
				label = fmt.Sprintf("%q", comparison.Path)
			}
			status := "尚无可比较的确认正文"
			switch comparison.Status {
			case "changed":
				status = "正文与处理前草稿不同"
			case "unchanged":
				status = "正文与处理前草稿相同"
			case "unknown":
				status = "动作效果未知，无法比较正文"
			}
			fmt.Fprintf(&out, "\n- 正文比较 %s：%s；仅比较确认时字节，不代表语义改善或当前文件状态。", label, status)
		}
		for i, issue := range review.Unresolved {
			if i >= 32 {
				fmt.Fprintf(&out, "\n另有%d项待核实，详见任务记录。", len(review.Unresolved)-i)
				break
			}
			fmt.Fprintf(&out, "\n- 待核实 %s · %s：%s", issue.ID, issue.Kind, truncateUTF8(issue.Reason, 1200))
			if issue.Path != "" {
				fmt.Fprintf(&out, "；来源 %q（版本 %s）", issue.Path, issue.Version)
			}
			for j, identity := range append(append(append([]string{}, issue.EvidenceIDs...), issue.FindingIDs...), issue.CheckIDs...) {
				if j >= 12 {
					out.WriteString(" · 其余来源详见任务记录")
					break
				}
				fmt.Fprintf(&out, " · %s", truncateUTF8(identity, 128))
			}
		}
		if review.Omitted > 0 {
			fmt.Fprintf(&out, "\n另有%d项未在摘要展示，详见任务记录。", review.Omitted)
		}
		if review.ErrorClass != "" {
			fmt.Fprintf(&out, "\n处理状态原因：%s", review.ErrorClass)
		}
	}
	if len(result.LocalChecks) > 0 {
		out.WriteString("\n\n局部确定性核对（动作当时的记录，不代表语义或运行验收）：")
		for i, check := range result.LocalChecks {
			if i >= 32 {
				fmt.Fprintf(&out, "\n另有 %d 项，详见任务记录。", len(result.LocalChecks)-i)
				break
			}
			fmt.Fprintf(&out, "\n- %s · %s：%s；前提：%s", check.ID, check.Kind, check.Result, check.PremiseResult)
			if check.OriginRunID != result.RunID {
				out.WriteString("；历史运行")
			}
			for j, source := range check.Sources {
				if j >= 4 {
					break
				}
				fmt.Fprintf(&out, "；%q:%d–%d（版本 %s）", source.Path, source.FirstLine, source.LastLine, source.Version)
			}
		}
	}
	if partial := result.PartialReport; partial != nil && strings.TrimSpace(partial.Text) != "" &&
		result.Status != Answered && result.Status != Verified && result.Status != NeedsVerification {
		source := "来源未知"
		switch partial.Source {
		case "model":
			source = "模型收尾汇总"
		case "recorded":
			source = "已记录事实摘要"
		}
		fmt.Fprintf(&out, "\n\n部分分析／任务未完成\n来源：%s", source)
		if partial.OriginRunID != "" {
			fmt.Fprintf(&out, "；来源运行：%s", partial.OriginRunID)
			if result.RunID != "" && partial.OriginRunID != result.RunID {
				out.WriteString("（历史来源，文件当前状态须重新核实）")
			}
		}
		out.WriteString("\n以下内容不代表任务已完成或验收通过。\n" + partial.Text)
	}
	return out.String()
}

func deliveryReviewStatusText(status string) string {
	switch status {
	case "idle", "pending", "not_run":
		return "尚未准入"
	case "not_needed":
		return "本次无需强制退回"
	case "admitted":
		return "已准入，结果待确认"
	case "completed":
		return "一次处理响应已记录"
	case "failed":
		return "处理请求失败"
	case "interrupted", "outcome_unknown":
		return "处理结果未确认，不自动重发"
	case "skipped", "skipped_no_budget":
		return "未追加处理请求"
	default:
		return status
	}
}
