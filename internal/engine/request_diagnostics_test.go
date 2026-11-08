package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestContentRejectionAfterWriteKeepsEffectsAndCapturesLocalPair(t *testing.T) {
	fixtureBytes, err := os.ReadFile("../../eval/fixtures/request_diagnostics/b1de-content-rejection.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SyntheticEnvelopes []json.RawMessage `json:"synthetic_envelopes"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil || len(fixture.SyntheticEnvelopes) != 2 {
		t.Fatalf("脱敏症状夹具无效: %v", err)
	}
	for _, tc := range []struct {
		name, mode string
		envelope   int
		brokenDir  bool
	}{
		{"metadata", requestdiag.MetadataOnly, 0, false},
		{"on_error", requestdiag.OnError, 1, false},
		{"storage_failure", requestdiag.OnError, 0, true},
		{"off", requestdiag.Off, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			dir := t.TempDir()
			diagDir := t.TempDir()
			obsDir := t.TempDir()
			if tc.brokenDir {
				diagDir = filepath.Join(diagDir, "not-a-directory")
				if err := os.WriteFile(diagDir, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "fixture-request-id")
				if calls.Add(1) == 1 {
					args, _ := json.Marshal(map[string]string{"path": "out.md", "content": "已保存内容", "expected_version": "missing"})
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "model": "deepseek-fixture", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "write-call", "type": "function", "function": map[string]any{"name": "write_file", "arguments": string(args)}}}}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
					return
				}
				w.WriteHeader(400)
				_, _ = w.Write(fixture.SyntheticEnvelopes[tc.envelope])
			}))
			defer server.Close()
			t.Setenv("DEEPSEEK_API_KEY", "sk-unit-not-a-real-secret")
			t.Setenv("DEEPSEEK_BASE_URL", server.URL)
			p := provider.NewDeepseekOpenAIProvider("deepseek-fixture")
			eng := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 2, MaxToolCalls: 5}})
			ctx, obs := observe.Start(context.Background(), observe.Options{WorkDir: dir}, observe.Config{Dir: obsDir})
			ctx, diag := StartRequestDiagnostics(ctx, requestdiag.Config{Mode: mode, Dir: diagDir})
			res, err := eng.Run(ctx, ctxpkg.NewSession("拒绝后文件交付", dir), TaskSpec{Goal: "创建文件", Mode: tools.ModeWorkspace}, nil)
			obs.Returned(ObservationResult(res, err))
			CloseRequestDiagnostics(diag, obs)
			var failure *provider.RequestFailure
			if !errors.As(err, &failure) || res.Status != Failed || res.StopCode != "model_content_rejected" || res.ExitCode() != 1 || res.FinalText != "" || calls.Load() != 2 || res.Usage.ModelCalls != 2 || res.Usage.ToolCalls != 1 || len(res.Artifacts) != 1 {
				t.Fatalf("拒绝分类/效果/调用数错误: %+v err=%v calls=%d", res, err, calls.Load())
			}
			if b, err := os.ReadFile(filepath.Join(dir, "out.md")); err != nil || string(b) != "已保存内容" {
				t.Fatalf("产物丢失: %q %v", b, err)
			}
			if payload := ObservationResult(res, err); payload.ErrorClass != "model_content_rejected" {
				t.Fatalf("观测类别错误: %+v", payload)
			}
			if !strings.Contains(FormatRunResult(res), "已有确认保存的产物") {
				t.Fatal("终报未交付可信产物说明")
			}
			var data strings.Builder
			_ = filepath.WalkDir(diagDir, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					b, _ := os.ReadFile(path)
					data.Write(b)
				}
				return nil
			})
			if strings.Contains(data.String(), "sk-unit-not-a-real-secret") {
				t.Fatal("密钥进入诊断")
			}
			if mode != requestdiag.Off && !tc.brokenDir && data.Len() == 0 {
				t.Fatal("没有本地诊断元数据")
			}
			if mode == requestdiag.Off && data.Len() != 0 {
				t.Fatal("关闭诊断仍新增文件")
			}
			if tc.brokenDir {
				events, readErr := os.ReadFile(filepath.Join(obsDir, observe.Identity(ctx).AttemptID, "events.jsonl"))
				if readErr != nil || !strings.Contains(string(events), "storage_unavailable") {
					t.Fatalf("诊断存储失败未被标明: %v", readErr)
				}
			}
			if mode == requestdiag.MetadataOnly && strings.Contains(data.String(), "已保存内容") {
				t.Fatal("metadata模式保存了正文")
			}
		})
	}
}

func TestDiagnosticOriginsRespectFinalProjection(t *testing.T) {
	r := &runState{}
	r.analysisRuntime.sources = map[string]AnalysisEvidence{"E1": {ActionID: "read-action", Receipt: schema.ReadReceipt{Version: "v1", ByteStart: 0, ByteEnd: 100}}}
	items := r.requestOrigins([]schema.Message{{Role: schema.RoleUser, ReadEvidenceID: "E1", RequestRepresentation: "folded_marker"}, {Role: schema.RoleUser, ReadEvidenceID: "E1"}, {Role: schema.RoleSystem, Content: "system"}})
	if items[0].Representation != "folded_marker" || items[1].Representation != "source" || items[2].Representation != "system_generated" || items[0].ActionID != "read-action" {
		t.Fatalf("折叠被冒充原文: %+v", items)
	}
}

func TestDiagnosticBridgeKeepsScalarFactsWithoutRequestContent(t *testing.T) {
	obsDir := t.TempDir()
	ctx, obs := observe.Start(context.Background(), observe.Options{WorkDir: t.TempDir()}, observe.Config{Dir: obsDir})
	ctx, diag := StartRequestDiagnostics(ctx, requestdiag.Config{Mode: requestdiag.OnError, Dir: t.TempDir()})
	ctx, call := requestdiag.BeginCall(ctx, requestdiag.Identity{ModelCallID: "local-scalar", AgentID: "root"}, nil)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unused.invalid/v1/messages", strings.NewReader(`{"model":"fixture","temperature":0,"stream":false,"top_p":null,"reasoning_effort":null,"text":{"format":{"type":"json_object"}},"tool_choice":{"type":"tool","name":"read_file"},"messages":[{"role":"user","content":"业务正文不可进入观测流"}],"tools":[{"name":"read_file","description":"Schema描述不可进入观测流"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	h := requestdiag.BeginHTTP(ctx, req, "fixture", "anthropic_messages")
	h.Finish(&http.Response{StatusCode: 400}, nil)
	call.Finish(requestdiag.Outcome{ErrorClass: "model_bad_request"})
	CloseRequestDiagnostics(diag, obs)
	b, err := os.ReadFile(filepath.Join(obsDir, observe.Identity(ctx).AttemptID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "业务正文") || strings.Contains(string(b), "Schema描述") {
		t.Fatal("正文进入常驻观测流")
	}
	var summary *observe.RequestSummary
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e observe.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.EventType == "request_metadata" {
			summary = e.Payload.Request
		}
	}
	if summary == nil {
		t.Fatal("缺少诊断摘要")
	}
	for key, want := range map[string]string{"temperature": "0", "stream": "false", "tool_choice_mode": `"tool"`, "tool_choice_name": `"read_file"`, "response_format_type": `"json_object"`} {
		if p := summary.Parameters[key]; p.State != "value" || string(p.Value) != want {
			t.Fatalf("实际参数%s被丢失: %+v", key, p)
		}
	}
	if summary.Parameters["reasoning_effort"].State != "null" || summary.Parameters["top_p"].State != "null" || summary.Parameters["max_tokens"].State != "absent" || summary.InputCount == nil || *summary.InputCount != 1 {
		t.Fatalf("参数存在性或输入数错误: %+v", summary)
	}
}

// 新增Provider类别不能覆盖既有的持久化、取消和任务预算裁决。
func TestRequestFailureKeepsStopPrecedence(t *testing.T) {
	failure := &provider.RequestFailure{Kind: "model_content_rejected", SafeSummary: "服务拒绝"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		status TaskStatus
		code   string
	}{
		{"typed", context.Background(), failure, Failed, "model_content_rejected"},
		{"storage", context.Background(), fmt.Errorf("%w: %w", errStorage, failure), Failed, "storage_error"},
		{"cancel", cancelled, failure, Cancelled, "cancelled"},
		{"budget", context.Background(), fmt.Errorf("%w: %w", errBudget, failure), BudgetExhausted, "budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := (RunResult{FinalText: "旧答复"}).stop(tc.ctx, tc.err)
			if result.Status != tc.status || result.StopCode != tc.code || result.FinalText != "" {
				t.Fatalf("停止优先级错误: %+v", result)
			}
		})
	}
}
