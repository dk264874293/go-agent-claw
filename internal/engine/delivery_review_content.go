package engine

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// 只比较正文的精确字节，不证明修订改善了语义，也不证明文件当前仍存在。
// 文件的比较结果仅来自本运行处理机会之后的新确认动作，不扫描工作区。
type DeliveryReviewContentComparison struct {
	Kind            string `json:"kind"`
	Path            string `json:"path,omitempty"`
	CandidateSHA256 string `json:"candidate_sha256"`
	ConfirmedSHA256 string `json:"confirmed_sha256,omitempty"`
	ActionID        string `json:"action_id,omitempty"`
	Status          string `json:"status"`
}

func deliveryContentSHA(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }

func deliveryReviewCandidates(msg schema.Message) []DeliveryReviewContentComparison {
	if len(msg.ToolCalls) == 0 && msg.Content != "" {
		return []DeliveryReviewContentComparison{{Kind: "answer", CandidateSHA256: deliveryContentSHA(msg.Content), Status: "not_compared"}}
	}
	counts := map[string]int{}
	var candidates []DeliveryReviewContentComparison
	for _, call := range msg.ToolCalls {
		if call.Name != "write_file" {
			continue // edit_file没有完整候选正文，不猜测最终文件。
		}
		var input struct {
			Path    string  `json:"path"`
			Content *string `json:"content"`
		}
		if json.Unmarshal(call.Arguments, &input) != nil || input.Content == nil || !filepath.IsLocal(input.Path) {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(input.Path))
		counts[path]++
		candidates = append(candidates, DeliveryReviewContentComparison{Kind: "file", Path: path, CandidateSHA256: deliveryContentSHA(*input.Content), Status: "not_compared"})
	}
	unique := candidates[:0]
	for _, c := range candidates {
		if counts[c.Path] == 1 && len(unique) < 64 {
			unique = append(unique, c)
		}
	}
	return unique
}

func sameDeliveryReviewCandidates(a, b []DeliveryReviewContentComparison) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Path != b[i].Path || a[i].CandidateSHA256 != b[i].CandidateSHA256 {
			return false
		}
	}
	return true
}

func validateDeliveryReviewComparisons(d *DeliveryReviewState) error {
	if len(d.ContentComparisons) > 64 || len(d.ContentComparisons) > 0 && d.AdmissionCount != 1 {
		return errors.New("正文比较须绑定唯一已准入的处理机会")
	}
	seen := map[string]bool{}
	for _, c := range d.ContentComparisons {
		key := c.Kind + "\x00" + c.Path
		if seen[key] || !validReportVersion(c.CandidateSHA256) || c.Kind != "file" && c.Kind != "answer" || c.Kind == "file" && !filepath.IsLocal(c.Path) || c.Kind == "answer" && c.Path != "" {
			return errors.New("交付前候选正文身份无效")
		}
		seen[key] = true
		switch c.Status {
		case "not_compared":
			if c.ConfirmedSHA256 != "" || c.ActionID != "" {
				return errors.New("未比较的正文不能携带确认身份")
			}
		case "unknown":
			if c.Kind != "file" || !analysisIDValid(c.ActionID) || c.ConfirmedSHA256 != "" {
				return errors.New("未知正文比较缺少动作身份或误带确认版本")
			}
		case "changed", "unchanged":
			if !validReportVersion(c.ConfirmedSHA256) || (c.Status == "unchanged") != (c.CandidateSHA256 == c.ConfirmedSHA256) || c.Kind == "file" && !analysisIDValid(c.ActionID) || c.Kind == "answer" && c.ActionID != "" {
				return errors.New("正文比较与确认版本不一致")
			}
		default:
			return errors.New("正文比较状态无效")
		}
	}
	return nil
}

func deliveryComparisonStatus(before, after string) string {
	if before == after {
		return "unchanged"
	}
	return "changed"
}

// 调用者在action_result事件锁内；历史回执复用不经过此处。
func recordDeliveryReviewContent(s *taskState, runID string, pending pendingAction, result schema.ToolResult) {
	d := s.DeliveryReview
	if d == nil || d.OriginRunID != runID || d.AdmissionCount != 1 || d.Status != "completed" || pending.Before == nil {
		return
	}
	path := filepath.ToSlash(filepath.Clean(pending.Before.Path))
	for i := range d.ContentComparisons {
		c := &d.ContentComparisons[i]
		if c.Kind != "file" || c.Path != path {
			continue
		}
		if result.Effect == schema.EffectUnknown {
			c.Status, c.ActionID, c.ConfirmedSHA256 = "unknown", pending.ID, ""
		} else if receipt := effectReceipt(pending, result); receipt.File != nil && validReportVersion(receipt.File.AfterVersion) {
			c.ConfirmedSHA256, c.ActionID = receipt.File.AfterVersion, pending.ID
			c.Status = deliveryComparisonStatus(c.CandidateSHA256, c.ConfirmedSHA256)
		}
	}
}

func recordDeliveryReviewAnswer(s *taskState, result RunResult) {
	d := s.DeliveryReview
	if d == nil || d.OriginRunID != result.RunID || d.Status != "completed" || result.FinalText == "" {
		return
	}
	for i := range d.ContentComparisons {
		c := &d.ContentComparisons[i]
		if c.Kind == "answer" {
			c.ConfirmedSHA256 = deliveryContentSHA(result.FinalText)
			c.Status = deliveryComparisonStatus(c.CandidateSHA256, c.ConfirmedSHA256)
		}
	}
}
