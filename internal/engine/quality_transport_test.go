package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// qualityLocalTransport 将公开Anthropic构造器的固定域名限定到本地夹具，不发外网请求。
type qualityLocalTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t qualityLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	copy.URL, copy.Host = &u, t.target.Host
	return t.base.RoundTrip(copy)
}

func TestQualityShortRequestsUseThreeRealSDKProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			captured := make(chan []byte, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				raw, _ := io.ReadAll(req.Body)
				captured <- raw
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "responses":
					_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"{}","annotations":[]}]}]}`)
				case "anthropic":
					_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				default:
					_, _ = io.WriteString(w, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]}`)
				}
			}))
			defer server.Close()
			var model provider.LLMProvider
			if protocol == "anthropic" {
				target, _ := url.Parse(server.URL)
				beforeTransport := http.DefaultTransport
				http.DefaultTransport = qualityLocalTransport{target, beforeTransport}
				defer func() { http.DefaultTransport = beforeTransport }()
				t.Setenv("ZHIPU_API_KEY", "offline-quality-test")
				model = provider.NewZhipuClaudeProvider("claude-quality-fixture")
			} else {
				t.Setenv("OPENAI_API_KEY", "offline-quality-test")
				t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
				name := "fixture-quality-chat"
				if protocol == "responses" {
					name = "gpt-6.1-sol"
				}
				model = provider.NewOpenAIProvider(name)
			}
			ctx, r, history := memoryV2Fixture(t)
			r.state.Version, r.state.Spec.Quality = 2, &QualitySpec{Version: 1}
			r.state.Quality = &QualityState{Version: 1, DraftID: newID(), Status: "checked", ReviewStatus: "not_run", RevisionStatus: "not_run", CheckResult: "pass"}
			for i := range history {
				if len(history[i].ToolCalls) == 0 {
					continue
				}
				call := history[i].ToolCalls[0]
				arguments, _ := json.Marshal(string(call.Arguments))
				history[i].ResponseItems = []json.RawMessage{
					json.RawMessage(`{"type":"reasoning","id":"rs1","summary":[],"encrypted_content":"opaque-quality-history"}`),
					json.RawMessage(`{"type":"future_kind","opaque":{"keep":true}}`),
					json.RawMessage(`{"type":"function_call","id":"fc` + call.ID + `","call_id":"` + call.ID + `","name":"read_file","arguments":` + string(arguments) + `,"status":"completed"}`),
				}
			}
			original, _ := json.Marshal(history)
			eng := NewAgentEngine(model, nil, EngineOptions{})
			if _, err := eng.generate(ctx, r, history, nil); err != nil {
				t.Fatal(err)
			}
			main := <-captured
			if !bytes.Contains(main, []byte("E1.md")) {
				t.Fatal("主请求缺少完整历史，不能构成对照")
			}
			packet := []byte(`{"draft_id":"short-ticket","claim":"只复核这一个命题"}`)
			if _, err := eng.qualityModel(ctx, r, "quality_review", "短复核系统说明", packet, nil); err != nil {
				t.Fatal(err)
			}
			review := <-captured
			if _, err := eng.qualityModel(ctx, r, "quality_revision", "短修订系统说明", packet, []schema.ToolDefinition{qualityPublicationDefinition()}); err != nil {
				t.Fatal(err)
			}
			revision := <-captured
			for _, raw := range [][]byte{review, revision} {
				for _, forbidden := range []string{"E1.md", "E2.md", "材料原文", "opaque-quality-history", "future_kind", "读取处理进度"} {
					if bytes.Contains(raw, []byte(forbidden)) {
						t.Fatalf("短请求重新注入普通历史 %q", forbidden)
					}
				}
				if !bytes.Contains(raw, []byte("short-ticket")) || !bytes.Contains(raw, []byte("model_calls_remaining")) {
					t.Fatalf("短包或共享预算未进入实际SDK请求: %s", raw)
				}
				assertQualityFirstUser(t, raw, protocol)
			}
			var body map[string]json.RawMessage
			_ = json.Unmarshal(review, &body)
			if tools := bytes.TrimSpace(body["tools"]); len(tools) != 0 && string(tools) != "[]" && string(tools) != "null" {
				t.Fatalf("复核请求仍带工具: %s", tools)
			}
			_ = json.Unmarshal(revision, &body)
			var defs []map[string]json.RawMessage
			if json.Unmarshal(body["tools"], &defs) != nil || len(defs) != 1 || !bytes.Contains(defs[0]["function"], []byte("write_report")) && !bytes.Contains(defs[0]["name"], []byte("write_report")) {
				t.Fatal("修订工具集合不止唯一提交入口")
			}
			var parameters map[string]json.RawMessage
			if protocol == "chat" {
				var function map[string]json.RawMessage
				_ = json.Unmarshal(defs[0]["function"], &function)
				_ = json.Unmarshal(function["parameters"], &parameters)
			} else if protocol == "responses" {
				_ = json.Unmarshal(defs[0]["parameters"], &parameters)
			} else {
				_ = json.Unmarshal(defs[0]["input_schema"], &parameters)
			}
			var required []string
			if json.Unmarshal(parameters["required"], &required) != nil || !containsString(required, "draft_id") || !containsString(required, "corrections") {
				t.Fatalf("实际SDK请求丢失必填提交身份：%s", revision)
			}
			// 专用请求不会消费、折叠或重写原会话中的调用与不透明项。
			after, _ := json.Marshal(history)
			if !bytes.Equal(original, after) {
				t.Fatal("短请求改写了原历史")
			}
			if _, err := eng.generate(ctx, r, history, nil); err != nil {
				t.Fatal(err)
			}
			mainAfter := <-captured
			if protocol == "responses" && (!bytes.Contains(mainAfter, []byte("opaque-quality-history")) || !bytes.Contains(mainAfter, []byte("future_kind"))) {
				t.Fatal("后续普通Responses历史丢失opaque/未知项")
			}
			assertQualityToolPairs(t, mainAfter, protocol)
			if r.state.Usage.ModelCalls != 4 || r.state.Usage.Turns != 2 {
				t.Fatalf("独立请求重复扣费或未共享轮次: %+v", r.state.Usage)
			}
			if _, err := eng.qualityModel(ctx, r, "quality_review", "不得重发", packet, nil); err == nil {
				t.Fatal("重复复核获准")
			}
			if len(captured) != 0 {
				t.Fatal("重复复核实际发出请求")
			}
		})
	}
}

func assertQualityFirstUser(t *testing.T, raw []byte, protocol string) {
	t.Helper()
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	key := "messages"
	if protocol == "responses" {
		key = "input"
	}
	var messages []struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(body[key], &messages) != nil {
		t.Fatalf("实际消息无效: %s", raw)
	}
	for _, msg := range messages {
		if msg.Role == "system" || msg.Role == "developer" {
			continue
		}
		if msg.Role != "user" {
			t.Fatalf("首条业务消息不是普通user: %s", raw)
		}
		return
	}
	t.Fatal("短请求丢失普通user消息")
}

func assertQualityToolPairs(t *testing.T, raw []byte, protocol string) {
	t.Helper()
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	key := "messages"
	if protocol == "responses" {
		key = "input"
	}
	var decoded any
	if json.Unmarshal(body[key], &decoded) != nil {
		t.Fatal("工具历史无效")
	}
	// 两个历史调用及两个对应结果必须全部保留。API特定配对字段已由本地SDK编码。
	encoded, _ := json.Marshal(decoded)
	for _, id := range []string{"callE1", "callE2"} {
		if bytes.Count(encoded, []byte(`"`+id+`"`)) != 2 {
			t.Fatalf("%s的工具配对损坏: %s", id, encoded)
		}
	}
}

func TestQualityShortRequestHardLimitStopsBeforeAdmission(t *testing.T) {
	ctx, r, _ := memoryV2Fixture(t)
	r.state.Spec.Quality = &QualitySpec{Version: 1}
	r.state.Quality = &QualityState{Version: 1, DraftID: newID(), ReviewStatus: "not_run", RevisionStatus: "not_run"}
	model := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Fatal("超硬上限仍调用模型")
		return nil, nil
	})
	eng := NewAgentEngine(model, nil, EngineOptions{MaxContextBytes: 1000})
	_, err := eng.qualityModel(ctx, r, "quality_review", "必须完整保留", []byte(strings.Repeat("重要命题", 1000)), nil)
	if !errors.Is(err, errContextBudget) || r.state.Usage.ModelCalls != 0 || r.state.Usage.Turns != 0 || r.state.Quality.ReviewStatus != "not_run" {
		t.Fatalf("超限后发生准入或裁掉关键内容: %+v %v", r.state, err)
	}
}
