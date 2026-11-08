package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const localCheckMaxChecks = 8

// LocalCheckSource 是客观主体身份，不把模型给出的样例名称当作已验证的分类。
type LocalCheckSource struct {
	EvidenceIDs []string `json:"evidence_ids"`
	Path        string   `json:"path"`
	Version     string   `json:"version"`
	SHA256      string   `json:"sha256"`
	ByteStart   int64    `json:"byte_start"`
	ByteEnd     int64    `json:"byte_end"`
	FirstLine   int      `json:"first_line,omitempty"`
	LastLine    int      `json:"last_line,omitempty"`
}

// LocalCheckSummary 不携带被检查的JSON或公式正文；完整输入和回执留在受限任务材料。
// 通过仅表示指定主体与断言成立，不表示自然语言结论或整个任务已验收。
type LocalCheckSummary struct {
	ID                string             `json:"id"`
	FileRef           string             `json:"file_ref,omitempty"`
	ReasonCode        string             `json:"reason_code,omitempty"`
	OriginRunID       string             `json:"origin_run_id"`
	AgentID           string             `json:"agent_id"`
	ActionID          string             `json:"action_id"`
	ModelCallID       string             `json:"model_call_id,omitempty"`
	ReceiptArtifactID string             `json:"receipt_artifact_id"`
	Kind              string             `json:"kind"`
	Result            string             `json:"result"`
	PremiseResult     string             `json:"premise_result"`
	Reason            string             `json:"reason,omitempty"`
	Scope             string             `json:"scope"`
	Sources           []LocalCheckSource `json:"sources,omitempty"`
}

type localCheckSelector struct {
	FileRef             string   `json:"file_ref,omitempty"`
	EvidenceIDs         []string `json:"evidence_ids,omitempty"`
	StartLine           *int     `json:"start_line,omitempty"`
	EndLine             *int     `json:"end_line,omitempty"`
	ByteStart           *int64   `json:"byte_start,omitempty"`
	ByteEnd             *int64   `json:"byte_end,omitempty"`
	RequireCompleteLine bool     `json:"require_complete_line,omitempty"`
}

func (selector *localCheckSelector) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("来源选择必须为JSON对象")
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("来源选择字段%s不能为null", key)
		}
	}
	type plain localCheckSelector
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode((*plain)(selector))
}

type localCheckBinding struct {
	Kind    string              `json:"kind"`
	Value   string              `json:"value,omitempty"`
	Source  *localCheckSelector `json:"source,omitempty"`
	Pointer string              `json:"pointer,omitempty"`
}

type localCheckAnchor struct {
	Subject localCheckSelector `json:"subject"`
	Quote   string             `json:"quote"`
}

type localCheckInput struct {
	Kind        string                       `json:"kind"`
	Subject     localCheckSelector           `json:"subject"`
	SampleLabel string                       `json:"sample_label,omitempty"`
	Anchor      *localCheckAnchor            `json:"anchor,omitempty"`
	Expression  string                       `json:"expression,omitempty"`
	Bindings    map[string]localCheckBinding `json:"bindings,omitempty"`
	Cases       []quality.Case               `json:"cases,omitempty"`
	Assertions  []quality.Assertion          `json:"assertions,omitempty"`
	Premise     string                       `json:"premise,omitempty"`
}

type localCheckArtifact struct {
	Version     int                               `json:"version"`
	OriginRunID string                            `json:"origin_run_id"`
	AgentID     string                            `json:"agent_id"`
	ActionID    string                            `json:"action_id"`
	ModelCallID string                            `json:"model_call_id,omitempty"`
	Checks      []localCheckInput                 `json:"checks"`
	Receipts    []quality.Receipt                 `json:"receipts"`
	Resolutions map[string]*LocalSourceResolution `json:"resolutions,omitempty"`
}

type localCheckTool struct{}

func (localCheckTool) Name() string { return "check_evidence" }

func (r *runState) localChecksEnabled() bool {
	return r != nil && r.readFeatureEnabled("local_checks")
}

func (localCheckTool) Definition() schema.ToolDefinition {
	text := map[string]any{"type": "string"}
	selector := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"evidence_ids"},
		"description": "只选本次运行已成功交付的原文证据；成对填写start_line/end_line或byte_start/byte_end，不能混用。主体必须完整，不能猜字节偏移。",
		"properties": map[string]any{
			"evidence_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": text},
			"start_line":   map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
			"byte_start": map[string]any{"type": "integer", "minimum": 0}, "byte_end": map[string]any{"type": "integer", "minimum": 1},
			"require_complete_line": map[string]any{"type": "boolean"},
		},
	}
	check := qualityCheckSchema()
	check["additionalProperties"] = false
	check["required"] = []string{"kind", "subject"}
	properties := check["properties"].(map[string]any)
	delete(properties, "id")
	delete(properties, "finding_id")
	properties["kind"] = map[string]any{"type": "string", "enum": []string{"arithmetic", "json"}}
	properties["subject"] = selector
	properties["sample_label"] = map[string]any{"type": "string", "maxLength": 128, "description": "可选样例线索，不代表分类已核实；以回执的文件、版本及实际主体范围为准"}
	properties["anchor"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"subject", "quote"}, "properties": map[string]any{"subject": selector, "quote": map[string]any{"type": "string", "maxLength": 200, "description": "可选同文件同版本的精确短原文锚点，不自动推断业务归属"}}}
	properties["bindings"] = map[string]any{"type": "object", "maxProperties": 16, "additionalProperties": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind"}, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"source_value", "test_input", "proposed_constant"}}, "value": text, "pointer": text, "source": selector}}}
	return schema.ToolDefinition{Name: "check_evidence", Description: "对已实际读取的明确JSON对象、数值和给定默认值做局部确定性核对，不重新读取工作区、不执行资料代码、不调用模型。JSON必须选完整明确对象；算术只支持十进制、变量、加减乘除和sum。default仅检查给定fallback，不证明代码实际默认语义。失败是正常检查结果，不能当成通用语义结论。不要与依赖检查结果的写入放在同一批。", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"checks"}, "properties": map[string]any{"checks": map[string]any{"type": "array", "minItems": 1, "maxItems": localCheckMaxChecks, "items": check}}}}
}

func localCheckRejected(reason string) error {
	return tools.NewRejectedError("局部核对参数或准入错误", "local_check_invalid", fmt.Errorf("%s", reason))
}

func parseLocalChecks(raw json.RawMessage) ([]localCheckInput, error) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, localCheckRejected("核对请求必须为有效UTF-8且不超过64KiB")
	}
	var args struct {
		Checks []localCheckInput `json:"checks"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, localCheckRejected("核对参数无效：" + err.Error())
	}
	if decoder.Decode(new(any)) != io.EOF || len(args.Checks) == 0 || len(args.Checks) > localCheckMaxChecks {
		return nil, localCheckRejected("必须提交单个JSON对象和1至8项checks")
	}
	for i := range args.Checks {
		if args.Checks[i].Kind == "numeric" {
			args.Checks[i].Kind = "arithmetic"
		} // 兼容内部旧名称，不在模型Schema重复展示。
		check := args.Checks[i]
		if check.Kind != "json" && check.Kind != "arithmetic" {
			return nil, localCheckRejected("kind仅支持json或arithmetic")
		}
		if len(check.SampleLabel) > 128 || len(check.Bindings) > 16 || len(check.Cases) > quality.MaxCases || len(check.Assertions) > quality.MaxCases {
			return nil, localCheckRejected("样例标签最多128字节、变量及样本/断言各最多16项")
		}
		selectors := []localCheckSelector{check.Subject}
		for _, binding := range check.Bindings {
			if binding.Source != nil {
				selectors = append(selectors, *binding.Source)
			}
		}
		if check.Anchor != nil {
			if strings.TrimSpace(check.Anchor.Quote) == "" || utf8.RuneCountInString(check.Anchor.Quote) > 200 {
				return nil, localCheckRejected("锚点需要不超过200字符的非空精确原文")
			}
			selectors = append(selectors, check.Anchor.Subject)
		}
		for _, selector := range selectors {
			if err := validateLocalCheckSelector(selector); err != nil {
				return nil, localCheckRejected(err.Error())
			}
		}
	}
	return args.Checks, nil
}

func (localCheckTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	r := currentRun(ctx)
	if !r.localChecksEnabled() {
		return "", localCheckRejected("当前任务未启用普通读取策略3")
	}
	inputs, err := parseLocalChecks(raw)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	identity := observe.Identity(ctx)
	actionID := ActionID(ctx)
	if actionID == "" {
		actionID = identity.ActionID
	}
	if actionID == "" || identity.AgentID == "" || r.runID == "" {
		return "", localCheckRejected("核对必须经过具有运行、Agent与动作身份的正常工具入口")
	}
	if !r.readFeaturesV2() {
		for _, input := range inputs {
			if localInputHasFileRef(input) {
				return "", localCheckRejected("历史读取策略不支持file_ref")
			}
		}
	}
	requested := int64(len(inputs))
	observe.Emit(ctx, "local_check_batch", observe.Payload{ChecksRequested: &requested})
	receipts := make([]quality.Receipt, 0, len(inputs))
	resolutions := make(map[string]*LocalSourceResolution)
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		receipt, resolution := r.runLocalCheckResolved(ctx, "K"+newID(), input)
		receipts = append(receipts, receipt)
		if resolution != nil {
			resolutions[receipt.ID] = resolution
		}
	}
	if err := r.storageError(); err != nil {
		return "", err
	}
	bundle := localCheckArtifact{Version: 1, OriginRunID: r.runID, AgentID: identity.AgentID, ActionID: actionID, ModelCallID: identity.ModelCallID, Checks: inputs, Receipts: receipts, Resolutions: resolutions}
	material, err := json.Marshal(bundle)
	if err != nil {
		return "", r.noteStorageError(fmt.Errorf("%w：编码局部核对回执失败: %w", errStorage, err))
	}
	artifactID := newID()
	if r.artifact == nil {
		return "", r.noteStorageError(fmt.Errorf("%w：局部核对证据存储不可用", errStorage))
	}
	summaries := make([]LocalCheckSummary, 0, len(receipts))
	for _, receipt := range receipts {
		summary := LocalCheckSummary{ID: receipt.ID, OriginRunID: r.runID, AgentID: identity.AgentID, ActionID: actionID, ModelCallID: identity.ModelCallID, ReceiptArtifactID: artifactID, Kind: receipt.Kind, Result: receipt.Result, PremiseResult: receipt.PremiseResult, Reason: localCheckReasonCode(receipt), Scope: receipt.Scope}
		if resolution := resolutions[receipt.ID]; resolution != nil {
			summary.FileRef = resolution.FileRef
			summary.ReasonCode = resolution.ReasonCode
		}
		if receipt.Diagnostic != nil {
			summary.ReasonCode = receipt.Diagnostic.Code
		}
		for _, source := range receipt.Sources {
			summary.Sources = append(summary.Sources, LocalCheckSource{EvidenceIDs: append([]string(nil), source.EvidenceIDs...), Path: source.Path, Version: source.Version, SHA256: source.SHA256, ByteStart: source.ByteStart, ByteEnd: source.ByteEnd, FirstLine: source.FirstLine, LastLine: source.LastLine})
		}
		summaries = append(summaries, summary)
	}
	// 先登记私有材料身份。即使后续写盘或完成事件失败，读取端也能拒绝暴露该材料，
	// 且恢复只显示未确认回执，不自动重复核验或授予旧读取资格。
	if err := r.record("local_checks_prepared", func(s *taskState) error {
		for _, summary := range summaries {
			summary.Result, summary.PremiseResult, summary.Reason = "not_run", "not_run", "receipt_not_confirmed"
			s.LocalChecks = append(s.LocalChecks, summary)
		}
		return nil
	}); err != nil {
		return "", err
	}
	if err := r.artifact(artifactID, material); err != nil {
		return "", r.noteStorageError(fmt.Errorf("%w：保存局部核对回执失败: %w", errStorage, err))
	}
	if err := r.record("local_checks_completed", func(s *taskState) error {
		for _, summary := range summaries {
			for i := range s.LocalChecks {
				if s.LocalChecks[i].ID == summary.ID {
					s.LocalChecks[i] = summary
					break
				}
			}
		}
		return nil
	}); err != nil {
		// 完成事件没有可靠提交，现场摘要也保留未确认状态；prepared身份仍可限制材料访问。
		r.mu.Lock()
		for i := range r.state.LocalChecks {
			if r.state.LocalChecks[i].ReceiptArtifactID == artifactID {
				r.state.LocalChecks[i].Result, r.state.LocalChecks[i].PremiseResult, r.state.LocalChecks[i].Reason = "not_run", "not_run", "receipt_not_confirmed"
			}
		}
		r.mu.Unlock()
		return "", err
	}
	for i, summary := range summaries {
		digest := ""
		if len(summary.Sources) > 0 {
			digest = summary.Sources[0].SHA256
		}
		observe.Emit(ctx, "local_check_completed", observe.Payload{CheckID: summary.ID, FileRef: summary.FileRef, CheckResult: summary.Result, PremiseResult: summary.PremiseResult, SubjectDigest: digest, SourceKind: "current", ErrorClass: localCheckReasonCode(receipts[i])})
	}
	if r.readFeaturesV2() {
		return localCheckOutputV2(artifactID, receipts, resolutions), nil
	}
	return localCheckOutput(artifactID, receipts), nil
}

func localCheckReasonCode(receipt quality.Receipt) string {
	if receipt.Diagnostic != nil {
		return receipt.Diagnostic.Code
	}
	switch receipt.Result {
	case "pass":
		return ""
	case "fail":
		return "counterexample"
	case "not_run":
		return "source_or_premise_unavailable"
	default:
		return "input_or_checker_error"
	}
}

// 始终输出完整有界JSON；完整公式、输入、实际结果和来源身份已在回执材料中保存。
func localCheckOutput(artifactID string, receipts []quality.Receipt) string {
	type item struct {
		ID            string             `json:"id"`
		Kind          string             `json:"kind"`
		Result        string             `json:"result"`
		PremiseResult string             `json:"premise_result"`
		Subject       *quality.SourceRef `json:"subject,omitempty"`
		Detail        string             `json:"detail"`
	}
	out := struct {
		ReceiptArtifactID string `json:"receipt_artifact_id"`
		Checks            []item `json:"checks"`
		Omitted           int    `json:"omitted"`
		Scope             string `json:"scope"`
	}{ReceiptArtifactID: artifactID, Scope: "仅指定对象、公式及样本；default只检验指定fallback，不证明代码默认语义或整个任务结论正确"}
	for _, receipt := range receipts {
		next := item{ID: receipt.ID, Kind: receipt.Kind, Result: receipt.Result, PremiseResult: receipt.PremiseResult, Detail: truncateUTF8(quality.RenderReceipt(receipt), 650)}
		if len(receipt.Sources) > 0 {
			source := receipt.Sources[0]
			next.Subject = &source
		}
		out.Checks = append(out.Checks, next)
		encoded, _ := json.Marshal(out)
		if len(encoded) > 7600 {
			out.Checks = out.Checks[:len(out.Checks)-1]
			break
		}
	}
	out.Omitted = len(receipts) - len(out.Checks)
	encoded, _ := json.Marshal(out)
	return string(encoded)
}

// localCheckContext 只重用当前运行、当前权限仍可证明的简短回执；历史回执明确标为线索。
func (r *runState) localCheckContext(ctx context.Context) string {
	if !r.localChecksEnabled() {
		return ""
	}
	identity := observe.Identity(ctx)
	r.mu.Lock()
	checks := append([]LocalCheckSummary(nil), r.state.LocalChecks...)
	r.mu.Unlock()
	var out strings.Builder
	count := 0
	for i := len(checks) - 1; i >= 0 && count < 8; i-- {
		check := checks[i]
		if check.AgentID != identity.AgentID {
			continue
		}
		current := check.OriginRunID == r.runID && len(check.Sources) > 0
		for _, source := range check.Sources {
			for _, id := range source.EvidenceIDs {
				if _, err := r.analysisLookup(ctx, true)(id); err != nil {
					current = false
				}
			}
		}
		label := "历史线索，不授予当前证明资格"
		if current {
			label = "核对时有效；仅指定主体与样本"
		}
		fmt.Fprintf(&out, "%s %s result=%s premise=%s %s；完整回执=%s\n", check.ID, check.Kind, check.Result, check.PremiseResult, label, check.ReceiptArtifactID)
		count++
	}
	if count == 0 {
		return ""
	}
	return "局部确定性核对回执（不代表通用语义或运行验收）：\n" + out.String()
}
