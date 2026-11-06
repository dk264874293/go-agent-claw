package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// 只验证固定投影经过实际 Provider/SDK 后的材料字节数，不推断模型 Token 或语义质量。
func TestAnalysisProjectedReadBodyShrinksActualProviderRequests(t *testing.T) {
	oldSource := strings.Repeat("旧文档的已处理事实。\n", 300)
	freshSource := "最新完整工具批次必须保留：data.get(\"score\", 5)。\n"
	oldCall := schema.ToolCall{ID: "call_old", Name: "read_lines", Arguments: json.RawMessage(`{"path":"old.md","start_line":1,"end_line":20}`)}
	freshCall := schema.ToolCall{ID: "call_fresh", Name: "read_lines", Arguments: json.RawMessage(`{"path":"fresh.md","start_line":150,"end_line":175}`)}
	opaque := json.RawMessage(`{"type":"reasoning","id":"rs_old","summary":[],"encrypted_content":"opaque-offline-fixture"}`)
	baseline := []schema.Message{
		{Role: schema.RoleSystem, Content: "目标：按版本比较文档。E1=old.md；E2=fresh.md。"},
		{Role: schema.RoleUser, Content: "分析材料并提供准确引用。"},
		{Role: schema.RoleAssistant, Content: "读取旧文档。", ToolCalls: []schema.ToolCall{oldCall}, ResponseItems: analysisResponseItems(t, opaque, "msg_old", "读取旧文档。", "fc_old", oldCall)},
		{Role: schema.RoleUser, ToolCallID: oldCall.ID, Content: oldSource},
		{Role: schema.RoleAssistant, Content: "旧文档已处理，继续检查默认值。", ToolCalls: []schema.ToolCall{freshCall}, ResponseItems: analysisResponseItems(t, nil, "msg_fresh", "旧文档已处理，继续检查默认值。", "fc_fresh", freshCall)},
		{Role: schema.RoleUser, ToolCallID: freshCall.ID, Content: freshSource},
	}
	projected := cloneAnalysisMessages(baseline)
	projected[3].Content = "证据 E1 的正文已折叠；有效发现 F1：文档属于 V1，尚需和 V2 对照。原文可按证据引用复核。"
	defs := []schema.ToolDefinition{tools.NewReadLinesTool(t.TempDir()).Definition()}
	for _, model := range []string{provider.DefaultModel, "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			captured := captureAnalysisTransport(t, model, [][]schema.Message{baseline, projected}, defs)
			if len(captured[1])*10 > len(captured[0])*7 {
				t.Fatalf("已处理正文折叠未反映到实际 HTTP 材料：%d → %d 字节", len(captured[0]), len(captured[1]))
			}
			t.Logf("固定投影实际编码字节：%d → %d，下降 %.1f%%；不是 Token 测量", len(captured[0]), len(captured[1]), 100*(1-float64(len(captured[1]))/float64(len(captured[0]))))
			for index, raw := range captured {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				assertAnalysisTransportSchema(t, body, model == "gpt-6.1-sol")
				wantOld := oldSource
				if index == 1 {
					wantOld = projected[3].Content
				}
				if model == "gpt-6.1-sol" {
					assertAnalysisResponsesIdentity(t, body["input"], []schema.ToolCall{oldCall, freshCall}, map[string]string{oldCall.ID: wantOld, freshCall.ID: freshSource}, opaque)
				} else {
					assertAnalysisChatIdentity(t, body["messages"], []schema.ToolCall{oldCall, freshCall}, map[string]string{oldCall.ID: wantOld, freshCall.ID: freshSource})
				}
			}
		})
	}
}

// 负向对照：Responses 优先发送 ResponseItems，单改 Content 没有压缩实际请求。
// 显式投影普通文本项时，调用项和不透明推理项必须保持原样。
func TestResponsesProjectionCannotOnlyReplaceAssistantContent(t *testing.T) {
	longText := strings.Repeat("已保存的分析笔记正文。", 500)
	call := schema.ToolCall{ID: "call_projection", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}
	opaque := json.RawMessage(`{"type":"reasoning","id":"rs_projection","summary":[],"encrypted_content":"opaque-do-not-change"}`)
	baseline := []schema.Message{
		{Role: schema.RoleSystem, Content: "分析目标和证据目录。"},
		{Role: schema.RoleUser, Content: "继续分析。"},
		{Role: schema.RoleAssistant, Content: longText, ToolCalls: []schema.ToolCall{call}, ResponseItems: analysisResponseItems(t, opaque, "msg_projection", longText, "fc_projection", call)},
		{Role: schema.RoleUser, ToolCallID: call.ID, Content: "最新工具正文完整保留。"},
	}
	contentOnly := cloneAnalysisMessages(baseline)
	contentOnly[2].Content = "简短笔记。"
	projected := cloneAnalysisMessages(contentOnly)
	projected[2].ResponseItems = analysisResponseItems(t, opaque, "msg_projection", projected[2].Content, "fc_projection", call)
	captured := captureAnalysisTransport(t, "gpt-6.1-sol", [][]schema.Message{baseline, contentOnly, projected}, nil)
	if !bytes.Equal(captured[0], captured[1]) {
		t.Fatal("负向对照不成立：仅修改 Content 意外改变了原始 ResponseItems 的实际回放")
	}
	if len(captured[2])*10 > len(captured[0])*7 {
		t.Fatalf("普通文本 ResponseItem 投影未显著缩小实际请求：%d → %d", len(captured[0]), len(captured[2]))
	}
	for _, raw := range captured {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		assertAnalysisResponsesIdentity(t, body["input"], []schema.ToolCall{call}, map[string]string{call.ID: baseline[3].Content}, opaque)
	}
}

func captureAnalysisTransport(t *testing.T, model string, inputs [][]schema.Message, defs []schema.ToolDefinition) [][]byte {
	t.Helper()
	captured := make(chan []byte, len(inputs))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1/chat/completions"
		if model == "gpt-6.1-sol" {
			wantPath = "/v1/responses"
		}
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Errorf("Provider 编码测试走错端点：%s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			t.Errorf("读取本地测试请求：%v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		if model == "gpt-6.1-sol" {
			_, _ = io.WriteString(w, `{"id":"resp_projection","object":"response","status":"completed","output":[{"type":"message","id":"msg_result","role":"assistant","status":"completed","content":[{"type":"output_text","text":"离线编码通过。","annotations":[]}]}]}`)
		} else {
			_, _ = io.WriteString(w, `{"id":"chat_projection","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"离线编码通过。"}}]}`)
		}
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "offline-projection-key")
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
	p := provider.NewOpenAIProvider(model)
	results := make([][]byte, 0, len(inputs))
	for _, messages := range inputs {
		message, err := p.Generate(context.Background(), messages, defs)
		if err != nil || message == nil || message.Content != "离线编码通过。" {
			t.Fatalf("本地 Provider 编码失败：%+v / %v", message, err)
		}
		select {
		case raw := <-captured:
			results = append(results, raw)
		default:
			t.Fatal("本地请求未被捕获")
		}
	}
	return results
}

func analysisResponseItems(t *testing.T, opaque json.RawMessage, messageID, text, functionID string, call schema.ToolCall) []json.RawMessage {
	t.Helper()
	items := make([]json.RawMessage, 0, 3)
	if opaque != nil {
		items = append(items, append(json.RawMessage(nil), opaque...))
	}
	for _, value := range []any{
		map[string]any{"type": "message", "id": messageID, "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}},
		map[string]any{"type": "function_call", "id": functionID, "call_id": call.ID, "status": "completed", "name": call.Name, "arguments": string(call.Arguments)},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, raw)
	}
	return items
}

func cloneAnalysisMessages(messages []schema.Message) []schema.Message {
	cloned := append([]schema.Message(nil), messages...)
	for index := range cloned {
		cloned[index].ToolCalls = append([]schema.ToolCall(nil), messages[index].ToolCalls...)
		cloned[index].ResponseItems = append([]json.RawMessage(nil), messages[index].ResponseItems...)
	}
	return cloned
}

func assertAnalysisTransportSchema(t *testing.T, body map[string]json.RawMessage, isResponses bool) {
	t.Helper()
	var defs []map[string]json.RawMessage
	if err := json.Unmarshal(body["tools"], &defs); err != nil || len(defs) != 1 {
		t.Fatalf("工具定义没有完整编码：%s / %v", body["tools"], err)
	}
	function := defs[0]
	if !isResponses {
		if err := json.Unmarshal(function["function"], &function); err != nil {
			t.Fatal(err)
		}
	} else if string(function["strict"]) != "false" {
		t.Fatal("Responses 意外启用 strict，破坏可选参数语义")
	}
	var parameters struct {
		Properties map[string]struct {
			Type    string `json:"type"`
			Minimum *int   `json:"minimum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(function["parameters"], &parameters); err != nil {
		t.Fatal(err)
	}
	if string(function["name"]) != `"read_lines"` || !reflect.DeepEqual(parameters.Required, []string{"path", "start_line", "end_line"}) || parameters.Properties["line_numbers"].Type != "boolean" || parameters.Properties["offset"].Type != "" || parameters.Properties["limit"].Type != "" {
		t.Fatalf("新参数错误地变成必填或编号类型变化：%s", function["parameters"])
	}
	for _, field := range []string{"start_line", "end_line"} {
		property := parameters.Properties[field]
		if property.Type != "integer" || property.Minimum == nil || *property.Minimum != 1 {
			t.Fatalf("按行参数未出现在真实编码 Schema：%s", function["parameters"])
		}
	}
}

func assertAnalysisResponsesIdentity(t *testing.T, raw json.RawMessage, calls []schema.ToolCall, outputs map[string]string, opaque json.RawMessage) {
	t.Helper()
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	seenCalls, seenOutputs, seenOpaque := make(map[string]int), make(map[string]int), 0
	for _, item := range input {
		var kind, callID string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["call_id"], &callID)
		switch kind {
		case "reasoning":
			var actual, expected any
			encoded, _ := json.Marshal(item)
			if json.Unmarshal(encoded, &actual) != nil || json.Unmarshal(opaque, &expected) != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("不透明推理项发生变化：%s", encoded)
			}
			seenOpaque++
		case "function_call":
			seenCalls[callID]++
			matched := false
			for _, call := range calls {
				var name, arguments, itemID string
				_ = json.Unmarshal(item["name"], &name)
				_ = json.Unmarshal(item["arguments"], &arguments)
				_ = json.Unmarshal(item["id"], &itemID)
				if callID == call.ID && itemID == strings.Replace(call.ID, "call_", "fc_", 1) && name == call.Name && arguments == string(call.Arguments) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("调用身份或参数改变：%v", item)
			}
		case "function_call_output":
			if seenCalls[callID] != 1 {
				t.Fatalf("工具结果出现在调用之前或调用重复：%s", callID)
			}
			seenOutputs[callID]++
			var output string
			_ = json.Unmarshal(item["output"], &output)
			if output != outputs[callID] {
				t.Fatalf("工具正文投影或最新批次变化：call_id=%s，输出=%q", callID, output)
			}
		}
	}
	if seenOpaque != 1 || len(seenCalls) != len(calls) || len(seenOutputs) != len(calls) {
		t.Fatalf("推理项或调用配对缺失：opaque=%d calls=%v outputs=%v", seenOpaque, seenCalls, seenOutputs)
	}
	for _, call := range calls {
		if seenCalls[call.ID] != 1 || seenOutputs[call.ID] != 1 {
			t.Fatalf("调用未恰好配对：%s", call.ID)
		}
	}
}

func assertAnalysisChatIdentity(t *testing.T, raw json.RawMessage, calls []schema.ToolCall, outputs map[string]string) {
	t.Helper()
	var messages []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	seenCalls, seenOutputs := make(map[string]int), make(map[string]int)
	for _, message := range messages {
		for _, actual := range message.ToolCalls {
			seenCalls[actual.ID]++
			matched := false
			for _, call := range calls {
				if actual.ID == call.ID && actual.Function.Name == call.Name && actual.Function.Arguments == string(call.Arguments) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("Chat 调用身份变化：%+v", actual)
			}
		}
		if message.Role == "tool" {
			if seenCalls[message.ToolCallID] != 1 {
				t.Fatalf("Chat 工具结果出现在调用之前或调用重复：%s", message.ToolCallID)
			}
			seenOutputs[message.ToolCallID]++
			if message.Content != outputs[message.ToolCallID] {
				t.Fatalf("Chat 工具正文或最新批次变化：%+v", message)
			}
		}
	}
	if len(seenCalls) != len(calls) || len(seenOutputs) != len(calls) {
		t.Fatalf("Chat 调用结果不完整：%v / %v", seenCalls, seenOutputs)
	}
	for _, call := range calls {
		if seenCalls[call.ID] != 1 || seenOutputs[call.ID] != 1 {
			t.Fatalf("Chat 调用未恰好配对：%s", call.ID)
		}
	}
}
