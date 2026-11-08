package engine

import (
	"encoding/json"

	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// 策略4独立工具契约只描述局部检查，不复制严格报告、草稿及模型复核字段。
func localCheckDefinitionV2() schema.ToolDefinition {
	text := map[string]any{"type": "string"}
	selector := map[string]any{"type": "object", "required": []string{"file_ref"}, "additionalProperties": false, "description": "当前证据目录的稳定file_ref；指定成对行号或绝对源字节范围。引擎仅合并当前已交付原文，不自动补读。", "properties": map[string]any{"file_ref": text, "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}, "byte_start": map[string]any{"type": "integer", "minimum": 0}, "byte_end": map[string]any{"type": "integer", "minimum": 1}, "require_complete_line": map[string]any{"type": "boolean"}}}
	assertions := map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "object", "required": []string{"op", "pointer"}, "additionalProperties": false, "properties": map[string]any{"op": map[string]any{"type": "string", "enum": []string{"exists", "absent", "type", "equals", "default"}}, "pointer": text, "type": text, "expected": map[string]any{}, "default": map[string]any{}}}}
	bindings := map[string]any{"type": "object", "maxProperties": 16, "description": "表达式每个命名变量必须在bindings声明kind，cases[].inputs不代替绑定。原文变量用source_value并给source（file_ref及完整范围）和pointer或可解析数值来源；不从subject隐式继承。test_input可在value或cases[].inputs给值；proposed_constant须给value。不能把原文数字伪装成自定常量。", "additionalProperties": map[string]any{"type": "object", "required": []string{"kind"}, "additionalProperties": false, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"source_value", "test_input", "proposed_constant"}}, "value": text, "pointer": text, "source": selector}}}
	cases := map[string]any{"type": "array", "maxItems": 16, "description": "预期值仅放cases[].expected。命名测试输入示例：expression:x+1,bindings:{x:{kind:test_input}},cases:[{inputs:{x:\"2\"},expected:\"3\"}]。这是数学样本，不证明代码前提或原文取值。", "items": map[string]any{"type": "object", "required": []string{"expected"}, "properties": map[string]any{"name": text, "inputs": map[string]any{"type": "object", "description": "只能给bindings已声明的test_input赋样本值", "additionalProperties": text}, "expected": text}}}
	check := map[string]any{"type": "object", "required": []string{"kind", "subject"}, "additionalProperties": false, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"arithmetic", "json"}}, "subject": selector, "expression": text, "bindings": bindings, "cases": cases, "assertions": assertions, "premise": map[string]any{"type": "string", "enum": []string{"source_arithmetic", "translated", "proposed"}}, "sample_label": map[string]any{"type": "string", "maxLength": 128}}}
	return schema.ToolDefinition{Name: "check_evidence", Description: "用file_ref及明确行范围核对已交付的完整JSON对象或受限算术。来源不足返回not_run及可复制补齐建议，不执行资料代码、不自动补读。pass只证明指定对象/公式；对象归属和代码前提另行判断。检查与写入不能同批。", InputSchema: map[string]any{"type": "object", "required": []string{"checks"}, "additionalProperties": false, "properties": map[string]any{"checks": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": check}}}}
}
func localInputHasFileRef(input localCheckInput) bool {
	if input.Subject.FileRef != "" {
		return true
	}
	if input.Anchor != nil && input.Anchor.Subject.FileRef != "" {
		return true
	}
	for _, b := range input.Bindings {
		if b.Source != nil && b.Source.FileRef != "" {
			return true
		}
	}
	return false
}
func boundLocalResolution(res *LocalSourceResolution) map[string]any {
	raw, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	omitted := 0
	for _, key := range []string{"resolved_evidence_ids", "available_ranges", "missing_byte_ranges", "next_actions"} {
		maxItems := 16
		if key == "next_actions" {
			maxItems = 4
		}
		if values, ok := m[key].([]any); ok && len(values) > maxItems {
			omitted += len(values) - maxItems
			m[key] = values[:maxItems]
		}
	}
	if actions, ok := m["next_actions"].([]any); ok {
		kept := []any{}
		for _, action := range actions {
			raw, _ := json.Marshal(action)
			if len(raw) <= 1600 {
				kept = append(kept, action)
			} else {
				omitted++
			}
		}
		m["next_actions"] = kept
	}
	if omitted > 0 {
		m["omitted_entries"] = omitted
	}
	return m
}
func localCheckOutputV2(artifactID string, receipts []quality.Receipt, resolutions map[string]*LocalSourceResolution) string {
	out := map[string]any{"receipt_artifact_id": artifactID, "checks": []any{}, "omitted": 0, "scope": "仅指定对象、公式和样本；来源不足不能宣称核对通过"}
	values := []any{}
	for _, receipt := range receipts {
		item := map[string]any{"id": receipt.ID, "kind": receipt.Kind, "result": receipt.Result, "premise_result": receipt.PremiseResult, "detail": truncateUTF8(quality.RenderReceipt(receipt), 650)}
		if receipt.Diagnostic != nil {
			item["diagnostic"] = receipt.Diagnostic
		}
		// 完整检查仍在受限材料中；响应按完整字段投影，不能用加总通过替代分项语义。
		if receipt.Kind == "numeric" || receipt.Kind == "arithmetic" {
			item["interpretation_result"] = "not_run"
			item["interpretation_scope"] = "加总只证明给定公式；分项名称、字段归属、输入条件、默认及截断分支仍须逐项对照原文，不代表代码已执行"
		}
		if len(receipt.Formula) <= 768 && receipt.Formula != "" {
			item["formula"] = receipt.Formula
		} else if receipt.Formula != "" {
			item["formula_omitted"] = true
		}
		var locations []quality.SourceRef
		for _, source := range receipt.Sources {
			candidate := append(append([]quality.SourceRef(nil), locations...), source)
			raw, _ := json.Marshal(candidate)
			if len(raw) <= 2000 {
				locations = candidate
			}
		}
		item["source_locations"], item["omitted_source_locations"] = locations, len(receipt.Sources)-len(locations)
		var cases []quality.CaseResult
		for _, result := range receipt.Cases {
			candidate := append(append([]quality.CaseResult(nil), cases...), result)
			raw, _ := json.Marshal(candidate)
			if len(raw) <= 1600 {
				cases = candidate
			}
		}
		item["case_results"], item["omitted_case_results"] = cases, len(receipt.Cases)-len(cases)
		if res := resolutions[receipt.ID]; res != nil {
			item["resolution"] = boundLocalResolution(res)
		}
		values = append(values, item)
		out["checks"] = values
		out["omitted"] = len(receipts) - len(values)
		raw, _ := json.Marshal(out)
		if len(raw) > 8000 {
			if len(values) == 1 {
				// 单项仍保留失败原因、请求范围及至少一个纠正入口；其余详情从受限材料读取。
				if res := resolutions[receipt.ID]; res != nil {
					minRange := map[string]any{"file_ref": res.FileRef}
					if res.Requested.StartLine != nil {
						minRange["start_line"] = *res.Requested.StartLine
						minRange["end_line"] = *res.Requested.EndLine
					}
					if res.Requested.ByteStart != nil {
						minRange["byte_start"] = *res.Requested.ByteStart
						minRange["byte_end"] = *res.Requested.ByteEnd
					}
					slim := map[string]any{"reason_code": res.ReasonCode, "requested_range": minRange, "omitted_entries": len(res.AvailableRanges) + len(res.ResolvedEvidenceIDs) + len(res.MissingByteRanges)}
					if len(res.NextActions) > 0 {
						rawAction, _ := json.Marshal(res.NextActions[0])
						if len(rawAction) <= 1600 {
							slim["next_actions"] = res.NextActions[:1]
						} else if res.Path != "" && res.Requested.StartLine != nil {
							slim["next_actions"] = []LocalSourceAction{{Tool: "read_lines", Arguments: map[string]any{"path": res.Path, "start_line": *res.Requested.StartLine, "end_line": *res.Requested.EndLine}}}
						}
					}
					item["resolution"] = slim
				}
				out["checks"] = []any{item}
				out["omitted"] = len(receipts) - 1
			} else {
				values = values[:len(values)-1]
				out["checks"] = values
				out["omitted"] = len(receipts) - len(values)
			}
			break
		}
	}
	raw, _ := json.Marshal(out)
	if len(raw) > 8000 {
		// 极长来源/纠正材料也不得损坏JSON或突破工具正文上限；不截断路径冒充可复制位置。
		first := receipts[0]
		out["checks"] = []any{map[string]any{"id": first.ID, "kind": first.Kind, "result": first.Result, "premise_result": first.PremiseResult, "reason": truncateUTF8(first.Reason, 500), "details_omitted": true, "interpretation_result": "not_run"}}
		if first.Diagnostic != nil {
			out["checks"].([]any)[0].(map[string]any)["diagnostic"] = first.Diagnostic
		}
		out["omitted"] = len(receipts) - 1
		raw, _ = json.Marshal(out)
	}
	return string(raw)
}
