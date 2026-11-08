package quality

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// Run 不做IO、网络、代码执行或语义推断。Result只表示指定命题，前提另列。
func Run(checks []Check, lookup Lookup) []Receipt {
	if len(checks) > MaxChecks {
		return []Receipt{{CheckerVersion: CheckerVersion, Result: "error", PremiseResult: "not_run", Reason: fmt.Sprintf("每草稿最多%d项检查", MaxChecks), Scope: "指定检查输入"}}
	}
	out := make([]Receipt, 0, len(checks))
	seen := map[string]bool{}
	for _, check := range checks {
		r := Receipt{CheckerVersion: CheckerVersion, ID: check.ID, FindingID: check.FindingID, Kind: check.Kind, Result: "error", PremiseResult: "not_run", Scope: "仅指定来源、表达式、对象及样本；不证明系统通用语义"}
		if check.ID == "" || len(check.ID) > 128 || check.FindingID == "" || len(check.FindingID) > 128 || seen[check.ID] {
			r.Reason = "检查身份为空、过长或重复"
			out = append(out, r)
			continue
		}
		seen[check.ID] = true
		subject, err := Resolve(check.Subject, lookup)
		if err != nil {
			r.Reason = err.Error()
			if sourceUnavailable(err) {
				r.Result = "not_run"
			}
			out = append(out, r)
			continue
		}
		r.Sources = append(r.Sources, subject.SourceRef)
		switch check.Kind {
		case "numeric":
			err = checkNumeric(check, subject, lookup, &r)
		case "json":
			err = checkJSON(check, subject, &r)
		default:
			err = fmt.Errorf("检查类型仅支持numeric或json")
		}
		if err != nil {
			r.Reason = err.Error()
			r.Result = "error"
			var input *inputError
			if errors.As(err, &input) {
				diagnostic := input.diagnostic
				r.Diagnostic = &diagnostic
			}
			if sourceUnavailable(err) {
				r.Result = "not_run"
			}
		}
		out = append(out, r)
	}
	return out
}
func checkNumeric(check Check, subject Resolved, lookup Lookup, r *Receipt) error {
	if len(check.Cases) == 0 || len(check.Cases) > MaxCases || len(check.Bindings) > MaxNodes || len(check.Assertions) > 0 {
		return fmt.Errorf("数值检查需要1至%d个样本和有限变量，不能混入JSON断言", MaxCases)
	}
	expr, err := parseExpression(check.Expression)
	if err != nil {
		return err
	}
	r.Formula = expr.render()
	vars := map[string]bool{}
	expr.variables(vars)
	if err := missingBindingError(vars, check.Bindings); err != nil {
		return err
	}
	values := map[string]*big.Rat{}
	inputNames := map[string]bool{}
	r.Bindings = map[string]BoundValue{}
	names := make([]string, 0, len(check.Bindings))
	for name := range check.Bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := check.Bindings[name]
		bound := BoundValue{Kind: b.Kind}
		if !vars[name] {
			return fmt.Errorf("绑定%s没有对应表达式变量", name)
		}
		switch b.Kind {
		case "source_value":
			if b.Source == nil {
				return fmt.Errorf("原文变量%s缺少来源选择", name)
			}
			source, err := Resolve(*b.Source, lookup)
			if err != nil {
				return err
			}
			r.Sources = append(r.Sources, source.SourceRef)
			bound.Source = &source.SourceRef
			var raw string
			var extracted *big.Rat
			if b.Pointer != "" {
				object, err := strictJSON(source.Content)
				if err != nil {
					return err
				}
				if _, ok := object.(map[string]any); !ok {
					return fmt.Errorf("原文JSON主体必须为对象")
				}
				value, present, err := pointer(object, b.Pointer)
				if err != nil {
					return err
				}
				if !present {
					return unavailable("原文变量%s的JSONPointer不存在", name)
				}
				number, ok := value.(json.Number)
				if !ok {
					return fmt.Errorf("原文变量%s不是JSON数值，不能隐式转换", name)
				}
				raw = number.String()
				extracted, err = jsonRational(raw)
				if err != nil {
					return err
				}
			} else {
				raw = strings.TrimSpace(string(source.Content))
				if raw == "" {
					return unavailable("原文数字为空")
				}
				// 禁止在84.5中只选择4，或把标识符中的字符当作原文数值。
				trimmed := bytes.TrimSpace(source.Content)
				if bytes.HasPrefix(source.Content, trimmed) && source.Before != nil && numberAdjacent(*source.Before) || bytes.HasSuffix(source.Content, trimmed) && source.After != nil && numberAdjacent(*source.After) {
					return unavailable("原文数值选择切断了完整token")
				}
				// 片段恰好从读取回执边界开始时，nil邻字节只表示未知，不能推断分隔符。
				// 例如实际84.5从offset=1读到4.5，即使末尾在EOF也不能将其当成完整数值。
				leftKnown := !bytes.HasPrefix(source.Content, trimmed) || source.StartsLine || source.Before != nil && !numberAdjacent(*source.Before)
				rightKnown := !bytes.HasSuffix(source.Content, trimmed) || source.EndsLine || source.After != nil && !numberAdjacent(*source.After)
				if !leftKnown || !rightKnown {
					return unavailable("原文数值token边界未确认，请读取相邻分隔符或完整行")
				}
			}
			value := extracted
			if value == nil {
				var err error
				value, err = decimal(raw)
				if err != nil {
					return err
				}
			}
			if b.Value != "" {
				claimed, e := decimal(b.Value)
				if e != nil {
					return e
				}
				if claimed.Cmp(value) != 0 {
					return fmt.Errorf("原文变量%s的自报值与实际来源不一致", name)
				}
			}
			values[name] = value
		case "test_input":
			if b.Source != nil || b.Pointer != "" {
				return fmt.Errorf("测试输入%s不能伪装原文来源", name)
			}
			inputNames[name] = true
			if b.Value != "" {
				v, err := decimal(b.Value)
				if err != nil {
					return err
				}
				values[name] = v
			}
		case "proposed_constant":
			if b.Source != nil || b.Pointer != "" {
				return fmt.Errorf("建议常量%s不能伪装原文来源", name)
			}
			v, err := decimal(b.Value)
			if err != nil {
				return err
			}
			values[name] = v
		default:
			return bindingInputError("binding_kind_invalid", []string{name}, fmt.Sprintf("变量%s必须声明source_value/test_input/proposed_constant", name), "将bindings中的kind设为source_value/test_input/proposed_constant，并按真实来源选择；不推断或自动补齐身份。")
		}
		if value := values[name]; value != nil {
			bound.Value = value.RatString()
		}
		r.Bindings[name] = bound
	}
	if check.Premise == "source_arithmetic" {
		original, err := parseExpression(strings.TrimSpace(string(subject.Content)))
		// 片段可能恰好删掉了后续乘数；只有完整行边界可将纯算术来源作为前提。
		// 未证明完整性仍可给出数学反例，但前提须进入定向复核。
		if err == nil && original.render() == expr.render() && subject.StartsLine && subject.EndsLine {
			r.PremiseResult = "pass"
		} else {
			r.PremiseResult = "not_run"
		}
	} else if check.Premise != "" && check.Premise != "translated" && check.Premise != "proposed" {
		return fmt.Errorf("数值前提只能是source_arithmetic/translated/proposed")
	}
	result := "pass"
	for _, sample := range check.Cases {
		if len(sample.Name) > 256 || len(sample.Inputs) > MaxNodes {
			return fmt.Errorf("样本名称或输入数量过大")
		}
		local := make(map[string]*big.Rat, len(values))
		for name, value := range values {
			local[name] = value
		}
		input := map[string]string{}
		for name, raw := range sample.Inputs {
			if !inputNames[name] {
				return bindingInputError("sample_binding_conflict", []string{name}, fmt.Sprintf("样本不能覆盖原文值或建议常量%s", name), "cases[].inputs只能设置已声明test_input变量；source_value和proposed_constant的值不能由样本覆盖。")
			}
			v, err := decimal(raw)
			if err != nil {
				return err
			}
			local[name] = v
			input[name] = v.RatString()
		}
		for name, value := range local {
			input[name] = value.RatString()
		}
		actual, err := expr.evaluate(local)
		if err != nil {
			return err
		}
		expected, err := decimal(sample.Expected)
		if err != nil {
			return err
		}
		sampleResult := "pass"
		if actual.Cmp(expected) != 0 {
			sampleResult = "fail"
			result = "fail"
		}
		r.Cases = append(r.Cases, CaseResult{Name: sample.Name, Inputs: input, Expected: expected.RatString(), Actual: actual.RatString(), Result: sampleResult})
	}
	r.Result = result
	return nil
}
func numberAdjacent(b byte) bool { return isName(b) || b == '.' || b == '+' || b == '-' }
func checkJSON(check Check, subject Resolved, r *Receipt) error {
	if len(check.Assertions) == 0 || len(check.Assertions) > MaxCases || len(check.Cases) > 0 || len(check.Bindings) > 0 || check.Expression != "" {
		return fmt.Errorf("JSON检查需要1至%d个断言，不能混入算术参数", MaxCases)
	}
	object, err := strictJSON(subject.Content)
	if err != nil {
		return err
	}
	if _, ok := object.(map[string]any); !ok {
		return fmt.Errorf("JSON主体必须是完整明确对象")
	}
	r.PremiseResult = "pass"
	r.Result = "pass"
	r.Scope = "仅所选完整JSON对象及指定Pointer，不证明整个文件或系统不存在字段"
	for _, assertion := range check.Assertions {
		value, present, err := pointer(object, assertion.Pointer)
		if err != nil {
			return err
		}
		actual := value
		passed := false
		switch assertion.Op {
		case "exists":
			passed = present
		case "absent":
			passed = !present
		case "type":
			switch assertion.Type {
			case "null", "boolean", "string", "number", "object", "array":
			default:
				return fmt.Errorf("JSON类型断言不支持%s", assertion.Type)
			}
			passed = present && jsonType(value) == assertion.Type
		case "equals", "default":
			expected, err := strictJSON(assertion.Expected)
			if err != nil {
				return fmt.Errorf("断言expected无效：%w", err)
			}
			if assertion.Op == "default" {
				fallback, err := strictJSON(assertion.Default)
				if err != nil {
					return fmt.Errorf("断言default无效：%w", err)
				}
				if !present {
					actual = fallback
				}
				passed = jsonEqual(actual, expected)
			} else {
				passed = present && jsonEqual(actual, expected)
			}
		default:
			return fmt.Errorf("不支持的JSON断言%s", assertion.Op)
		}
		out := AssertionResult{Pointer: assertion.Pointer, Op: assertion.Op, Present: present, Expected: append(json.RawMessage(nil), assertion.Expected...), Default: append(json.RawMessage(nil), assertion.Default...), Result: "pass"}
		if present || assertion.Op == "default" {
			out.Actual, _ = json.Marshal(actual)
		}
		if !passed {
			out.Result = "fail"
			r.Result = "fail"
		}
		r.Assertions = append(r.Assertions, out)
	}
	return nil
}

// RenderReceipt 的公式与结果来自已解析AST/实际计算，而不是发现中的自由文字。
func RenderReceipt(r Receipt) string {
	var out strings.Builder
	fmt.Fprintf(&out, "检查 %s（%s）：%s；前提：%s", r.ID, r.Kind, r.Result, r.PremiseResult)
	if r.Formula != "" {
		fmt.Fprintf(&out, "\n公式：`%s`", r.Formula)
	}
	for _, sample := range r.Cases {
		inputs, _ := json.Marshal(sample.Inputs)
		fmt.Fprintf(&out, "\n样本 %s %s：实际 %s，断言 %s（%s）", sample.Name, inputs, sample.Actual, sample.Expected, sample.Result)
	}
	for _, assertion := range r.Assertions {
		fmt.Fprintf(&out, "\nPointer %q %s：present=%t，实际 %s，预期 %s", assertion.Pointer, assertion.Op, assertion.Present, string(assertion.Actual), string(assertion.Expected))
		if assertion.Op == "default" {
			fmt.Fprintf(&out, "，仅缺失时使用 %s", assertion.Default)
		}
		fmt.Fprintf(&out, "（%s）", assertion.Result)
	}
	if r.Reason != "" {
		out.WriteString("\n原因：" + r.Reason)
	}
	out.WriteString("\n证明范围：" + r.Scope)
	return out.String()
}
