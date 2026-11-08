package monitor

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func TestRequestDiagnosticsProjectionUsesWhitelistAndKeepsPresence(t *testing.T) {
	x, _, server, cookie := authenticated(t)
	work, root := x.config.Workspaces[0], x.config.ObserveDir
	tid, aid := strings.Repeat("a", 32), strings.Repeat("b", 32)
	secret := "monitor-request-private-key-123456789"
	t.Setenv("MONITOR_REQUEST_API_KEY", secret)
	diagnosticID, manifestID := strings.Repeat("c", 32), strings.Repeat("d", 32)
	regressionObservation(t, root, aid, observe.WorkspaceID(work), tid, time.Now(),
		map[string]any{"event_type": "run_attempt_started"},
		map[string]any{"event_type": "request_diagnostic", "error_class": "model_http_error", "request": map[string]any{
			"provider": "deepseek", "protocol": "chat_completions", "endpoint_id": "endpoint-local", "model": "fixture-model",
			"parameters": map[string]any{
				"temperature": map[string]any{"state": "value", "value": 0},
				"stream":      map[string]any{"state": "value", "value": false},
				"top_p":       map[string]any{"state": "absent"},
				"max_tokens":  map[string]any{"state": "null", "value": nil},
				"tool_choice": map[string]any{"state": "value", "value": map[string]any{"body": "不能展示的请求正文"}},
				"messages":    map[string]any{"state": "value", "value": "不能展示的请求正文"},
			},
			"role_counts": map[string]any{"user": 1, "arbitrary-content": 500}, "tool_names": []string{"read_file", secret},
			"http_status": 400, "service_request_id": "safe-request-id", "manifest_id": manifestID, "diagnostic_id": diagnosticID,
			"capture_mode": "on_error", "capture_status": "dropped", "capture_reason": "size_limit", "capture_complete": false,
			"capture_redacted": true, "previous_available": false,
			"messages": []string{"不能展示的请求正文"}, "body": "不能展示的请求正文", "headers": map[string]any{"Authorization": secret},
			"error": map[string]any{"code": map[string]any{"state": "absent"}, "type": map[string]any{"state": "value", "value": "invalid_request_error"}, "param": map[string]any{"state": "null"}, "request_id": map[string]any{"state": "value", "value": "safe-request-id"}, "summary": "错误摘要 " + secret},
		}},
		map[string]any{"event_type": "run_returned", "status": "FAILED"})
	path := filepath.Join(root, aid, "events.jsonl")
	before, _ := os.ReadFile(path)
	x.Refresh()
	attempt, ok := x.Attempt(aid)
	if !ok || len(attempt.Events) != 3 {
		t.Fatalf("请求观测未导入: %+v", attempt)
	}
	request := attempt.Events[1].Payload["request"].(map[string]any)
	parameters := request["parameters"].(map[string]any)
	if parameters["temperature"].(map[string]any)["value"] != float64(0) || parameters["stream"].(map[string]any)["value"] != false || parameters["top_p"].(map[string]any)["state"] != "absent" || parameters["max_tokens"].(map[string]any)["state"] != "null" || parameters["tool_choice"].(map[string]any)["state"] != "unknown" {
		t.Fatalf("零值、空值、未发送或无效对象混淆: %+v", parameters)
	}
	if request["capture_complete"] != false || request["capture_redacted"] != true || request["previous_available"] != false {
		t.Fatalf("明确false被补造为未知: %+v", request)
	}
	alerts := x.Tasks("", "", "")[0].Alerts
	diagnosticAlert := false
	for _, alert := range alerts {
		if alert.Code == "request_diagnostic_unavailable" && alert.AttemptID == aid && alert.Sequence == 2 {
			diagnosticAlert = true
		}
	}
	if !diagnosticAlert {
		t.Fatal("诊断丢失未关联到独立提示和原事件")
	}
	raw, _ := json.Marshal(request)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "不能展示的请求正文") || strings.Contains(string(raw), "arbitrary-content") {
		t.Fatalf("请求白名单或脱敏失效: %s", raw)
	}
	get := func(path string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		req.Header.Set("Cookie", cookie)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response, body
	}
	response, body := get("/api/v1/attempts/" + aid + "/events")
	if response.StatusCode != 200 || strings.Contains(string(body), secret) || !strings.Contains(string(body), "safe-request-id") {
		t.Fatalf("事件API输出错误: %d %s", response.StatusCode, body)
	}
	for _, endpoint := range []string{"/api/v1/evidence/" + diagnosticID, "/api/v1/evidence/" + manifestID, "/api/v1/diagnostics/" + diagnosticID} {
		response, _ := get(endpoint)
		if response.StatusCode != 404 {
			t.Fatalf("诊断身份被暴露为普通证据或正文接口: %s %d", endpoint, response.StatusCode)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("读取请求诊断修改了源文件")
	}
}

func TestLegacyRequestParametersRemainUnknown(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	tid, aid := strings.Repeat("e", 32), strings.Repeat("f", 32)
	regressionObservation(t, root, aid, observe.WorkspaceID(work), tid, time.Now(), map[string]any{"event_type": "model_finished", "status": "failed"})
	x, err := NewIndex(Config{Workspaces: []string{work}, ObserveDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	attempt, _ := x.Attempt(aid)
	if _, exists := attempt.Events[0].Payload["request"]; exists {
		t.Fatal("为旧记录补造了请求参数")
	}
}
