package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCLIIntegration 通过真实 CLI 进程覆盖入口装配和模型协议，测试目录没有 .env。
func TestCLIIntegration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "claw")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("构建测试 CLI 失败: %v\n%s", err, output)
	}
	for _, scenario := range []string{"工具回传", "预算拒绝", "REPL 独立运行", "权限拒绝", "无审批拒绝"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("私密测试输出"), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				number := len(requests)
				mu.Unlock()
				message := map[string]any{"role": "assistant", "content": "完成"}
				finish := "stop"
				if (scenario == "工具回传" || scenario == "权限拒绝" || scenario == "无审批拒绝") && number == 1 {
					message["content"] = ""
					message["tool_calls"] = []any{map[string]any{"id": "read-1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"sample.txt"}`}}}
					finish = "tool_calls"
					if scenario != "工具回传" {
						message["tool_calls"] = []any{map[string]any{"id": "write-1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"forbidden","content":"不应写入"}`}}}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("test-%d", number), "object": "chat.completion", "created": 1, "model": "mock",
					"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
					"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
			}))
			defer server.Close()
			args := []string{"-dir", dir, "-model", "mock", "-base-url", server.URL, "-max-output-tokens", "32", "-max-total-tokens", "100000"}
			if scenario == "权限拒绝" {
				args = append(args, "-permissions", "read-only")
			}
			if scenario == "无审批拒绝" {
				args = append(args, "-approval", "deny")
			}
			input := ""
			if scenario == "REPL 独立运行" {
				input = "/status\n第一个任务\n第二个任务\n/cost\n/exit\n"
			} else {
				args = append(args, "-prompt", "读取 sample.txt")
			}
			if scenario == "预算拒绝" {
				args = append(args, "-max-total-tokens", "1")
			}
			runCtx, runCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer runCancel()
			cmd := exec.CommandContext(runCtx, binary, args...)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "OPENAI_API_KEY=local-test-key"}
			cmd.Stdin = strings.NewReader(input)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			if scenario == "预算拒绝" {
				if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 1 || !strings.Contains(out.String(), "token_budget") {
					t.Fatalf("预算退出不正确: %v\n%s", err, out.String())
				}
			} else if err != nil {
				t.Fatalf("CLI 运行失败: %v\n%s", err, out.String())
			}
			mu.Lock()
			defer mu.Unlock()
			wantRequests, wantTraces := 2, 1
			if scenario == "预算拒绝" {
				wantRequests = 0
			}
			if scenario == "REPL 独立运行" {
				wantTraces = 2
				ids := regexp.MustCompile(`RunID: ([0-9a-f]{32})`).FindAllStringSubmatch(out.String(), -1)
				if len(ids) != 2 || ids[0][1] == ids[1][1] || !strings.Contains(out.String(), "计划模式: false") || !strings.Contains(out.String(), "2 笔费用未知") {
					t.Fatalf("REPL 状态不正确:\n%s", out.String())
				}
			}
			if len(requests) != wantRequests {
				t.Fatalf("模型请求数量: %d，期望 %d", len(requests), wantRequests)
			}
			for _, request := range requests {
				if request["max_completion_tokens"] != float64(32) {
					t.Fatalf("输出上限未到达伪网关: %+v", request)
				}
			}
			if scenario == "工具回传" {
				data, _ := json.Marshal(requests[1]["messages"])
				if !bytes.Contains(data, []byte("私密测试输出")) || !bytes.Contains(data, []byte(`"tool_call_id":"read-1"`)) {
					t.Fatalf("工具结果未回传: %s", data)
				}
			}
			if scenario == "权限拒绝" || scenario == "无审批拒绝" {
				data, _ := json.Marshal(requests[1]["messages"])
				expected := "工具权限不足"
				if scenario == "无审批拒绝" {
					expected = "没有可信审批来源"
				}
				if !bytes.Contains(data, []byte(expected)) || !bytes.Contains(data, []byte(`"tool_call_id":"write-1"`)) {
					t.Fatalf("拒绝没有回传: %s", data)
				}
				if _, err := os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
					t.Fatal("拒绝后仍写入")
				}
			}
			traces, err := filepath.Glob(filepath.Join(dir, ".claw", "traces", "trace_*.json"))
			if err != nil || len(traces) != wantTraces {
				t.Fatalf("Trace 数量错误: %v %v", traces, err)
			}
			for _, path := range traces {
				data, err := os.ReadFile(path)
				if err != nil || bytes.Contains(data, []byte("私密测试输出")) || bytes.Contains(data, []byte("sample.txt")) || bytes.Contains(data, []byte("local-test-key")) {
					t.Fatalf("Trace 包含原始内容或不可读: %s %v", path, err)
				}
			}
		})
	}
}
