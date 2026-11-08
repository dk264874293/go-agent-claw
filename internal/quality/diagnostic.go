package quality

import (
	"fmt"
	"sort"
)

type inputError struct {
	diagnostic InputDiagnostic
	text       string
}

func (e *inputError) Error() string { return e.text }

func bindingInputError(code string, variables []string, text, hint string) error {
	variables = append([]string(nil), variables...)
	sort.Strings(variables)
	diagnostic := InputDiagnostic{Code: code, Variables: variables, Hint: hint}
	diagnostic.Variables = nil
	bytes := 0
	for _, name := range variables {
		if len(name) > 128 || bytes+len(name) > 512 || len(diagnostic.Variables) >= 16 {
			diagnostic.Omitted++
			continue
		}
		diagnostic.Variables = append(diagnostic.Variables, name)
		bytes += len(name)
	}
	return &inputError{diagnostic: diagnostic, text: text}
}

func missingBindingError(vars map[string]bool, bindings map[string]Binding) error {
	var missing []string
	for name := range vars {
		if _, ok := bindings[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return bindingInputError("binding_identity_missing", missing, fmt.Sprintf("变量%s缺少绑定身份", missing[0]),
		"在bindings中逐个声明表达式变量的kind。cases[].inputs只给已声明test_input的样本值，不代替身份声明。source_value必须提供实际来源和pointer或独立数值范围；proposed_constant使用value。不要把原文值自动改成测试输入；扩大subject范围不能修复绑定缺失。")
}
