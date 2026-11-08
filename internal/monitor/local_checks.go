package monitor

import (
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
)

// 监测只输出核对元数据，不提供完整核对材料及表达式正文入口。
type LocalCheck struct {
	ID            string             `json:"id"`
	FileRef       string             `json:"file_ref,omitempty"`
	ReasonCode    string             `json:"reason_code,omitempty"`
	OriginRunID   string             `json:"origin_run_id"`
	ActionID      string             `json:"action_id"`
	Kind          string             `json:"kind"`
	Result        string             `json:"result"`
	PremiseResult string             `json:"premise_result"`
	Scope         string             `json:"scope"`
	Reason        string             `json:"reason"`
	Sources       []LocalCheckSource `json:"sources,omitempty"`
}
type LocalCheckSource struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	ByteStart int64  `json:"byte_start"`
	ByteEnd   int64  `json:"byte_end"`
	FirstLine int    `json:"first_line"`
	LastLine  int    `json:"last_line"`
}

func (x *Index) projectLocalChecks(t *Task, checks []engine.LocalCheckSummary) {
	t.LocalChecks = nil
	for i, check := range checks {
		if i >= 128 {
			break
		}
		v := LocalCheck{ID: observe.Redact(check.ID), FileRef: observe.Redact(check.FileRef), ReasonCode: observe.Redact(check.ReasonCode), OriginRunID: observe.Redact(check.OriginRunID), ActionID: observe.Redact(check.ActionID), Kind: observe.Redact(check.Kind), Result: observe.Redact(check.Result), PremiseResult: observe.Redact(check.PremiseResult), Scope: clip(observe.Redact(check.Scope), 500), Reason: clip(observe.Redact(check.Reason), 500)}
		for j, source := range check.Sources {
			if j >= 16 {
				break
			}
			v.Sources = append(v.Sources, LocalCheckSource{Path: observe.Redact(source.Path), Version: observe.Redact(source.Version), ByteStart: source.ByteStart, ByteEnd: source.ByteEnd, FirstLine: source.FirstLine, LastLine: source.LastLine})
		}
		t.LocalChecks = append(t.LocalChecks, v)
	}
}

func (x *Index) localCheckArtifactVisible(root, taskID, artifactID string) bool {
	source := x.sources[stableID(observe.WorkspaceID(root)+"/"+taskID)]
	if source == nil || source.view.Sequence == 0 {
		return false
	}
	for _, check := range source.view.LocalChecks {
		if check.ReceiptArtifactID == artifactID {
			return false
		}
	}
	if source.view.Spec.ReadContextVersion == 3 || source.view.Spec.ReadContextVersion == 4 || source.view.Spec.DeliveryReview != nil {
		// 索引可能落后于prepared事件。新策略只公开已提交的源文身份，
		// 未登记的新材料始终关闭，不能在下一次刷新前被误当成通用公开材料。
		memory := source.view.ReadMemory
		if memory == nil {
			return false
		}
		origin, ok := memory.Evidence[memory.SourceArtifacts[artifactID]]
		if ok && origin.ArtifactID == artifactID && origin.Receipt.SourceView == nil {
			return true
		}
		if source.view.Spec.DeliveryReview != nil {
			// 策略2没有旧来源索引；只开放成功动作已提交的原始读取材料，
			// 不因目录中有文件或候选ID已落盘就推断材料可以展示。
			for _, entry := range memory.Evidence {
				if entry.ArtifactID == artifactID && entry.ActionID != "" && entry.Receipt.SourceView == nil {
					return true
				}
			}
		}
		return false
	}
	return true
}
