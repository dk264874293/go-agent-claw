package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticNavigationAllowsExternalLauncher(t *testing.T) {
	_, _, hs, cookie := authenticated(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("启动链接被拒绝 %s: %s", path, res.Status)
		}
	}
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/tasks", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatal("跨站 API 不应放行")
	}
}

func TestSSEAtLatestCursorConnectsImmediately(t *testing.T) {
	_, s, hs, cookie := authenticated(t)
	s.Notify()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", hs.URL+"/api/v1/stream", nil)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Last-Event-ID", s.instance+":1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("最新游标的SSE连接被无谓延迟: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
}

func TestJSONResponseRedactsNestedValuesAndPreservesNumbers(t *testing.T) {
	secret := "private-evidence-value-0123456789"
	t.Setenv("MONITOR_TEST_API_KEY", secret)
	w := httptest.NewRecorder()
	jsonOut(w, map[string]any{"sequence": uint64(1<<60 + 1), "effects": []any{map[string]any{"file": map[string]any{"path": "a/" + secret + "/b"}}}, "labels": []string{secret}})
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("嵌套历史字段泄漏凭据")
	}
	if !strings.Contains(w.Body.String(), `"sequence":1152921504606846977`) {
		t.Fatal("序号精度丢失", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"path"`) || !strings.Contains(w.Body.String(), "[REDACTED]") {
		t.Fatal("响应键或脱敏错误")
	}
}

func TestEvidenceUTF8AndTruncatedSecretBoundary(t *testing.T) {
	x, _, hs, cookie := authenticated(t)
	root := t.TempDir()
	request := func(id, query string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/api/v1/evidence/"+id+query, nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body
	}
	os.WriteFile(filepath.Join(root, "unicode"), []byte("你好末尾"), 0600)
	x.mu.Lock()
	ref := x.addEvidence(root, "unicode", "unicode")
	x.mu.Unlock()
	code, body := request(ref.ID, "?offset=1&limit=3")
	if code != 200 || body["text"] != "你" || body["next_offset"] != float64(3) {
		t.Fatalf("字符中offset读取错误 %d %#v", code, body)
	}
	code, body = request(ref.ID, "?offset=3&limit=1")
	if code != 400 {
		t.Fatalf("不可容纳完整字符的limit应拒绝 %d %#v", code, body)
	}
	secret := "PREFIX-PRIVATE-CREDENTIAL-123456789"
	t.Setenv("MONITOR_TEST_API_KEY", secret)
	os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", (1<<20)-10)+secret+"tail"), 0600)
	x.mu.Lock()
	ref = x.addEvidence(root, "large", "large")
	x.mu.Unlock()
	code, body = request(ref.ID, "?offset=1048544&limit=64")
	if code != 200 || body["truncated"] != true {
		t.Fatalf("截断标记错误 %d %#v", code, body)
	}
	if strings.Contains(body["text"].(string), secret[:10]) || !strings.Contains(body["text"].(string), "[REDACTED]") {
		t.Fatal("证据截断暴露凭据前缀", body)
	}
}
