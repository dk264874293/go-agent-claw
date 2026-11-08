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
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/provider"
)

// 30个请求全部只到本地HTTP；验证实际SDK编码，不能将脚本轨迹当作真实质量或Token收益。
func TestReadFeatureEightCombinationsUseThreeRealSDKProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			captured := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				raw, _ := io.ReadAll(req.Body)
				captured <- raw
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "responses":
					_, _ = io.WriteString(w, `{"id":"resp_features","object":"response","status":"completed","output":[{"type":"message","id":"msg_features","role":"assistant","content":[{"type":"output_text","text":"继续","annotations":[]}]}]}`)
				case "anthropic":
					_, _ = io.WriteString(w, `{"id":"msg_features","type":"message","role":"assistant","content":[{"type":"text","text":"继续"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				default:
					_, _ = io.WriteString(w, `{"id":"chat_features","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"继续"}}]}`)
				}
			}))
			defer server.Close()
			var model provider.LLMProvider
			if protocol == "anthropic" {
				target, _ := url.Parse(server.URL)
				previous := http.DefaultTransport
				http.DefaultTransport = qualityLocalTransport{target: target, base: previous}
				defer func() { http.DefaultTransport = previous }()
				t.Setenv("ZHIPU_API_KEY", "offline-features")
				model = provider.NewZhipuClaudeProvider("claude-feature-fixture")
			} else {
				t.Setenv("OPENAI_API_KEY", "offline-features")
				t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
				name := "feature-chat-fixture"
				if protocol == "responses" {
					name = "gpt-6.1-sol"
				}
				model = provider.NewOpenAIProvider(name)
			}
			generate := func(request TaskSpec, mask int) []byte {
				ctx, r, history := memoryV2Fixture(t)
				request.Goal, request.Kind, request.Mode = r.state.Spec.Goal, r.state.Spec.Kind, r.state.Spec.Mode
				if err := NormalizeReadFeatures(&request); err != nil {
					t.Fatal(err)
				}
				r.state.Spec = request
				// 固定观察时间及初始额度，使两份独立fixture的请求可以逐字节比较。
				r.started = time.Time{}
				for i := range history {
					if len(history[i].ToolCalls) == 0 {
						continue
					}
					call := history[i].ToolCalls[0]
					args, _ := json.Marshal(string(call.Arguments))
					history[i].ResponseItems = []json.RawMessage{
						json.RawMessage(`{"type":"reasoning","id":"rs_features","summary":[],"encrypted_content":"opaque-feature-contract"}`),
						json.RawMessage(`{"type":"future_kind","opaque":{"preserve":true,"number":9007199254740993}}`),
						json.RawMessage(`{"type":"function_call","id":"fc` + call.ID + `","call_id":"` + call.ID + `","name":"read_file","arguments":` + string(args) + `,"status":"completed"}`),
					}
				}
				history[0].Content += r.readFeaturePrompt(ctx)
				session := ctxpkg.NewSession("feature-wire", "")
				session.Append(history...)
				original, _ := json.Marshal(session.GetWorkingMemory(0))
				engine := NewAgentEngine(model, nil, EngineOptions{})
				defs := r.visibleTools(ctx, NewWorkspaceRegistry(""))
				if _, err := engine.generate(ctx, r, session.GetWorkingMemory(0), defs); err != nil {
					t.Fatal(err)
				}
				body := <-captured
				after, _ := json.Marshal(session.GetWorkingMemory(0))
				if !bytes.Equal(original, after) {
					t.Fatal("实际请求改写原Session或Responses项")
				}
				if r.state.Usage.ModelCalls != 1 || r.state.Usage.ToolCalls != 0 {
					t.Fatal("实验开关产生额外调用")
				}
				assertReadFeatureSDKContract(t, body, protocol, mask)
				return body
			}
			byDefault := generate(TaskSpec{}, 0)
			explicitBaseline := generate(TaskSpec{ReadContextVersion: 2}, 0)
			if !bytes.Equal(byDefault, explicitBaseline) {
				t.Fatal("新普通默认请求与显式baseline2实际编码不一致")
			}
			for mask := 0; mask < 8; mask++ {
				body := generate(TaskSpec{ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: mask&1 != 0, LocalChecks: mask&2 != 0, ClaimFocus: mask&4 != 0}}, mask)
				if mask == 0 && !bytes.Equal(body, explicitBaseline) {
					t.Fatal("全部关闭仍混入新增Schema/提示/投影")
				}
			}
		})
	}
}

func assertReadFeatureSDKContract(t *testing.T, raw []byte, protocol string, mask int) {
	t.Helper()
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		t.Fatal("SDK请求JSON无效")
	}
	var defs []map[string]json.RawMessage
	if json.Unmarshal(body["tools"], &defs) != nil {
		t.Fatal("SDK工具定义丢失")
	}
	foundCheck, sourceContract := false, false
	for _, def := range defs {
		if protocol == "chat" {
			var nested map[string]json.RawMessage
			if json.Unmarshal(def["function"], &nested) != nil {
				t.Fatal("Chat工具无效")
			}
			def = nested
		}
		var name string
		_ = json.Unmarshal(def["name"], &name)
		if name == "check_evidence" {
			foundCheck = true
		}
		key := "parameters"
		if protocol == "anthropic" {
			key = "input_schema"
		}
		if name == "read_evidence" {
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if json.Unmarshal(def[key], &schema) != nil {
				t.Fatal("来源工具Schema无效")
			}
			sourceContract = schema.Properties["evidence_id"] != nil
		}
	}
	if foundCheck != (mask&2 != 0) || sourceContract != (mask&1 != 0) {
		t.Fatalf("SDK中开关联动: mask=%d check=%t source=%t", mask, foundCheck, sourceContract)
	}
	// 先解析字符串，避免JSON编码中的转义或Anthropic系统块影响中文匹配。
	decoded := string(raw)
	var material any
	_ = json.Unmarshal(raw, &material)
	decoded = fmt.Sprint(material)
	if strings.Contains(decoded, claimFocusPrompt) != (mask&4 != 0) || strings.Contains(decoded, ordinarySourceQualityPrompt) {
		t.Fatal("短命题提示联动、缺失或旧长提示泄露")
	}
	assertQualityFirstUser(t, raw, protocol)
	seenCalls, seenOutputs := map[string]bool{}, map[string]bool{}
	if protocol == "responses" {
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(body["input"], &items)
		opaque, unknown := 0, 0
		for _, item := range items {
			var kind, id, name, args, output string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &id)
			switch kind {
			case "reasoning":
				if string(item["encrypted_content"]) == `"opaque-feature-contract"` {
					opaque++
				}
			case "future_kind":
				if bytes.Contains(item["opaque"], []byte(`9007199254740993`)) {
					unknown++
				}
			case "function_call":
				_ = json.Unmarshal(item["name"], &name)
				_ = json.Unmarshal(item["arguments"], &args)
				assertFeatureCall(t, id, name, args, seenCalls)
			case "function_call_output":
				_ = json.Unmarshal(item["output"], &output)
				assertFeatureOutput(t, id, output, seenCalls, seenOutputs)
			}
		}
		if opaque != 2 || unknown != 2 {
			t.Fatal("Responses不透明/未知项被删除或改写")
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
		_ = json.Unmarshal(body["messages"], &messages)
		for _, message := range messages {
			if protocol == "chat" {
				for _, call := range message.ToolCalls {
					assertFeatureCall(t, call.ID, call.Function.Name, call.Function.Arguments, seenCalls)
				}
				if message.ToolCallID != "" {
					var output string
					_ = json.Unmarshal(message.Content, &output)
					assertFeatureOutput(t, message.ToolCallID, output, seenCalls, seenOutputs)
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
				_ = json.Unmarshal(message.Content, &blocks)
				for _, block := range blocks {
					if block.Type == "tool_use" {
						assertFeatureCall(t, block.ID, block.Name, string(block.Input), seenCalls)
					}
					if block.Type == "tool_result" {
						var text string
						if json.Unmarshal(block.Content, &text) != nil {
							var parts []struct {
								Text string `json:"text"`
							}
							_ = json.Unmarshal(block.Content, &parts)
							for _, part := range parts {
								text += part.Text
							}
						}
						assertFeatureOutput(t, block.ToolUseID, text, seenCalls, seenOutputs)
					}
				}
			}
		}
	}
	if len(seenCalls) != 2 || len(seenOutputs) != 2 {
		t.Fatalf("工具配对数量错误: %d/%d", len(seenCalls), len(seenOutputs))
	}
}

func assertFeatureCall(t *testing.T, id, name, args string, seen map[string]bool) {
	t.Helper()
	if seen[id] || name != "read_file" || (id != "callE1" && id != "callE2") {
		t.Fatal("工具身份被修改")
	}
	var actual map[string]any
	_ = json.Unmarshal([]byte(args), &actual)
	want := map[string]any{"path": strings.TrimPrefix(id, "call") + ".md"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("业务参数变化: %s", args)
	}
	seen[id] = true
}

func assertFeatureOutput(t *testing.T, id, output string, calls, outputs map[string]bool) {
	t.Helper()
	if !calls[id] || outputs[id] || !strings.HasPrefix(output, strings.TrimPrefix(id, "call")+" 决定性事实\n") || strings.Count(output, "材料原文。") != 500 {
		t.Fatal("未处理原文丢失或工具配对断裂")
	}
	outputs[id] = true
}
