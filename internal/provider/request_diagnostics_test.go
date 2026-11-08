package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestProviderDiagnosticsKeepThreeProtocolWireBytesAndSourceMappings(t *testing.T) {
	for _, tc := range []struct {
		name, model, provider, protocol, response string
		create                                    func(string, string, string) LLMProvider
	}{
		{"chat", "wire-chat", "deepseek", "chat_completions", `{"choices":[{"message":{"role":"assistant","content":"完成"}}]}`, func(m, k, u string) LLMProvider { return newCompatibleProviderForChannel(m, k, u, "deepseek") }},
		{"responses", "gpt-6.1-sol", "openai", "responses", `{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}]}`, func(m, k, u string) LLMProvider { return newCompatibleProviderForChannel(m, k, u, "openai") }},
		{"anthropic", "claude-wire", "zhipu", "anthropic_messages", `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"完成"}],"stop_reason":"end_turn"}`, func(m, k, u string) LLMProvider { return newClaudeProviderForChannel(m, k, u, "zhipu") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const privateText = "request-private-business-92834"
			const privateKey = "request-private-credential-34234"
			t.Setenv("TEST_API_KEY", privateKey)
			received := make(chan []byte, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				received <- b
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			var baseline []byte
			for _, mode := range []string{requestdiag.Off, requestdiag.MetadataOnly, requestdiag.OnError} {
				dir := t.TempDir()
				var mu sync.Mutex
				var recorded []*requestdiag.Metadata
				ctx, a := requestdiag.Start(context.Background(), requestdiag.Config{Mode: mode, Dir: dir}, requestdiag.Identity{AgentID: "root"}, func(e requestdiag.Event) {
					if e.Metadata != nil {
						mu.Lock()
						recorded = append(recorded, e.Metadata)
						mu.Unlock()
					}
				})
				ctx, call := requestdiag.BeginCall(ctx, requestdiag.Identity{AgentID: "root", ModelCallID: "wire-call"}, []requestdiag.Origin{{InputIndex: 0, Representation: "system_generated", SourceKind: "system"}, {InputIndex: 1, Representation: "raw", SourceKind: "read", EvidenceID: "E1", Version: "version-fixture"}})
				p := tc.create(tc.model, privateKey, server.URL+"/")
				result, err := p.Generate(ctx, []schema.Message{{Role: schema.RoleSystem, Content: "规则"}, {Role: schema.RoleUser, Content: privateText}}, []schema.ToolDefinition{{Name: "read_file", Description: privateText, InputSchema: map[string]any{"type": "object"}}})
				call.Finish(requestdiag.Outcome{Success: err == nil && result != nil})
				a.Close()
				if err != nil {
					t.Fatal(err)
				}
				wire := <-received
				if baseline == nil {
					baseline = wire
				} else if !bytes.Equal(baseline, wire) {
					t.Fatalf("记录模式%s改变SDK发送字节", mode)
				}
				if mode == requestdiag.Off {
					if len(recorded) != 0 {
						t.Fatal("off仍生成诊断记录")
					}
					continue
				}
				if len(recorded) != 1 {
					t.Fatalf("HTTP元数据次数错误: %d", len(recorded))
				}
				m := recorded[0]
				if m.Provider != tc.provider || m.Protocol != tc.protocol || m.Model != tc.model || m.Parameters["temperature"].Presence != "absent" {
					t.Fatalf("实际渠道或参数状态错误: %+v", m)
				}
				if tc.protocol == "anthropic_messages" && string(m.Parameters["max_tokens"].Value) != "4096" {
					t.Fatalf("Claude显式上限丢失: %+v", m.Parameters)
				}
				if tc.protocol == "responses" && (string(m.Parameters["store"].Value) != "false" || m.Parameters["store"].Presence != "value") {
					t.Fatalf("false被当作缺省: %+v", m.Parameters)
				}
				found := false
				for _, item := range m.Items {
					if item.Origin != nil && item.Origin.EvidenceID == "E1" {
						found = true
						if item.Origin.Representation != "raw" {
							t.Fatal("来源表示错误")
						}
					}
				}
				if !found {
					t.Fatalf("最终协议路径未绑定证据身份: %+v", m.Items)
				}
				_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						data, readErr := os.ReadFile(path)
						if readErr != nil {
							t.Fatal(readErr)
						}
						if bytes.Contains(data, []byte(privateText)) || bytes.Contains(data, []byte(privateKey)) {
							t.Fatalf("成功请求正文或密钥进入落盘记录: %s", path)
						}
					}
					return err
				})
			}
		})
	}
}

type bodyReadProbe struct {
	reads int
	data  *strings.Reader
}

func (p *bodyReadProbe) Read(b []byte) (int, error) { p.reads++; return p.data.Read(b) }
func (p *bodyReadProbe) Close() error               { return nil }

func TestRequestMiddlewareDoesNotConsumeSuccessOrErrorResponseBodies(t *testing.T) {
	for _, status := range []int{200, 400, 502} {
		ctx, a := requestdiag.Start(context.Background(), requestdiag.Config{Mode: requestdiag.OnError, Dir: t.TempDir()}, requestdiag.Identity{}, nil)
		ctx, call := requestdiag.BeginCall(ctx, requestdiag.Identity{AgentID: "root", ModelCallID: "stream"}, nil)
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://unused.invalid/chat/completions", strings.NewReader(`{"model":"fixture","messages":[]}`))
		body := &bodyReadProbe{data: strings.NewReader("data: private-response-stream\n\n")}
		resp, err := requestEncodingObserver("fixture", "openai", "chat_completions")(req, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
		})
		if err != nil || resp.Body != body || body.reads != 0 {
			t.Fatalf("状态%d的响应流被采集消费: %d %v", status, body.reads, err)
		}
		call.Finish(requestdiag.Outcome{Success: status == 200})
		a.Close()
	}
}

func TestMalformedSuccessfulSDKResponseIsNotTransportFailure(t *testing.T) {
	for _, tc := range []struct {
		name, model, body string
		create            func(string, string, string) LLMProvider
	}{
		{"chat", "fixture", `{"choices":broken`, func(m, k, u string) LLMProvider { return newCompatibleProvider(m, k, u) }},
		{"responses", "gpt-6.1-sol", `{"output":broken`, func(m, k, u string) LLMProvider { return newCompatibleProvider(m, k, u) }},
		{"anthropic", "claude-fixture", `{"content":broken`, func(m, k, u string) LLMProvider { return newClaudeProvider(m, k, u) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := tc.create(tc.model, "fixture", server.URL).Generate(context.Background(), nil, nil)
			if FailureKind(err) != "model_response_invalid" {
				t.Fatalf("HTTP2xx解析错误被误归网络: %v", err)
			}
		})
	}
}

func TestActualSerializedParametersKeepZeroFalseNullAndAbsence(t *testing.T) {
	var recorded *requestdiag.Metadata
	ctx, attempt := requestdiag.Start(context.Background(), requestdiag.Config{Mode: requestdiag.MetadataOnly, Dir: t.TempDir()}, requestdiag.Identity{}, func(e requestdiag.Event) {
		if e.Metadata != nil {
			recorded = e.Metadata
		}
	})
	ctx, call := requestdiag.BeginCall(ctx, requestdiag.Identity{AgentID: "root", ModelCallID: "parameter-wire"}, nil)
	const payload = `{"model":"fixture","messages":[],"temperature":0,"parallel_tool_calls":false,"top_p":null}`
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://unused.invalid", strings.NewReader(payload))
	_, err := requestEncodingObserver("fixture", "openai", "chat_completions")(req, func(sent *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(sent.Body)
		if readErr != nil || string(body) != payload {
			t.Fatalf("记录改变实际发送参数: %q %v", body, readErr)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	})
	call.Finish(requestdiag.Outcome{Success: err == nil})
	attempt.Close()
	if recorded == nil {
		t.Fatal("缺少实际编码元数据")
	}
	for name, want := range map[string]struct{ presence, value string }{"temperature": {"value", "0"}, "parallel_tool_calls": {"value", "false"}, "top_p": {"null", ""}, "max_tokens": {"absent", ""}} {
		got := recorded.Parameters[name]
		if got.Presence != want.presence || string(got.Value) != want.value {
			t.Fatalf("%s实际编码状态丢失: %+v", name, got)
		}
	}
}

func TestEmptyProtocolResponsesKeepUsageWithoutReplacingPreviousSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		responses   []string
		create      func(string, string, string) LLMProvider
	}{
		{"chat", "fixture", []string{
			`{"choices":[{"message":{"content":"有效答复"}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			`{"choices":[{"message":{"content":"  ","reasoning_content":"private-thought"}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			`{"choices":[{"message":{"tool_calls":[{"type":"function","id":"read-1","function":{"name":"read_file","arguments":"{}"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
		}, func(m, k, u string) LLMProvider { return newCompatibleProvider(m, k, u) }},
		{"responses", "gpt-6.1-sol", []string{
			`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"有效答复"}]}],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[{"text":"private-thought"}]}],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"status":"completed","output":[{"type":"function_call","id":"tool-1","call_id":"read-1","name":"read_file","arguments":"{}"}],"usage":{"input_tokens":7,"output_tokens":2}}`,
		}, func(m, k, u string) LLMProvider { return newCompatibleProvider(m, k, u) }},
		{"anthropic", "claude-fixture", []string{
			`{"type":"message","content":[{"type":"text","text":"有效答复"}],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"type":"message","content":[],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"type":"message","content":[{"type":"thinking","thinking":"private-thought"}],"usage":{"input_tokens":7,"output_tokens":2}}`,
			`{"type":"message","content":[{"type":"tool_use","id":"read-1","name":"read_file","input":{}}],"usage":{"input_tokens":7,"output_tokens":2}}`,
		}, func(m, k, u string) LLMProvider { return newClaudeProvider(m, k, u) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(requests.Add(1)) - 1
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.responses[i])
			}))
			defer server.Close()
			var mu sync.Mutex
			byID := map[string]*requestdiag.Metadata{}
			ctx, attempt := requestdiag.Start(context.Background(), requestdiag.Config{Mode: requestdiag.OnError, Dir: t.TempDir()}, requestdiag.Identity{}, func(e requestdiag.Event) {
				if e.Metadata != nil {
					mu.Lock()
					byID[e.Identity.ModelCallID] = e.Metadata
					mu.Unlock()
				}
			})
			p := tc.create(tc.model, "fixture", server.URL)
			for i, callID := range []string{"valid", "empty", "private_only", "tool"} {
				callCtx, call := requestdiag.BeginCall(ctx, requestdiag.Identity{AgentID: "root", ModelCallID: callID}, nil)
				msg, err := p.Generate(callCtx, []schema.Message{{Role: schema.RoleUser, Content: "读取"}}, nil)
				call.Finish(requestdiag.Outcome{Success: err == nil && msg != nil, ErrorClass: FailureKind(err)})
				if msg == nil || msg.Usage == nil || *msg.Usage != (schema.Usage{PromptTokens: 7, CompletionTokens: 2}) {
					t.Fatalf("第%d轮已知用量丢失: %+v %v", i, msg, err)
				}
				if i == 1 || i == 2 {
					if FailureKind(err) != "model_response_invalid" {
						t.Fatalf("空/私密推理回应被当作成功: %+v %v", msg, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if i == 3 && len(msg.ToolCalls) != 1 {
					t.Fatal("有效工具调用被空内容校验拒绝")
				}
			}
			attempt.Close()
			if len(byID) != 4 || byID["tool"].PreviousSuccessfulCallID != "valid" || byID["empty"].Error.Kind != "model_response_invalid" || byID["private_only"].Error.Kind != "model_response_invalid" {
				t.Fatalf("无效响应污染成功缓存或错误事实: %+v", byID)
			}
		})
	}
}
