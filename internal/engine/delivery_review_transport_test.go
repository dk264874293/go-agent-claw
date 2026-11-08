package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// deliveryWireProvider只观察真实引擎调用；业务响应仍通过实际SDK从本地HTTP读取。
type deliveryWireProvider struct {
	inner     provider.LLMProvider
	before    func(context.Context, int) (schema.Message, error)
	replies   chan<- schema.Message
	session   *ctxpkg.Session
	calls     int
	stages    []string
	prefix    []schema.Message
	originals map[string]schema.Message
}

func (p *deliveryWireProvider) Generate(ctx context.Context, messages []schema.Message, definitions []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	p.stages = append(p.stages, modelStage(ctx))
	history := p.session.GetWorkingMemory(0)
	if len(history) < len(p.prefix) {
		return nil, fmt.Errorf("原Session历史被缩短")
	}
	previous, _ := json.Marshal(p.prefix)
	current, _ := json.Marshal(history[:len(p.prefix)])
	if len(p.prefix) > 0 && !bytes.Equal(previous, current) {
		return nil, fmt.Errorf("请求投影改写此前Session消息")
	}
	plan, err := p.before(ctx, p.calls)
	if err != nil {
		return nil, err
	}
	p.replies <- plan
	before, _ := json.Marshal(history)
	msg, err := p.inner.Generate(ctx, messages, definitions)
	after, _ := json.Marshal(p.session.GetWorkingMemory(0))
	if !bytes.Equal(before, after) {
		return nil, fmt.Errorf("SDK调用改变原Session")
	}
	_ = json.Unmarshal(before, &p.prefix)
	if err == nil && msg != nil {
		for _, call := range msg.ToolCalls {
			raw, _ := json.Marshal(msg)
			var copy schema.Message
			_ = json.Unmarshal(raw, &copy)
			p.originals[call.ID] = copy
		}
	}
	return msg, err
}

func TestDeliveryReviewUsesThreeRealSDKProtocolsWithoutExtraAdmission(t *testing.T) {
	for _, protocol := range []string{"chat", "anthropic", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
					dir := t.TempDir()
					if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("版本二\n非空检查不证明合法性。\n"), 0600); err != nil {
						t.Fatal(err)
					}
					replies := make(chan schema.Message, 1)
					var mu sync.Mutex
					var captured [][]byte
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						raw, err := io.ReadAll(req.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						mu.Lock()
						captured = append(captured, append([]byte(nil), raw...))
						number := len(captured)
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(deliveryWireResponse(protocol, number, <-replies))
					}))
					defer server.Close()
					model := deliveryWireSDK(t, protocol, server.URL)
					session := ctxpkg.NewSession("交付处理真实SDK", dir)
					const final = `{"answer":"只依据已读版本二","verified":false,"value":0}`
					const published = "版本二；非空不证明合法，运行效果未实测。"
					p := &deliveryWireProvider{inner: model, replies: replies, session: session, originals: map[string]schema.Message{}}
					p.before = func(ctx context.Context, n int) (schema.Message, error) {
						call := func(id, name string, args map[string]any) schema.Message {
							raw, _ := json.Marshal(args)
							return schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: id, Name: name, Arguments: raw}}}
						}
						switch n {
						case 1:
							return call("wire_read", "read_file", map[string]any{"path": "a.md", "line_numbers": true}), nil
						case 2:
							content := published
							args := map[string]any{"path": "result.md", "content": content, "expected_version": "missing"}
							if enabled {
								content = "候选内容不得执行"
								args["content"] = content
								evidence := analysisIntegrationEvidence(ctx)
								if len(evidence) != 1 {
									return schema.Message{}, fmt.Errorf("缺少本响应可处理的原文")
								}
								args["_work_notes"] = carrierSummary(evidence[0].ID, "已核对原文版本，文档处置尚未登记")
							}
							return call("wire_draft", "write_file", args), nil
						case 3:
							if !enabled {
								return schema.Message{Role: schema.RoleAssistant, Content: final}, nil
							}
							if _, err := os.Stat(filepath.Join(dir, "result.md")); !errors.Is(err, os.ErrNotExist) {
								return schema.Message{}, fmt.Errorf("被退回候选已执行：%v", err)
							}
							r := currentRun(ctx)
							if modelStage(ctx) != "delivery_review" || r.state.Usage.ModelCalls != 3 || r.state.Usage.Turns != 3 || r.state.Usage.ToolCalls != 2 || len(r.state.Pending) != 0 {
								return schema.Message{}, fmt.Errorf("处理阶段重复准入或候选创建Pending：%+v %s", r.state.Usage, modelStage(ctx))
							}
							var evidence AnalysisEvidence
							for _, e := range r.analysisRuntime.sources {
								if e.Receipt.Path == "a.md" {
									evidence = e
								}
							}
							if evidence.ID == "" {
								return schema.Message{}, fmt.Errorf("处理阶段丢失实际读取身份")
							}
							if _, err := r.analysisLookup(ctx, true)(evidence.ID); err != nil {
								return schema.Message{}, fmt.Errorf("处理阶段未保留成功交付资格：%w", err)
							}
							if !containsString(r.state.ReadMemory.ProcessedReads[observe.Identity(ctx).AgentID], evidence.ID) {
								return schema.Message{}, fmt.Errorf("场景未覆盖原文已处理但文档处置仍未完成")
							}
							notes := map[string]any{"schema_version": 1, "findings": []any{map[string]any{"id": "F1", "kind": "fact", "statement": "首行注明版本二", "context": "仅限所选原文版本", "evidence": []any{map[string]any{"evidence_id": evidence.ID, "start_line": 1, "end_line": 1, "quote": "版本二"}}}}, "processed_reads": []string{evidence.ID}, "summaries": map[string]string{evidence.ID: "已确认版本二及非空检查的证明边界，运行效果仍未实测"}, "source_reviews": []any{map[string]any{"evidence_ids": []string{evidence.ID}, "finding_ids": []string{"F1"}, "disposition": "addressed", "reason": "已有来源发现，保持版本及运行边界"}}}
							return call("wire_publish", "write_file", map[string]any{"path": "result.md", "content": published, "expected_version": "missing", "_work_notes": notes}), nil
						case 4:
							if enabled {
								return schema.Message{Role: schema.RoleAssistant, Content: final}, nil
							}
						}
						return schema.Message{}, fmt.Errorf("不应额外请求模型%d", n)
					}
					reg := NewWorkspaceRegistry(dir)
					eng := NewAgentEngine(p, reg, EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 4, MaxToolCalls: 5, MaxTurns: 4}})
					spec := TaskSpec{Goal: "依据已读材料生成result.md，保持版本与执行边界", Mode: tools.ModeWorkspace, Kind: TaskChange}
					if enabled {
						spec.DeliveryReview = &DeliveryReviewSpec{Version: 1}
					}
					result, events := observedRun(t, eng, session, spec)
					models, toolAttempts := 3, 2
					if enabled {
						models, toolAttempts = 4, 3
					}
					if result.Status != NeedsVerification || result.FinalText != final || result.Usage.ModelCalls != models || result.Usage.Turns != models || result.Usage.ToolCalls != toolAttempts || p.calls != models {
						t.Fatalf("真实SDK流程/额度/JSON错误：%+v calls%d", result, p.calls)
					}
					wantStages := []string{"main", "main", "main"}
					if enabled {
						wantStages = []string{"main", "main", "delivery_review", "main"}
					}
					if !reflect.DeepEqual(p.stages, wantStages) {
						t.Fatalf("阶段错误：%v", p.stages)
					}
					if enabled && (result.DeliveryReview == nil || result.DeliveryReview.AdmissionCount != 1 || result.DeliveryReview.Status != "completed" || result.DeliveryReview.Addressed != 1 || len(result.DeliveryReview.Unresolved) != 0) {
						t.Fatalf("一次处置未完成：%+v", result.DeliveryReview)
					}
					if enabled && (len(result.DeliveryReview.ContentComparisons) != 1 || result.DeliveryReview.ContentComparisons[0].Status != "changed" || result.DeliveryReview.ContentComparisons[0].ConfirmedSHA256 != deliveryContentSHA(published)) {
						t.Fatalf("真实SDK修订未关联确认正文：%+v", result.DeliveryReview)
					}
					if !enabled && result.DeliveryReview != nil {
						t.Fatal("默认off产生新交付处理状态")
					}
					data, err := os.ReadFile(filepath.Join(dir, "result.md"))
					if err != nil || string(data) != published || len(result.Delivery.Effects) != 1 {
						t.Fatalf("实际写入/回执错误：%q %v %+v", data, err, result.Delivery)
					}
					deliveryWireEvents(t, events, enabled)
					mu.Lock()
					bodies := append([][]byte(nil), captured...)
					mu.Unlock()
					if len(bodies) != models {
						t.Fatalf("HTTP请求重复：%d", len(bodies))
					}
					for i, body := range bodies {
						assertQualityFirstUser(t, body, protocol)
						assertDeliveryWirePairs(t, body, protocol, min(i, toolAttempts))
						var decoded any
						_ = json.Unmarshal(body, &decoded)
						if enabled && i == 2 {
							prompt := fmt.Sprint(decoded)
							for _, forbidden := range []string{"没有候选时省略载体", "本轮没有可记的读取候选，请省略_work_notes"} {
								if strings.Contains(prompt, forbidden) {
									t.Fatalf("真实SDK处理请求仍禁止处置载体：%s", forbidden)
								}
							}
							if !strings.Contains(prompt, "没有新的读取处理候选") || !strings.Contains(prompt, "若交付前目录仍有待办") {
								t.Fatal("真实SDK请求未传递已处理来源仍可处置的提示")
							}
						}
						if !enabled {
							for _, token := range []string{"source_reviews", "wait_for", "ready_to_resolve", "交付前处理待办"} {
								if strings.Contains(fmt.Sprint(decoded), token) {
									t.Fatalf("off暴露新Schema/提示%q", token)
								}
							}
						}
						if protocol == "responses" {
							assertDeliveryWireOpaque(t, body, min(i, toolAttempts))
						}
					}
					if enabled {
						_, args := deliveryWireCalls(t, bodies[3], protocol)
						if bytes.Contains(args["wire_publish"], []byte("_work_notes")) || bytes.Contains(args["wire_publish"], []byte("source_reviews")) {
							t.Fatal("已消费载体没有从真实SDK请求副本移除")
						}
					}
					for _, msg := range session.GetWorkingMemory(0) {
						for _, c := range msg.ToolCalls {
							original := p.originals[c.ID]
							if !reflect.DeepEqual(msg.ToolCalls, original.ToolCalls) || !reflect.DeepEqual(msg.ResponseItems, original.ResponseItems) {
								t.Fatalf("Session原始调用或opaque项被改写：%s", c.ID)
							}
						}
					}
					if enabled && !bytes.Contains(p.originals["wire_publish"].ToolCalls[0].Arguments, []byte("_work_notes")) {
						t.Fatal("测试未生成真实原始载体")
					}
				})
			}
		})
	}
}

func deliveryWireSDK(t *testing.T, protocol, address string) provider.LLMProvider {
	t.Helper()
	if protocol == "anthropic" {
		target, _ := url.Parse(address)
		previous := http.DefaultTransport
		http.DefaultTransport = qualityLocalTransport{target: target, base: previous}
		t.Cleanup(func() { http.DefaultTransport = previous })
		t.Setenv("ZHIPU_API_KEY", "offline-delivery-review")
		return provider.NewZhipuClaudeProvider("claude-delivery-review-fixture")
	}
	t.Setenv("OPENAI_API_KEY", "offline-delivery-review")
	t.Setenv("OPENAI_BASE_URL", address+"/v1/")
	name := "delivery-review-chat-fixture"
	if protocol == "responses" {
		name = "gpt-6.1-sol"
	}
	return provider.NewOpenAIProvider(name)
}

func deliveryWireResponse(protocol string, number int, msg schema.Message) any {
	if protocol == "anthropic" {
		var blocks []any
		if msg.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, c := range msg.ToolCalls {
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Arguments})
		}
		reason := "end_turn"
		if len(msg.ToolCalls) > 0 {
			reason = "tool_use"
		}
		return map[string]any{"id": fmt.Sprintf("msg_%d", number), "type": "message", "role": "assistant", "content": blocks, "stop_reason": reason, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
	}
	if protocol == "responses" {
		items := []any{map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_dr_%d", number), "summary": []any{}, "encrypted_content": fmt.Sprintf("opaque-delivery-%d", number)}}
		for _, c := range msg.ToolCalls {
			items = append(items, map[string]any{"type": "function_call", "id": "fc_" + c.ID, "call_id": c.ID, "name": c.Name, "arguments": string(c.Arguments), "status": "completed"})
		}
		if msg.Content != "" {
			items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("text_%d", number), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": msg.Content, "annotations": []any{}}}})
		}
		return map[string]any{"id": fmt.Sprintf("resp_%d", number), "object": "response", "status": "completed", "output": items, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
	}
	var calls []any
	for _, c := range msg.ToolCalls {
		calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": string(c.Arguments)}})
	}
	reason := "stop"
	message := map[string]any{"role": "assistant", "content": msg.Content}
	if len(calls) > 0 {
		message["tool_calls"] = calls
		reason = "tool_calls"
	}
	return map[string]any{"id": fmt.Sprintf("chat_%d", number), "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "finish_reason": reason, "message": message}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1}}
}

func deliveryWireEvents(t *testing.T, events []observe.Event, enabled bool) {
	t.Helper()
	handlers, rejected, reviewRequests := 0, 0, 0
	for _, e := range events {
		if e.EventType == "model_started" && e.Payload.Stage == "delivery_review" {
			reviewRequests++
		}
		if e.EventType == "tool_handler_started" && e.Payload.Tool == "write_file" {
			handlers++
			if enabled && e.Payload.ToolCallID == "wire_draft" {
				t.Fatal("被退回候选进入Handler")
			}
		}
		if e.EventType == "tool_finished" && e.Payload.ToolCallID == "wire_draft" && e.Payload.Status == string(schema.ToolRejected) {
			rejected++
		}
	}
	want := 0
	if enabled {
		want = 1
	}
	if handlers != 1 || rejected != want || reviewRequests != want {
		t.Fatalf("真实阶段/Handler/退回记录错误：handlers%d rejected%d review%d", handlers, rejected, reviewRequests)
	}
}

func assertDeliveryWirePairs(t *testing.T, raw []byte, protocol string, expected int) {
	t.Helper()
	outputs, args := deliveryWireCalls(t, raw, protocol)
	if len(outputs) != expected || len(args) != expected {
		t.Fatalf("工具配对数量错误：%d/%d，期望%d", len(args), len(outputs), expected)
	}
	for id := range args {
		if _, ok := outputs[id]; !ok {
			t.Fatalf("调用没有结果：%s", id)
		}
	}
}

func deliveryWireCalls(t *testing.T, raw []byte, protocol string) (map[string]string, map[string]json.RawMessage) {
	t.Helper()
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		t.Fatal("SDK请求不是JSON")
	}
	outputs, args := map[string]string{}, map[string]json.RawMessage{}
	addCall := func(id string, a json.RawMessage) {
		if _, ok := args[id]; ok {
			t.Fatalf("重复调用%s", id)
		}
		args[id] = a
	}
	addOutput := func(id, text string) {
		if _, ok := args[id]; !ok {
			t.Fatalf("结果先于调用%s", id)
		}
		if _, ok := outputs[id]; ok {
			t.Fatalf("重复结果%s", id)
		}
		outputs[id] = text
	}
	if protocol == "responses" {
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(body["input"], &items)
		for _, item := range items {
			var kind, id, value string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &id)
			if kind == "function_call" {
				_ = json.Unmarshal(item["arguments"], &value)
				addCall(id, json.RawMessage(value))
			}
			if kind == "function_call_output" {
				_ = json.Unmarshal(item["output"], &value)
				addOutput(id, value)
			}
		}
		return outputs, args
	}
	var messages []struct {
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(body["messages"], &messages)
	for _, m := range messages {
		if protocol == "chat" {
			for _, c := range m.ToolCalls {
				addCall(c.ID, json.RawMessage(c.Function.Arguments))
			}
			if m.ToolCallID != "" {
				var v string
				_ = json.Unmarshal(m.Content, &v)
				addOutput(m.ToolCallID, v)
			}
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			if b.Type == "tool_use" {
				addCall(b.ID, b.Input)
			}
			if b.Type == "tool_result" {
				var text string
				if json.Unmarshal(b.Content, &text) != nil {
					var parts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(b.Content, &parts)
					for _, part := range parts {
						text += part.Text
					}
				}
				addOutput(b.ToolUseID, text)
			}
		}
	}
	return outputs, args
}

func assertDeliveryWireOpaque(t *testing.T, raw []byte, count int) {
	t.Helper()
	var body struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(raw, &body)
	seen := map[string]bool{}
	for _, item := range body.Input {
		var kind, opaque string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["encrypted_content"], &opaque)
		if kind == "reasoning" {
			if seen[opaque] {
				t.Fatal("opaque重复回放")
			}
			seen[opaque] = true
		}
	}
	for i := 1; i <= count; i++ {
		if !seen[fmt.Sprintf("opaque-delivery-%d", i)] {
			t.Fatalf("Responses opaque第%d项丢失：%s", i, raw)
		}
	}
	if len(seen) != count {
		t.Fatalf("opaque数量错误：%d/%d", len(seen), count)
	}
}
