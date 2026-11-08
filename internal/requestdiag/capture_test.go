package requestdiag

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type events struct {
	mu     sync.Mutex
	values []Event
}

func (e *events) add(v Event) { e.mu.Lock(); defer e.mu.Unlock(); e.values = append(e.values, v) }
func (e *events) all() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.values...)
}
func setup(t *testing.T, config Config) (context.Context, *Attempt, *events) {
	t.Helper()
	if config.Dir == "" {
		config.Dir = t.TempDir()
	}
	e := &events{}
	ctx, a := Start(context.Background(), config, Identity{ObservationAttemptID: "observation"}, e.add)
	return ctx, a, e
}
func runHTTP(t *testing.T, ctx context.Context, agent, callID, body string, status int, success bool) (*Call, *HTTPAttempt) {
	t.Helper()
	ctx, c := BeginCall(ctx, Identity{AgentID: agent, ModelCallID: callID}, []Origin{{InputIndex: 0, Representation: "source", EvidenceID: "E1"}})
	ctx = WithWireMappings(ctx, map[int][]string{0: {"messages[0]"}})
	req, err := http.NewRequest("POST", "https://user:password@example.test/v1/chat/completions?api_key=private", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	h := BeginHTTP(ctx, req, "deepseek", "chat_completions")
	if h == nil {
		t.Fatal("缺少HTTP采集")
	}
	h.Finish(&http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": []string{"server-request"}}}, nil)
	c.Finish(Outcome{Success: success, ErrorClass: "model_content_rejected"})
	return c, h
}
func payload(text string) string {
	b, _ := json.Marshal(map[string]any{"model": "model", "messages": []any{map[string]any{"role": "user", "content": text}}})
	return string(b)
}

func TestConfigAndUnconfiguredCallsDoNotCreateFiles(t *testing.T) {
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "")
	t.Setenv("CLAW_OBSERVE", "off")
	cfg, err := ResolveConfig("", "")
	if err != nil || cfg.Mode != Off {
		t.Fatal(cfg, err)
	}
	cfg, err = ResolveConfig(OnError, "")
	if err != nil || cfg.Mode != OnError {
		t.Fatal(cfg, err)
	}
	if _, err = ResolveConfig("everything", ""); err == nil {
		t.Fatal("非法模式被接收")
	}
	_, c := BeginCall(context.Background(), Identity{}, nil)
	if c != nil {
		t.Fatal("直接调用创建诊断")
	}
	c.Finish(Outcome{})
	dir := filepath.Join(t.TempDir(), "not-created")
	ctx, a, _ := setup(t, Config{Mode: Off, Dir: dir})
	if Mode(ctx) != Off {
		t.Fatal("关闭模式失效")
	}
	a.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("关闭仍落盘", err)
	}
}

func TestMetadataRecordsActualPresenceWithoutBusinessBody(t *testing.T) {
	t.Setenv("SAMPLE_API_KEY", "key-very-private-12345")
	dir := t.TempDir()
	ctx, a, e := setup(t, Config{Mode: MetadataOnly, Dir: dir})
	body := `{"model":"model","temperature":0,"stream":false,"max_tokens":null,"messages":[{"role":"user","content":"business-body key-very-private-12345"}],"tools":[{"type":"function","function":{"name":"read_file","description":"key-very-private-12345"}}]}`
	runHTTP(t, ctx, "root", "one", body, 200, true)
	a.Close()
	es := e.all()
	if len(es) != 1 || es[0].Diagnostic.State != "saved" {
		t.Fatalf("未保存meta: %+v", es)
	}
	m := es[0].Metadata
	if m.Parameters["temperature"].Presence != "value" || string(m.Parameters["temperature"].Value) != "0" || m.Parameters["max_tokens"].Presence != "null" || m.Parameters["top_p"].Presence != "absent" || string(m.Parameters["stream"].Value) != "false" {
		t.Fatalf("参数状态失真: %+v", m.Parameters)
	}
	if m.InputCount != 1 || m.ToolCount != 1 || m.Items[0].Origin.EvidenceID != "E1" || m.RequestID == nil || m.ManifestID == "" {
		t.Fatalf("结构或来源失真: %+v", m)
	}
	if es[0].Diagnostic.Complete || es[0].Diagnostic.SavedBytes != 0 || es[0].Diagnostic.Reason != "metadata_only" || m.BodyCaptureState != "not_enabled" {
		t.Fatal("仅元数据不能声称已保存完整请求正文")
	}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("business-body")) || bytes.Contains(data, []byte("key-very-private")) || strings.Contains(path, "redacted.json") {
				t.Errorf("metadata泄露正文: %s", path)
			}
		}
		return nil
	})
}

func TestFailureBodyPairRedactsSecretsAndPrivateThinking(t *testing.T) {
	t.Setenv("SAMPLE_API_KEY", "secret-from-env-12345")
	dir := t.TempDir()
	ctx, a, e := setup(t, Config{Mode: OnError, Dir: dir})
	runHTTP(t, ctx, "root", "good", payload("first-source"), 200, true)
	body := `{"model":"model","messages":[{"role":"user","content":"second-source secret-from-env-12345"},{"role":"assistant","reasoning_content":"hidden-thought","content":"public"}],"tools":[{"name":"x","description":"secret-from-env-12345"}],"input":[{"type":"reasoning","encrypted_content":"private-encrypted"}],"arguments":"{\"password\":\"unlisted-password\",\"thinking\":\"nested-private-thought\"}"}`
	runHTTP(t, ctx, "root", "bad", body, 400, false)
	a.Close()
	es := e.all()
	if len(es) != 2 {
		t.Fatal(len(es))
	}
	var failed Event
	for _, ev := range es {
		if ev.Identity.ModelCallID == "bad" {
			failed = ev
		}
	}
	if failed.Metadata.PreviousSuccessfulCallID != "good" || !failed.Diagnostic.PreviousAvailable || !failed.Diagnostic.Redacted {
		t.Fatalf("比较或脱敏错误: %+v", failed)
	}
	if !failed.Diagnostic.Complete || failed.Metadata.BodyCaptureState != "saved" || len(failed.Diagnostic.OmittedFields) == 0 {
		t.Fatal("正文完整性或省略字段未明确记录")
	}
	for _, ev := range es {
		if ev.Identity.ModelCallID == "good" && (ev.Diagnostic.Complete || ev.Diagnostic.SavedBytes != 0 || ev.Diagnostic.Reason != "success_body_not_saved" || ev.Metadata.BodyCaptureState != "cached_only") {
			t.Fatal("成功缓存被误报为正文落盘")
		}
	}
	path := filepath.Join(dir, failed.Identity.CaptureAttemptID, failed.Diagnostic.ID)
	current, err := os.ReadFile(filepath.Join(path, "request.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-from-env", "hidden-thought", "private-encrypted", "unlisted-password", "nested-private-thought"} {
		if bytes.Contains(current, []byte(forbidden)) {
			t.Fatal("泄露", forbidden)
		}
	}
	previous, err := os.ReadFile(filepath.Join(path, "previous-success.redacted.json"))
	if err != nil || !bytes.Contains(previous, []byte("first-source")) {
		t.Fatal("缺少上一成功", err)
	}
	var decoded any
	if json.Unmarshal(current, &decoded) != nil {
		t.Fatal("保存了残缺JSON")
	}
	info, _ := os.Stat(filepath.Join(path, "request.redacted.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("权限过宽")
	}
}

func TestPreviousSuccessFrozenAtBeginAndSeparatedByAgent(t *testing.T) {
	ctx, a, e := setup(t, Config{Mode: OnError})
	runHTTP(t, ctx, "root", "initial", payload("zero"), 200, true)
	oldCtx, old := BeginCall(ctx, Identity{AgentID: "root", ModelCallID: "earlier-start"}, nil)
	runHTTP(t, ctx, "root", "later-success", payload("later"), 200, true)
	req, _ := http.NewRequest("POST", "https://example.test/v1/chat/completions", strings.NewReader(payload("old-fails")))
	h := BeginHTTP(oldCtx, req, "deepseek", "chat_completions")
	h.Finish(&http.Response{StatusCode: 400}, nil)
	old.Finish(Outcome{ErrorClass: "failure"})
	runHTTP(t, ctx, "child", "child-fails", payload("child"), 400, false)
	runHTTP(t, ctx, "root", "parse-fails", payload("bad-response"), 200, false)
	runHTTP(t, ctx, "root", "last-fails", payload("last"), 400, false)
	a.Close()
	byID := map[string]*Metadata{}
	for _, ev := range e.all() {
		byID[ev.Identity.ModelCallID] = ev.Metadata
	}
	if byID["earlier-start"].PreviousSuccessfulCallID != "initial" {
		t.Fatal("把后来成功倒灌成上一成功")
	}
	if byID["child-fails"].PreviousSuccessfulCallID != "" {
		t.Fatal("跨Agent串用")
	}
	if byID["last-fails"].PreviousSuccessfulCallID != "later-success" {
		t.Fatal("HTTP200解析失败更新了成功缓存")
	}
}

func TestMissingBodyAndSizeLimitDoNotConsumeRequest(t *testing.T) {
	for _, variant := range []string{"missing", "oversize"} {
		t.Run(variant, func(t *testing.T) {
			ctx, a, e := setup(t, Config{Mode: OnError, MaxRequestBytes: 16})
			ctx, c := BeginCall(ctx, Identity{ModelCallID: "call", AgentID: "root"}, nil)
			raw := payload("original-data")
			req, _ := http.NewRequest("POST", "https://example.test", strings.NewReader(raw))
			if variant == "missing" {
				req.GetBody = nil
			}
			h := BeginHTTP(ctx, req, "provider", "chat")
			after, _ := io.ReadAll(req.Body)
			if string(after) != raw {
				t.Fatal("消耗或更改了原请求")
			}
			h.Finish(&http.Response{StatusCode: 400}, nil)
			c.Finish(Outcome{})
			a.Close()
			es := e.all()
			if len(es) != 1 || es[0].Diagnostic.Complete || es[0].Diagnostic.SavedBytes != 0 {
				t.Fatalf("未知冒充完整: %+v", es)
			}
		})
	}
}

func TestUnknownContentLengthDoesNotBecomeZero(t *testing.T) {
	ctx, a, e := setup(t, Config{Mode: MetadataOnly})
	ctx, c := BeginCall(ctx, Identity{AgentID: "root", ModelCallID: "unknown-size"}, nil)
	req, _ := http.NewRequest("POST", "https://example.test", strings.NewReader(payload("nonempty")))
	req.ContentLength, req.GetBody = 0, nil
	h := BeginHTTP(ctx, req, "provider", "chat")
	h.Finish(&http.Response{StatusCode: 400}, nil)
	c.Finish(Outcome{})
	a.Close()
	if e.all()[0].Metadata.RequestBytes != nil {
		t.Fatal("未知ContentLength被伪造为零")
	}
}

func TestQuotaAndSymlinkFailuresRemainDiagnosticOnly(t *testing.T) {
	for _, variant := range []string{"directory", "attempt", "symlink", "busy"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{Mode: OnError, Dir: dir}
			var lock *os.File
			switch variant {
			case "directory":
				cfg.MaxDirectoryBytes = 1
			case "attempt":
				cfg.MaxAttemptBytes = 1
			case "symlink":
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
					t.Fatal(err)
				}
			case "busy":
				var err error
				lock, err = os.OpenFile(filepath.Join(dir, ".quota.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			}
			ctx, a, e := setup(t, cfg)
			runHTTP(t, ctx, "root", "call", payload("data"), 400, false)
			a.Close()
			es := e.all()
			if len(es) != 1 || es[0].Diagnostic.State != "unavailable" || es[0].Metadata.ManifestID != "" {
				t.Fatalf("故障被记为成功: %+v", es)
			}
			if es[0].Diagnostic.Complete || es[0].Diagnostic.SavedBytes != 0 || es[0].Diagnostic.PreviousAvailable {
				t.Fatal("保存失败仍声称副本完整")
			}
		})
	}
}

func TestSavedBodyExpansionDoesNotClaimCompleteJSON(t *testing.T) {
	ctx, a, e := setup(t, Config{Mode: OnError, MaxRequestBytes: 70})
	runHTTP(t, ctx, "root", "failure", `{"model":"m","messages":[{"role":"user","content":"x"}]}`, 400, false)
	a.Close()
	ev := e.all()[0]
	if ev.Metadata.CaptureState != "complete" || ev.Metadata.BodyCaptureState != "unavailable" || ev.Diagnostic.Complete || ev.Diagnostic.SavedBytes != 0 || ev.Diagnostic.Reason != "saved_body_limit" {
		t.Fatalf("元数据与保存正文完整性混淆: %+v %+v", ev.Metadata, ev.Diagnostic)
	}
}

func TestAttemptQuotaIncludesOrphanedPendingBytes(t *testing.T) {
	dir := t.TempDir()
	ctx, a, e := setup(t, Config{Mode: MetadataOnly, Dir: dir, MaxAttemptBytes: 10000})
	pending := filepath.Join(dir, a.identity.CaptureAttemptID, ".pending-orphan")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "partial.json"), make([]byte, 9500), 0600); err != nil {
		t.Fatal(err)
	}
	runHTTP(t, ctx, "root", "failure", payload("data"), 200, true)
	a.Close()
	ev := e.all()[0]
	if ev.Diagnostic.State != "unavailable" || ev.Diagnostic.Reason != "attempt_quota" {
		t.Fatalf("遗留字节绕过单次额度: %+v", ev.Diagnostic)
	}
}

func TestParameterSummariesAndBoundedDifferences(t *testing.T) {
	ctx, a, e := setup(t, Config{Mode: MetadataOnly})
	runHTTP(t, ctx, "root", "first", `{"model":"m","temperature":0,"messages":[{"role":"user","content":"old"}],"tools":[{"name":"old"}]}`, 200, true)
	runHTTP(t, ctx, "root", "second", `{"model":"m","temperature":1,"tool_choice":{"type":"function","function":{"name":"read_file"}},"text":{"format":{"type":"json_schema","schema":{"secret-business-description":"hidden"}}},"reasoning_effort":"low","messages":[{"role":"user","content":"new"}],"tools":[{"name":"new"}]}`, 400, false)
	a.Close()
	m := e.all()[1].Metadata
	if m.ToolChoiceMode != "function" || m.ToolChoiceName != "read_file" || m.ResponseFormatType != "json_schema" || m.ResponseFormatSchemaFingerprint == "" || m.ReasoningEffort != "low" || !m.SchemaChanged || !m.DiffComplete || len(m.ChangedPaths) != 1 {
		t.Fatalf("安全摘要或差异缺失: %+v", m)
	}
	if !strings.Contains(strings.Join(m.ChangedParameters, ","), "temperature") || bytes.Contains(m.Parameters["text"].Value, []byte("hidden")) {
		t.Fatal("差异缺失或参数泄露业务Schema")
	}
	before := Metadata{CaptureState: "complete", Items: []InputItem{{JSONPath: "one", Fingerprint: "a"}}, OmittedItems: 1}
	after := Metadata{CaptureState: "complete", Items: []InputItem{{JSONPath: "one", Fingerprint: "b"}}}
	_, _, _, complete := changes(&after, &before)
	if complete {
		t.Fatal("省略清单被当成完整差异")
	}
	before.OmittedItems = 0
	after.Items = nil
	for i := 0; i < 129; i++ {
		after.Items = append(after.Items, InputItem{JSONPath: strconv.Itoa(i)})
	}
	added, _, _, complete := changes(&after, &before)
	if complete || len(added) != 128 {
		t.Fatal("差异截断未降级")
	}
}

func TestBuildWhitelistAndManifestBoundaries(t *testing.T) {
	b := selectedBuildInfo(&debug.BuildInfo{GoVersion: "go1.test", Path: "private-module", Main: debug.Module{Path: "private-path"}, Settings: []debug.BuildSetting{{Key: "-ldflags", Value: "secret-endpoint"}, {Key: "vcs.revision", Value: "revision"}, {Key: "vcs.modified", Value: "true"}}, Deps: []*debug.Module{{Path: "github.com/openai/openai-go/v3", Version: "v3.1", Replace: &debug.Module{Path: "private-replace"}}, {Path: "private-module", Version: "private-version"}}})
	data, _ := json.Marshal(b)
	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("private")) || b.VCSModified == nil || !*b.VCSModified || b.VCSRevision != "revision" || b.SDKVersions["github.com/openai/openai-go/v3"] != "v3.1" || len(b.SDKVersions) != 1 {
		t.Fatal("构建信息超出白名单", string(data))
	}
	dir := t.TempDir()
	ctx, a, e := setup(t, Config{Mode: MetadataOnly, Dir: dir})
	var messages []any
	for i := 0; i < 800; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": "not-stored"})
	}
	body, _ := json.Marshal(map[string]any{"model": "m", "messages": messages})
	runHTTP(t, ctx, "root", "large-list", string(body), 200, true)
	a.Close()
	ev := e.all()[0]
	if ev.Metadata.OmittedItems == 0 || ev.Metadata.InputCount != 800 || ev.Metadata.ProjectionVersion != 1 {
		t.Fatal("来源清单数量丢失")
	}
	path := filepath.Join(dir, ev.Identity.CaptureAttemptID, ev.Metadata.ManifestID, "manifest.json")
	info, err := os.Stat(path)
	if err != nil || info.Size() > 64<<10 || info.Mode().Perm() != 0600 {
		t.Fatal("清单大小或权限错误", err)
	}
	for _, path := range []string{dir, filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("目录权限错误", err)
		}
	}
}

func TestQueueOverflowAndBoundedCloseWarnOnce(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	blocked, release := make(chan struct{}), make(chan struct{})
	e := &events{}
	ctx, a := Start(context.Background(), Config{Mode: OnError, Dir: t.TempDir(), QueueSize: 1}, Identity{}, func(event Event) {
		e.add(event)
		if event.Identity.ModelCallID == "block" {
			close(blocked)
			<-release
		}
	})
	runHTTP(t, ctx, "root", "block", payload("data"), 400, false)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("writer未开始")
	}
	runHTTP(t, ctx, "root", "queued", payload("data"), 400, false)
	runHTTP(t, ctx, "root", "overflow", payload("data"), 400, false)
	started := time.Now()
	a.CloseWithin(5 * time.Millisecond)
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("退出受观测阻塞")
	}
	close(release)
	a.Close()
	if strings.Count(output.String(), "请求诊断记录不完整") != 1 {
		t.Fatal("诊断告警没有限制为一次", output.String())
	}
	found := false
	for _, event := range e.all() {
		if event.Identity.ModelCallID == "overflow" && event.Diagnostic.State == "dropped" {
			found = true
		}
	}
	if !found {
		t.Fatal("队列溢出未记录缺口")
	}
}

func TestConcurrentCallsLateFinishAndRepeatedClose(t *testing.T) {
	ctx, a, e := setup(t, Config{Mode: OnError})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); runHTTP(t, ctx, "agent", id(), payload("parallel"), 200, true) }()
	}
	wg.Wait()
	lateCtx, late := BeginCall(ctx, Identity{AgentID: "root", ModelCallID: "late"}, nil)
	req, _ := http.NewRequest("POST", "https://example.test", strings.NewReader(payload("late")))
	h := BeginHTTP(lateCtx, req, "p", "chat")
	a.CloseWithin(time.Second)
	a.Close()
	h.Finish(&http.Response{StatusCode: 400}, nil)
	late.Finish(Outcome{})
	if len(e.all()) < 4 {
		t.Fatal("并发元数据丢失")
	}
}
