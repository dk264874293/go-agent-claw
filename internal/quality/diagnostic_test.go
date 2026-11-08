package quality

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNamedInputsNeedBindingIdentityAndReturnCorrection(t *testing.T) {
	selector, lookup := material("原文只是代码前提，不能把模型样本当成原文数值。")
	check := Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: selector, Expression: "left + right", Cases: []Case{{Inputs: map[string]string{"left": "2", "right": "3"}, Expected: "5"}}, Premise: "translated"}
	got := Run([]Check{check}, lookup)[0]
	if got.Result != "error" || got.Diagnostic == nil || got.Diagnostic.Code != "binding_identity_missing" || strings.Join(got.Diagnostic.Variables, ",") != "left,right" || len(got.Cases) != 0 {
		t.Fatalf("样本值绕过身份或缺少可纠正诊断: %+v", got)
	}
	// 修正由调用方明确选择测试身份；检查器不猜测这些数来自原文。
	check.Bindings = map[string]Binding{}
	for _, name := range got.Diagnostic.Variables {
		check.Bindings[name] = Binding{Kind: "test_input"}
	}
	fixed := Run([]Check{check}, lookup)[0]
	if fixed.Result != "pass" || fixed.PremiseResult != "not_run" || fixed.Diagnostic != nil || fixed.Cases[0].Actual != "5" {
		t.Fatalf("身份修正未成功或冒充前提通过: %+v", fixed)
	}
	check.Cases[0].Expected = "6"
	counterexample := Run([]Check{check}, lookup)[0]
	if counterexample.Result != "fail" || counterexample.Diagnostic != nil {
		t.Fatal("反例被误分类为参数错误")
	}
}

func TestBindingConflictIsNotSilentlyRepaired(t *testing.T) {
	c, lookup := numeric("score+1")
	c.Bindings["score"] = Binding{Kind: "proposed_constant", Value: "2"}
	got := Run([]Check{c}, lookup)[0]
	if got.Result != "error" || got.Diagnostic == nil || got.Diagnostic.Code != "sample_binding_conflict" {
		t.Fatalf("建议常量被样本覆盖: %+v", got)
	}
	c.Bindings["score"] = Binding{Kind: "unknown"}
	got = Run([]Check{c}, lookup)[0]
	if got.Diagnostic == nil || got.Diagnostic.Code != "binding_kind_invalid" {
		t.Fatalf("未识别绑定身份没有单独分类: %+v", got)
	}
}

func TestInputDiagnosticBoundsWholeVariableNames(t *testing.T) {
	names := map[string]bool{}
	for i := 0; i < 100; i++ {
		names[strings.Repeat("x", 100)+string(rune('A'+i))] = true
	}
	err := missingBindingError(names, nil).(*inputError)
	raw, _ := json.Marshal(err.diagnostic)
	if len(raw) > 2000 || err.diagnostic.Omitted == 0 || len(err.diagnostic.Variables) > 16 {
		t.Fatalf("诊断超限: %d %+v", len(raw), err.diagnostic)
	}
	for _, name := range err.diagnostic.Variables {
		if !names[name] {
			t.Fatal("变量名被截断后冒充有效字段")
		}
	}
}
