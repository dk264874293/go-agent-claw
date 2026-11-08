package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

// TestPerformanceAcceptance 测量完整认证 HTTP 路径，包含输出脱敏和编码。
// 显式开启以避免日常单测生成十万条事件；不调用模型或外部验收命令。
func TestPerformanceAcceptance(t *testing.T) {
	if os.Getenv("CLAW_MONITOR_PERF_ACCEPTANCE") == "" {
		t.Skip("设置 CLAW_MONITOR_PERF_ACCEPTANCE=1 运行完整 HTTP 性能验收")
	}
	t.Logf("机器: Go=%s OS=%s ARCH=%s CPU=%d GOMAXPROCS=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	work := t.TempDir()
	const tasks, eventsPerTask = 1000, 100
	var totalBytes int64
	for n := 1; n <= tasks; n++ {
		totalBytes += writePerformanceTask(t, work, n, "性能验收历史任务", eventsPerTask)
	}
	if totalBytes > 256<<20 {
		t.Fatalf("合成数据超过 256 MiB: %d", totalBytes)
	}
	t.Logf("数据集: 任务=%d 事件=%d 字节=%d MiB=%.2f", tasks, tasks*eventsPerTask, totalBytes, float64(totalBytes)/(1<<20))
	started := time.Now()
	index, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	t.Logf("初始索引耗时: %s", time.Since(started))
	server := NewServer(index, "")
	httpServer := httptest.NewUnstartedServer(server)
	server.host = httpServer.Listener.Addr().String()
	httpServer.Start()
	defer httpServer.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	cookie := performanceSession(t, client, httpServer.URL, server.token)
	getPage := func(t *testing.T, query string) (int, int, time.Duration) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/tasks?page=1&page_size=50"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		elapsed := time.Since(start)
		if readErr != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("列表读取失败: status=%d read=%v", resp.StatusCode, readErr)
		}
		var page struct {
			Items []json.RawMessage `json:"items"`
			Total int               `json:"total"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return len(page.Items), page.Total, elapsed
	}

	t.Run("AuthenticatedFirstPage", func(t *testing.T) {
		latencies := make([]time.Duration, 0, 100)
		for n := 0; n < 100; n++ {
			count, total, elapsed := getPage(t, "")
			if count != 50 || total != tasks {
				t.Fatalf("分页不完整: count=%d total=%d", count, total)
			}
			latencies = append(latencies, elapsed)
		}
		p95 := performanceP95(latencies)
		t.Logf("认证 GET /api/v1/tasks 第一页: 样本=%d P95=%s 最大=%s", len(latencies), p95, latencies[len(latencies)-1])
		if p95 > 200*time.Millisecond {
			t.Fatalf("列表 P95 超过 200 ms: %s", p95)
		}
	})

	for _, query := range []struct {
		name string
		path string
	}{{"Overview", "/api/v1/overview"}, {"SummaryFirstPage", "/api/v1/tasks?view=summary&page_size=50"}, {"AlertsFirstPage", "/api/v1/alerts?page_size=50"}} {
		t.Run(query.name, func(t *testing.T) {
			latencies := make([]time.Duration, 0, 100)
			for n := 0; n < 100; n++ {
				req, err := http.NewRequest(http.MethodGet, httpServer.URL+query.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.AddCookie(cookie)
				start := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				latencies = append(latencies, time.Since(start))
				if readErr != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("查询失败: status=%d read=%v", resp.StatusCode, readErr)
				}
				var result struct {
					Total     int               `json:"total"`
					Items     []json.RawMessage `json:"items"`
					Recent    []json.RawMessage `json:"recent"`
					Attention []json.RawMessage `json:"attention"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				if result.Total != tasks || query.name == "Overview" && (len(result.Recent) != 6 || len(result.Attention) != 6) || query.name != "Overview" && len(result.Items) != 50 {
					t.Fatalf("概览或分页遗漏任务: total=%d items=%d recent=%d attention=%d", result.Total, len(result.Items), len(result.Recent), len(result.Attention))
				}
			}
			p95 := performanceP95(latencies)
			t.Logf("认证 GET %s: 样本=%d P95=%s 最大=%s", query.path, len(latencies), p95, latencies[len(latencies)-1])
			if p95 > 200*time.Millisecond {
				t.Fatalf("查询 P95 超过 200 ms: %s", p95)
			}
		})
	}

	t.Run("RecordVisibility", func(t *testing.T) {
		// 与正式服务一致：每秒 Refresh，视图变化后 Notify；轮询走真实认证接口。
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if index.Refresh() {
						server.Notify()
					}
				case <-stop:
					return
				}
			}
		}()
		defer func() { close(stop); <-stopped }()
		latencies := make([]time.Duration, 0, 10)
		for n := 1; n <= 10; n++ {
			goal := fmt.Sprintf("新增记录可见性样本%02d", n)
			writePerformanceTask(t, work, tasks+n, goal, 1)
			start := time.Now()
			for {
				count, total, _ := getPage(t, "&q="+url.QueryEscape(goal))
				if count == 1 && total == 1 {
					latencies = append(latencies, time.Since(start))
					break
				}
				if time.Since(start) > 3*time.Second {
					t.Fatalf("新增记录超过 3 秒仍不可见: 样本=%d count=%d total=%d", n, count, total)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		p95 := performanceP95(latencies)
		t.Logf("已有 1000 任务 / 10 万事件时新增记录至 HTTP 可见: 样本=%d Refresh=1s 轮询=20ms P95=%s 最大=%s", len(latencies), p95, latencies[len(latencies)-1])
		if p95 > 2*time.Second {
			t.Fatalf("记录可见延迟 P95 超过 2 秒: %s", p95)
		}
	})
}

func writePerformanceTask(t *testing.T, work string, number int, goal string, events int) int64 {
	t.Helper()
	id := fmt.Sprintf("%032x", number)
	dir := filepath.Join(work, ".claw", "tasks", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"version":       1,
		"spec":          map[string]any{"id": id, "goal": goal, "kind": "change", "mode": "workspace"},
		"limits":        map[string]any{"MaxModelCalls": 8, "MaxToolCalls": 20},
		"usage":         map[string]any{"model_calls": 8, "tool_calls": 3},
		"effect_ledger": map[string]any{"history_complete": true, "effects": []any{map[string]any{"action_id": "action", "tool": "bash", "status": "SUCCEEDED", "effect": "UNKNOWN"}}},
		"last_result":   map[string]any{"task_id": id, "run_id": "historical-run", "status": "BUDGET_EXHAUSTED", "stop_code": "model_limit"},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for seq := 1; seq <= events; seq++ {
		if err := enc.Encode(taskstore.Event{Version: 1, Sequence: uint64(seq), Kind: "finished", Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	// 原子发布完整日志，测量从记录发布后开始，避免夹具写入速度影响结果。
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path+".tmp", data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	return int64(data.Len())
}

func performanceSession(t *testing.T, client *http.Client, address, token string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, address+"/api/v1/session", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", address)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 {
		t.Fatalf("会话兑换失败: %d", resp.StatusCode)
	}
	return resp.Cookies()[0]
}

func performanceP95(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[int(math.Ceil(float64(len(samples))*0.95))-1]
}
