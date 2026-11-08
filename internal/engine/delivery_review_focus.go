package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

// 定向目录复用已有发现，不新增草稿、语义评分或模型阶段。
// 一个条目连同其完整依赖整体投影/省略，不能截断摘录或丢掉改变结论的条件。
type deliveryReviewClaim struct {
	Finding      AnalysisFinding   `json:"finding"`
	Dependencies []AnalysisFinding `json:"dependencies,omitempty"`
}

type deliveryReviewCheck struct {
	ID                   string             `json:"id"`
	FileRef              string             `json:"file_ref,omitempty"`
	Result               string             `json:"result"`
	PremiseResult        string             `json:"premise_result"`
	ReasonCode           string             `json:"reason_code,omitempty"`
	Reason               string             `json:"reason,omitempty"`
	Scope                string             `json:"scope"`
	ArtifactID           string             `json:"receipt_artifact_id"`
	SourceLocations      []LocalCheckSource `json:"source_locations,omitempty"`
	InterpretationResult string             `json:"interpretation_result,omitempty"`
}

func (r *runState) currentDeliveryReviewNotes(ctx context.Context) AnalysisReport {
	agent := observe.Identity(ctx).AgentID
	r.mu.Lock()
	var raw []byte
	if r.state.ReadMemory != nil {
		raw, _ = json.Marshal(r.state.ReadMemory.Notes[agent])
	}
	r.mu.Unlock()
	var report AnalysisReport
	if json.Unmarshal(raw, &report) != nil || report.SchemaVersion != 1 {
		return AnalysisReport{SchemaVersion: 1}
	}
	lookup := r.analysisLookup(ctx, true)
	checked, _ := ValidateAnalysisReport(report, func(id string) (AnalysisEvidence, error) {
		e, err := lookup(id)
		if err == nil && (e.AgentID != agent || e.RunID != r.runID) {
			err = errors.New("定向目录只使用本人本轮成功交付的来源")
		}
		return e, err
	})
	return checked
}

func deliveryReviewClaims(report AnalysisReport) []deliveryReviewClaim {
	byID := map[string]AnalysisFinding{}
	for _, f := range report.Findings {
		byID[f.ID] = f
	}
	ordered := append([]AnalysisFinding(nil), report.Findings...)
	// 推断及建议优先；这只是核对顺序，不把类型视作风险或语义正确的证明。
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Kind != "fact" && ordered[j].Kind == "fact"
	})
	var claims []deliveryReviewClaim
	for _, f := range ordered {
		claim := deliveryReviewClaim{Finding: f}
		seen := map[string]bool{f.ID: true}
		var visit func(string)
		visit = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			dep := byID[id] // 只输入依赖闭合、引用已验证的报告。
			for _, parent := range dep.DependsOn {
				visit(parent)
			}
			claim.Dependencies = append(claim.Dependencies, dep)
		}
		for _, id := range f.DependsOn {
			visit(id)
		}
		claims = append(claims, claim)
	}
	return claims
}

func (r *runState) deliveryReviewChecks(ctx context.Context) []deliveryReviewCheck {
	r.mu.Lock()
	checks := append([]LocalCheckSummary(nil), r.state.LocalChecks...)
	r.mu.Unlock()
	var result []deliveryReviewCheck
	for i := len(checks) - 1; i >= 0; i-- {
		c := checks[i]
		if c.AgentID != observe.Identity(ctx).AgentID || c.OriginRunID != r.runID {
			continue
		}
		item := deliveryReviewCheck{ID: c.ID, FileRef: c.FileRef, Result: c.Result, PremiseResult: c.PremiseResult, ReasonCode: c.ReasonCode, Reason: truncateUTF8(c.Reason, 500), Scope: truncateUTF8(c.Scope, 300), ArtifactID: c.ReceiptArtifactID, SourceLocations: c.Sources}
		if c.Kind == "arithmetic" || c.Kind == "numeric" {
			item.InterpretationResult = "not_run"
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Result != "pass" && result[j].Result == "pass" })
	return result
}

const deliveryReviewClaimInstructions = "\n先处理会改变结论的命题：数值分别核对原文标注、静态公式、默认值及加总；每个分项绑定字段、输入对象、触发条件和默认/截断分支，加总正确不证明分项含义正确。存在、非空、类型和合法性分开；跨文档核对两端版本及迁移条件。重要结论就地使用完整文件相对路径:行号，比较引用两端，不用文末索引代替依据；把这些条件写进正文，不只补核对失败说明。claims保留完整依赖和原文位置，不代表语义通过。核对结果应进入正文，修正、限定或明确待核实；检查后无需改动可保持正文，只补处置声明不证明已修正。不要为每份文档编造问题，也不执行资料脚本。"
