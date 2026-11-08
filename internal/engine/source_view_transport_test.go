package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// 通过真实SDK和本地HTTP服务验证新来源视图入口；不调用外部模型，不推断Token收益。
func TestSourceViewConsumedNotesReachThreeRealSDKProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			for _, policy := range []int{3, 4} {
				t.Run(fmt.Sprintf("policy%d", policy), func(t *testing.T) {
					captured := make(chan []byte, 2)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
						raw, _ := io.ReadAll(request.Body)
						captured <- raw
						w.Header().Set("Content-Type", "application/json")
						switch protocol {
						case "responses":
							_, _ = io.WriteString(w, `{"id":"resp_view","object":"response","status":"completed","output":[{"type":"message","id":"msg_view","role":"assistant","content":[{"type":"output_text","text":"继续","annotations":[]}]}]}`)
						case "anthropic":
							_, _ = io.WriteString(w, `{"id":"msg_view","type":"message","role":"assistant","content":[{"type":"text","text":"继续"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						default:
							_, _ = io.WriteString(w, `{"id":"chat_view","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"继续"}}]}`)
						}
					}))
					defer server.Close()
					var model provider.LLMProvider
					if protocol == "anthropic" {
						target, _ := url.Parse(server.URL)
						previous := http.DefaultTransport
						http.DefaultTransport = qualityLocalTransport{target: target, base: previous}
						defer func() { http.DefaultTransport = previous }()
						t.Setenv("ZHIPU_API_KEY", "offline-source-view")
						model = provider.NewZhipuClaudeProvider("claude-source-view-fixture")
					} else {
						t.Setenv("OPENAI_API_KEY", "offline-source-view")
						t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
						name := "source-view-chat-fixture"
						if protocol == "responses" {
							name = "gpt-6.1-sol"
						}
						model = provider.NewOpenAIProvider(name)
					}
					ctx, r, _ := sourceViewFixture(t)
					if policy == 4 {
						r.state.Spec.ReadContextVersion = 4
						r.state.Spec.ReadFeatures = &ReadFeatureSpec{Version: 1, SourceViews: true}
					}
					origin := r.analysisRuntime.sources["E1"]
					business := `{"id":"` + origin.ArtifactID + `","offset":3,"limit":90}`
					if policy == 4 {
						business = `{"evidence_id":"` + origin.ID + `","offset":3,"limit":90}`
					}
					notes, _ := json.Marshal(carrierSummary(origin.ID, strings.Repeat("首行是E1决定性事实；本摘要仅限定已交付版本和读取范围。", 20)))
					arguments := strings.TrimSuffix(business, "}") + `,"_work_notes":` + string(notes) + `}`
					call := schema.ToolCall{ID: "source_view_notes", Name: "read_evidence", Arguments: json.RawMessage(arguments)}
					batch, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{call})
					if err != nil || !batch.valid || batch.response.Accepted != 1 || len(batch.calls) != 1 || string(batch.calls[0].Arguments) != business {
						t.Fatalf("新入口没有实际消费有效载体或修改了业务参数：%+v %v", batch, err)
					}
					engine := NewAgentEngine(model, nil, EngineOptions{})
					results, err := engine.executeBatch(ctx, r, NewExplorerRegistry(r.state.WorkDir), nil, batch.calls, "")
					if err != nil || len(results) != 1 || results[0].Status != schema.ToolSucceeded || results[0].Read == nil || results[0].Read.SourceView == nil {
						t.Fatalf("剥离载体后没有正常执行来源视图：%+v %v", results, err)
					}
					encodedArgs, _ := json.Marshal(arguments)
					items := []json.RawMessage{
						json.RawMessage(`{"type":"reasoning","id":"rs_view","summary":[],"encrypted_content":"opaque-source-view-exact"}`),
						json.RawMessage(`{"type":"future_kind","opaque":{"preserve":true,"large":9007199254740993}}`),
						json.RawMessage(`{"type":"function_call","id":"fc_view","call_id":"source_view_notes","name":"read_evidence","arguments":` + string(encodedArgs) + `,"status":"completed"}`),
					}
					output := observation(results[0]) + "\n" + batch.receipts[0]
					session := ctxpkg.NewSession("source-view-wire", "")
					session.Append(
						schema.Message{Role: schema.RoleSystem, Content: "只读核对当前原文，保留版本和范围。"},
						schema.Message{Role: schema.RoleUser, Content: "核对已读资料，继续正常任务。"},
						schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{call}, ResponseItems: items},
						schema.Message{Role: schema.RoleUser, ToolCallID: call.ID, ToolStatus: results[0].Status, Content: output, ReadEvidenceID: results[0].Read.EvidenceID, ReadActionID: results[0].Read.ActionID},
					)
					original, _ := json.Marshal(session.GetWorkingMemory(0))
					definition := (evidenceTool{}).Definition()
					if policy == 4 {
						definition = sourceViewDefinitionV2(definition)
					} else {
						definition = sourceViewDefinition(definition)
					}
					definition = r.withWorkNotesCarrier(definition)
					defs := []schema.ToolDefinition{definition}
					// 未投影历史作为实际SDK编码对照，随后经过引擎生产请求路径清理已消费载体。
					if _, err := model.Generate(ctx, session.GetWorkingMemory(0), defs); err != nil {
						t.Fatal(err)
					}
					before := <-captured
					if _, err := engine.generate(ctx, r, session.GetWorkingMemory(0), defs); err != nil {
						t.Fatal(err)
					}
					after := <-captured
					assertSourceViewSDKCall(t, before, protocol, arguments, output, policy)
					assertSourceViewSDKCall(t, after, protocol, business, output, policy)
					assertQualityFirstUser(t, after, protocol)
					unchanged, _ := json.Marshal(session.GetWorkingMemory(0))
					if !bytes.Equal(original, unchanged) {
						t.Fatal("后续请求清理改写了原Session或Responses不透明项")
					}
					if !r.isConsumedWorkNotesCarrier("root", r.runID, call) || !containsString(r.state.ReadMemory.ProcessedReads["root"], origin.ID) {
						t.Fatal("此测试未经过真实载体消费与来源处理资格")
					}
					if r.state.Usage.ToolCalls != 1 || r.state.Usage.ModelCalls != 1 || len(captured) != 0 {
						t.Fatalf("新增专用模型阶段或重复执行：%+v", r.state.Usage)
					}
				})
			}
		})
	}
}

func assertSourceViewSDKCall(t *testing.T, raw []byte, protocol, arguments, output string, policy int) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var defs []map[string]json.RawMessage
	if json.Unmarshal(body["tools"], &defs) != nil || len(defs) != 1 {
		t.Fatal("工具定义丢失")
	}
	definition := defs[0]
	key := "parameters"
	if protocol == "chat" {
		if json.Unmarshal(definition["function"], &definition) != nil {
			t.Fatal("Chat定义无效")
		}
	}
	if protocol == "anthropic" {
		key = "input_schema"
	}
	var parameters struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	wantRequired := []string{"id"}
	if policy == 4 {
		wantRequired = nil
	}
	if json.Unmarshal(definition[key], &parameters) != nil || !reflect.DeepEqual(parameters.Required, wantRequired) || parameters.Properties["_work_notes"] == nil || parameters.Properties["review"] == nil || (parameters.Properties["evidence_id"] != nil) != (policy == 4) {
		t.Fatalf("新工具Schema或必填字段变化：%s", definition[key])
	}
	callCount, resultCount := 0, 0
	if protocol == "responses" {
		var items []map[string]json.RawMessage
		if json.Unmarshal(body["input"], &items) != nil {
			t.Fatal("Responses输入无效")
		}
		opaque, unknown := false, false
		for _, item := range items {
			var kind, id, name, actual, result string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &id)
			switch kind {
			case "reasoning":
				opaque = string(item["encrypted_content"]) == `"opaque-source-view-exact"`
			case "future_kind":
				unknown = bytes.Contains(item["opaque"], []byte(`9007199254740993`)) && bytes.Contains(item["opaque"], []byte(`"preserve":true`))
			case "function_call":
				callCount++
				_ = json.Unmarshal(item["name"], &name)
				_ = json.Unmarshal(item["arguments"], &actual)
				if id != "source_view_notes" || name != "read_evidence" || actual != arguments || string(item["id"]) != `"fc_view"` {
					t.Fatalf("Responses业务参数或身份变化：%s", raw)
				}
			case "function_call_output":
				resultCount++
				_ = json.Unmarshal(item["output"], &result)
				if callCount != 1 || id != "source_view_notes" || result != output {
					t.Fatal("Responses工具结果配对或最新原文变化")
				}
			}
		}
		if !opaque || !unknown {
			t.Fatal("Responses不透明/未知项发生变化")
		}
	} else {
		var messages []struct {
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
			t.Fatal("SDK消息无效")
		}
		for _, message := range messages {
			if protocol == "chat" {
				for _, call := range message.ToolCalls {
					callCount++
					if call.ID != "source_view_notes" || call.Function.Name != "read_evidence" || call.Function.Arguments != arguments {
						t.Fatal("Chat业务参数或身份变化")
					}
				}
				if message.ToolCallID != "" {
					resultCount++
					var result string
					_ = json.Unmarshal(message.Content, &result)
					if callCount != 1 || message.ToolCallID != "source_view_notes" || result != output {
						t.Fatal("Chat工具结果配对或最新原文变化")
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
						if block.ID != "source_view_notes" || block.Name != "read_evidence" || !reflect.DeepEqual(want, actual) {
							t.Fatal("Anthropic业务参数或身份变化")
						}
					}
					if block.Type == "tool_result" {
						resultCount++
						var result string
						if json.Unmarshal(block.Content, &result) != nil {
							var content []struct {
								Text string `json:"text"`
							}
							if json.Unmarshal(block.Content, &content) != nil {
								t.Fatal("Anthropic结果内容无效")
							}
							for _, item := range content {
								result += item.Text
							}
						}
						if callCount != 1 || block.ToolUseID != "source_view_notes" || result != output {
							t.Fatal("Anthropic工具结果配对或最新原文变化")
						}
					}
				}
			}
		}
	}
	if callCount != 1 || resultCount != 1 {
		t.Fatalf("工具调用/结果配对数量错误：%d/%d", callCount, resultCount)
	}
}
