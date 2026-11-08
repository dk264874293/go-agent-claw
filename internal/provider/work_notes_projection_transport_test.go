package provider

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

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// 使用固定请求投影验证三套实际SDK序列化，不调用真实模型、不推断Token或语义质量。
func TestWorkNotesProjectionWireFixturesAcrossThreeSDKProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			captured := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				raw, _ := io.ReadAll(request.Body)
				captured <- raw
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "responses":
					_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}]}`)
				case "anthropic":
					_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"完成"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				default:
					_, _ = io.WriteString(w, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"完成"}}]}`)
				}
			}))
			defer server.Close()
			var model LLMProvider
			switch protocol {
			case "responses":
				model = newCompatibleProvider("gpt-6.1-sol", "offline", server.URL+"/v1/")
			case "anthropic":
				model = newClaudeProvider("claude-fixture", "offline", server.URL)
			default:
				model = newCompatibleProvider("fixture-chat", "offline", server.URL+"/v1/")
			}
			defs := []schema.ToolDefinition{{Name: "read_lines", InputSchema: map[string]any{
				"type": "object", "required": []string{"path", "start_line", "end_line"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"}, "start_line": map[string]any{"type": "integer"}, "end_line": map[string]any{"type": "integer"},
					"line_numbers": map[string]any{"type": "boolean"}, "_work_notes": map[string]any{"type": "object"},
				},
			}}}
			business := `{"path":"source.md","start_line":1,"end_line":2,"line_numbers":true}`
			metadata, _ := json.Marshal(map[string]any{"schema_version": 1, "findings": []any{}, "summaries": map[string]string{"E1": strings.Repeat("已处理读取的摘要。", 500)}})
			original := strings.TrimSuffix(business, "}") + `,"_work_notes":` + string(metadata) + `}`
			var bodies [][]byte
			for _, arguments := range []string{original, business} {
				argString, _ := json.Marshal(arguments)
				messages := []schema.Message{
					{Role: schema.RoleSystem, Content: "当前笔记引用E1，原文资格沿用当前证据目录。"},
					{Role: schema.RoleUser, Content: "继续核对"},
					{Role: schema.RoleAssistant, Content: "普通文字保留", ToolCalls: []schema.ToolCall{{ID: "call_1", Name: "read_lines", Arguments: json.RawMessage(arguments)}}, ResponseItems: []json.RawMessage{
						json.RawMessage(`{"type":"reasoning","id":"rs1","summary":[],"encrypted_content":"opaque-wire-fixture"}`),
						json.RawMessage(`{"type":"future_kind","opaque":{"x":true}}`),
						json.RawMessage(`{"type":"function_call","id":"fc1","call_id":"call_1","name":"read_lines","arguments":` + string(argString) + `,"status":"completed"}`),
					}},
					{Role: schema.RoleUser, ToolCallID: "call_1", Content: "最新批次及尚未处理的原文完整保留"},
				}
				if _, err := model.Generate(context.Background(), messages, defs); err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, <-captured)
			}
			if len(bodies[1])*10 >= len(bodies[0])*7 {
				t.Fatalf("实际wire未反映元数据清理: %d -> %d", len(bodies[0]), len(bodies[1]))
			}
			for i, raw := range bodies {
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) != nil {
					t.Fatal("SDK请求JSON无效")
				}
				assertCarrierWireRequired(t, body["tools"], protocol)
				want := original
				if i == 1 {
					want = business
				}
				assertCarrierWireCallAndOutput(t, body, protocol, want)
			}
			if _, ok := defs[0].InputSchema.(map[string]any)["required"].([]string); !ok {
				t.Fatal("required的Go类型发生变化")
			}
		})
	}
}

func assertCarrierWireRequired(t *testing.T, raw json.RawMessage, protocol string) {
	t.Helper()
	var defs []map[string]json.RawMessage
	if json.Unmarshal(raw, &defs) != nil || len(defs) != 1 {
		t.Fatal("工具定义丢失")
	}
	def := defs[0]
	key := "parameters"
	if protocol == "chat" {
		if json.Unmarshal(def["function"], &def) != nil {
			t.Fatal("Chat工具无效")
		}
	}
	if protocol == "anthropic" {
		key = "input_schema"
	}
	var parameters struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(def[key], &parameters) != nil || !reflect.DeepEqual(parameters.Required, []string{"path", "start_line", "end_line"}) || parameters.Properties["_work_notes"].Type != "object" || parameters.Properties["line_numbers"].Type != "boolean" {
		t.Fatalf("optional或required语义变化: %s", def[key])
	}
}

func assertCarrierWireCallAndOutput(t *testing.T, body map[string]json.RawMessage, protocol, arguments string) {
	t.Helper()
	callCount, outputCount := 0, 0
	if protocol == "responses" {
		var items []map[string]json.RawMessage
		if json.Unmarshal(body["input"], &items) != nil {
			t.Fatal("Responses输入无效")
		}
		opaque, unknown := false, false
		for _, item := range items {
			var kind, id, arg, name, output string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &id)
			if kind == "reasoning" {
				opaque = bytes.Contains(item["encrypted_content"], []byte("opaque-wire-fixture"))
			}
			if kind == "future_kind" {
				unknown = string(item["opaque"]) == `{"x":true}`
			}
			if kind == "function_call" {
				callCount++
				_ = json.Unmarshal(item["arguments"], &arg)
				_ = json.Unmarshal(item["name"], &name)
				if id != "call_1" || name != "read_lines" || arg != arguments {
					t.Fatalf("Responses调用变化: %v", item)
				}
			}
			if kind == "function_call_output" {
				outputCount++
				_ = json.Unmarshal(item["output"], &output)
				if callCount != 1 || id != "call_1" || output != "最新批次及尚未处理的原文完整保留" {
					t.Fatal("Responses配对或原文变化")
				}
			}
		}
		if !opaque || !unknown {
			t.Fatal("Responses opaque/unknown项丢失")
		}
	} else {
		var messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(body["messages"], &messages) != nil {
			t.Fatal("消息输入无效")
		}
		for _, message := range messages {
			if protocol == "chat" {
				for _, call := range message.ToolCalls {
					callCount++
					if call.ID != "call_1" || call.Function.Name != "read_lines" || call.Function.Arguments != arguments {
						t.Fatal("Chat调用变化")
					}
				}
				if message.ToolCallID != "" {
					outputCount++
					if callCount != 1 || message.ToolCallID != "call_1" || !bytes.Contains(message.Content, []byte("最新批次及尚未处理的原文完整保留")) {
						t.Fatal("Chat配对或原文变化")
					}
				}
			} else {
				var blocks []struct {
					Type      string          `json:"type"`
					ID        string          `json:"id"`
					Name      string          `json:"name"`
					Input     json.RawMessage `json:"input"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
				}
				if json.Unmarshal(message.Content, &blocks) != nil {
					t.Fatal("Anthropic块无效")
				}
				for _, block := range blocks {
					if block.Type == "tool_use" {
						callCount++
						var want, actual any
						_ = json.Unmarshal([]byte(arguments), &want)
						_ = json.Unmarshal(block.Input, &actual)
						if block.ID != "call_1" || block.Name != "read_lines" || !reflect.DeepEqual(want, actual) {
							t.Fatal("Anthropic调用变化")
						}
					}
					if block.Type == "tool_result" {
						outputCount++
						if callCount != 1 || block.ToolUseID != "call_1" || !bytes.Contains(block.Content, []byte("最新批次及尚未处理的原文完整保留")) {
							t.Fatal("Anthropic配对或原文变化")
						}
					}
				}
			}
		}
	}
	if callCount != 1 || outputCount != 1 {
		t.Fatalf("调用配对缺失: %d/%d", callCount, outputCount)
	}
}
