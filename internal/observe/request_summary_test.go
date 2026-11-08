package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestSummaryWhitelistAndPresenceDoNotLeakBody(t *testing.T) {
	secret := "sk-local-diagnostic-fixture-secret"
	input := &RequestSummary{Provider: "deepseek", Parameters: map[string]ParameterValue{
		"temperature": {State: "value", Value: json.RawMessage("0")}, "stream": {State: "value", Value: json.RawMessage("false")}, "max_tokens": {State: "absent"}, "top_p": {State: "null"},
		"messages": {State: "value", Value: json.RawMessage(`"private source"`)}, "tool_choice_name": {State: "value", Value: json.RawMessage(`"` + secret + `"`)},
	}, Error: &RequestErrorSummary{Code: ParameterValue{State: "absent"}, Type: ParameterValue{State: "value", Value: json.RawMessage(`"upstream_error"`)}, Summary: secret}}
	clean := cleanRequestSummary(input, []string{secret})
	b, _ := json.Marshal(clean)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "private source") || strings.Contains(string(b), "messages") {
		t.Fatalf("正文或凭据进入摘要: %s", b)
	}
	if string(clean.Parameters["temperature"].Value) != "0" || string(clean.Parameters["stream"].Value) != "false" || clean.Parameters["top_p"].State != "null" || clean.Parameters["max_tokens"].State != "absent" {
		t.Fatalf("参数presence被破坏: %s", b)
	}
	if string(input.Parameters["tool_choice_name"].Value) != `"`+secret+`"` {
		t.Fatal("清理改变调用方参数")
	}
}

func TestRequestSummaryRejectsObjectParametersAndBoundsLists(t *testing.T) {
	input := &RequestSummary{Parameters: map[string]ParameterValue{"tool_choice": {State: "value", Value: json.RawMessage(`{"messages":"not permitted"}`)}}, ToolNames: make([]string, 200)}
	for i := range input.ToolNames {
		input.ToolNames[i] = strings.Repeat("工具", 100)
	}
	clean := cleanRequestSummary(input, nil)
	b, _ := json.Marshal(clean)
	if clean.Parameters["tool_choice"].State != "unknown" || len(b) > 4096 || clean.OmittedItems == 0 {
		t.Fatalf("摘要未受限: len=%d omitted=%d", len(b), clean.OmittedItems)
	}
}

func TestRequestSummaryCountsDistinguishKnownZeroAndUnknown(t *testing.T) {
	zero, invalid := 0, -1
	clean := SanitizeRequestSummary(&RequestSummary{InputCount: &zero, AddedInputs: &zero, RemovedInputs: &invalid})
	if clean.InputCount == nil || *clean.InputCount != 0 || clean.AddedInputs == nil || *clean.AddedInputs != 0 || clean.RemovedInputs != nil || clean.ChangedInputs != nil {
		t.Fatalf("计数零值与未知混淆: %+v", clean)
	}
	zero = 5
	if *clean.InputCount != 0 || *clean.AddedInputs != 0 {
		t.Fatal("摘要仍引用可变调用方计数")
	}
	encoded, _ := json.Marshal(clean)
	if !strings.Contains(string(encoded), `"input_count":0`) || !strings.Contains(string(encoded), `"added_inputs":0`) || strings.Contains(string(encoded), `"removed_inputs"`) || strings.Contains(string(encoded), `"changed_inputs"`) {
		t.Fatalf("零值未保留或未知被补造: %s", encoded)
	}
}
