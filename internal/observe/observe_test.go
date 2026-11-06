package observe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func recorded(t *testing.T, dir, id string) []Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, data)
}
func decode(t *testing.T, data []byte) []Event {
	t.Helper()
	var events []Event
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 8192), 1024*1024)
	var previous uint64
	for s.Scan() {
		var event Event
		if err := json.Unmarshal(s.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence <= previous {
			t.Fatalf("序号未递增: %d <= %d", event.Sequence, previous)
		}
		previous = event.Sequence
		events = append(events, event)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
func find(events []Event, kind string) *Event {
	for i := range events {
		if events[i].EventType == kind {
			return &events[i]
		}
	}
	return nil
}

func TestLifecycleIdentityRedactionAndPermissions(t *testing.T) {
	dir := t.TempDir()
	secret := "known-private-credential-12345"
	t.Setenv("OPENAI_API_KEY", secret)
	ctx, a := Start(context.Background(), Options{WorkDir: t.TempDir(), Entry: "cli", Goal: "分析 " + secret, Model: "test-model"}, Config{Dir: dir})
	ids := Identity(ctx)
	ctx = WithTask(ctx, "task-1")
	ctx = WithAction(ctx, "action-1")
	ctx = WithModelCall(ctx, "model-1")
	in, out, cost := int64(25), int64(4), 0.1
	Emit(ctx, "model_usage", Payload{InputTokens: &in, OutputTokens: &out, Cost: &cost, Summary: "Bearer aaa-token-123 " + secret})
	in = 999
	out = 999
	cost = 999 // 入队必须复制指针值，不能保留调用方可变对象。
	sub := WithAgent(ctx, "child-1", ids.AgentID)
	Emit(sub, "agent_started", Payload{ParentActionID: "action-1"})
	a.Returned(Payload{Status: "ANSWERED", ResultRunID: "old-cached-run"})
	a.Returned(Payload{Status: "FAILED"})
	a.Close()
	events := recorded(t, dir, ids.AttemptID)
	if len(events) != 5 {
		t.Fatalf("事件数 %d: %#v", len(events), events)
	}
	if e := find(events, "run_returned"); e == nil || e.TaskID != "task-1" || e.Payload.ResultRunID != "old-cached-run" || e.AttemptID == "old-cached-run" {
		t.Fatalf("返回身份错误: %#v", e)
	}
	e := find(events, "model_usage")
	if e == nil || e.ActionID != "action-1" || e.ModelCallID != "model-1" || *e.Payload.InputTokens != 25 || *e.Payload.Cost != 0.1 {
		t.Fatalf("调用归属/值错误: %#v", e)
	}
	if child := find(events, "agent_started"); child == nil || child.ParentAgentID != ids.AgentID || child.ActionID != "" || child.ModelCallID != "" {
		t.Fatalf("子任务作用域错误: %#v", child)
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "aaa-token-123") {
		t.Fatal("凭据泄漏")
	}
	for p, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, ids.AttemptID): 0700, filepath.Join(dir, ids.AttemptID, "events.jsonl"): 0600} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("权限 %s: %v %v", p, info, err)
		}
	}
	if footer := find(events, "recorder_closed"); footer == nil || footer.Payload.Incomplete {
		t.Fatalf("结束记录错误: %#v", footer)
	}
}

func TestOffDoesNotCreateStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "不存在")
	t.Setenv("CLAW_OBSERVE", "off")
	t.Setenv("CLAW_OBSERVE_DIR", dir)
	ctx, a := StartFromEnv(context.Background(), Options{WorkDir: t.TempDir()})
	Emit(ctx, "model_started", Payload{})
	a.Returned(Payload{})
	a.Close()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("禁用后写入了目录: %v", err)
	}
}

type memoryWriter struct {
	mu         sync.Mutex
	data       bytes.Buffer
	gate       <-chan struct{}
	entered    chan struct{}
	once       sync.Once
	failWrites int
	failSync   bool
}

func (w *memoryWriter) Write(p []byte) (int, error) {
	if w.gate != nil {
		w.once.Do(func() {
			if w.entered != nil {
				close(w.entered)
			}
			<-w.gate
		})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failWrites > 0 {
		w.failWrites--
		return 0, errors.New("模拟写盘故障")
	}
	return w.data.Write(p)
}
func (w *memoryWriter) Sync() error {
	if w.failSync {
		return errors.New("模拟同步失败")
	}
	return nil
}
func (w *memoryWriter) Close() error { return nil }
func (w *memoryWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data.Bytes())
}

func TestQueueOverflowKeepsEndAndReportsGap(t *testing.T) {
	gate := make(chan struct{})
	writer := &memoryWriter{gate: gate, entered: make(chan struct{})}
	ctx, a := start(context.Background(), Options{}, Config{QueueSize: 2, Dir: t.TempDir()}, func(string, string) (eventWriter, error) { return writer, nil })
	<-writer.entered
	begin := time.Now()
	for i := 0; i < 100; i++ {
		Emit(ctx, "model_started", Payload{})
	}
	a.Returned(Payload{Status: "ANSWERED"})
	if time.Since(begin) > 250*time.Millisecond {
		t.Fatal("Emit 被磁盘阻塞")
	}
	close(gate)
	a.Close()
	events := decode(t, writer.bytes())
	if find(events, "run_returned") == nil {
		t.Fatal("队列已满时丢失终态")
	}
	footer := find(events, "recorder_closed")
	if footer == nil || !footer.Payload.Incomplete || footer.Payload.Dropped == 0 || find(events, "observation_gap") == nil {
		t.Fatalf("未说明缺口: %#v", events)
	}
}

func TestLimitsAndFooter(t *testing.T) {
	writer := &memoryWriter{}
	ctx, a := start(context.Background(), Options{}, Config{Dir: t.TempDir(), MaxAttemptBytes: 4096, ReserveBytes: 1024}, func(string, string) (eventWriter, error) { return writer, nil })
	for i := 0; i < 100; i++ {
		Emit(ctx, "tool_finished", Payload{Summary: strings.Repeat("中", 200)})
	}
	a.Returned(Payload{Status: "ANSWERED"})
	a.Close()
	data := writer.bytes()
	if len(data) > 4096 {
		t.Fatalf("超过单次上限: %d", len(data))
	}
	events := decode(t, data)
	footer := find(events, "recorder_closed")
	if footer == nil || !footer.Payload.Incomplete {
		t.Fatalf("缺少超限标记: %#v", events)
	}
	if find(events, "run_returned") == nil {
		t.Fatal("未为终态预留空间")
	}
}

func TestOversizedRecordUnknownUsageAndUTF8(t *testing.T) {
	writer := &memoryWriter{}
	ctx, a := start(context.Background(), Options{}, Config{Dir: t.TempDir(), MaxRecordBytes: 1000}, func(string, string) (eventWriter, error) { return writer, nil })
	Emit(ctx, "tool_finished", Payload{Path: strings.Repeat("中", 400), Summary: strings.Repeat("文", 400)})
	bad := math.NaN()
	negative := int64(-1)
	Emit(ctx, "model_usage", Payload{Cost: &bad, InputTokens: &negative})
	a.Returned(Payload{Status: "ANSWERED"})
	a.Close()
	events := decode(t, writer.bytes())
	if find(events, "tool_finished") != nil {
		t.Fatal("超长记录没有拒绝")
	}
	u := find(events, "model_usage")
	if u == nil || u.Payload.Cost != nil || u.Payload.InputTokens != nil || u.Payload.OutputTokens != nil {
		t.Fatal("未知用量未保持null")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(writer.bytes()), []byte{'\n'}) {
		if len(line)+1 > 1000 {
			t.Fatal("超过单条上限")
		}
	}
}

func TestWriteFailureCanRecoverWithGap(t *testing.T) {
	writer := &memoryWriter{failWrites: 1}
	ctx, a := start(context.Background(), Options{}, Config{Dir: t.TempDir()}, func(string, string) (eventWriter, error) { return writer, nil })
	Emit(ctx, "model_finished", Payload{})
	a.Returned(Payload{Status: "ANSWERED"})
	a.Close()
	events := decode(t, writer.bytes())
	if footer := find(events, "recorder_closed"); footer == nil || !footer.Payload.Incomplete || footer.Payload.Dropped == 0 {
		t.Fatalf("写故障未记录: %#v", events)
	}
}

func TestBadDirectoryAndBoundedClose(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "文件")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, a := Start(context.Background(), Options{}, Config{Dir: path})
	Emit(ctx, "model_started", Payload{})
	a.Returned(Payload{Status: "ANSWERED"})
	a.Close()
	gate := make(chan struct{})
	writer := &memoryWriter{gate: gate, entered: make(chan struct{})}
	_, blocked := start(context.Background(), Options{}, Config{Dir: t.TempDir(), CloseTimeout: 20 * time.Millisecond}, func(string, string) (eventWriter, error) { return writer, nil })
	<-writer.entered
	begin := time.Now()
	blocked.Close()
	if time.Since(begin) > 200*time.Millisecond {
		t.Fatal("关闭等待没有上界")
	}
	close(gate)
	<-blocked.done
}

func TestConcurrentAgentsAndClose(t *testing.T) {
	dir := t.TempDir()
	ctx, a := Start(context.Background(), Options{}, Config{Dir: dir, QueueSize: 2048})
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := WithAgent(ctx, NewID(), Identity(ctx).AgentID)
			for i := 0; i < 50; i++ {
				Emit(WithModelCall(child, NewID()), "model_finished", Payload{})
			}
		}()
	}
	wg.Wait()
	a.Returned(Payload{})
	a.Close()
	events := recorded(t, dir, Identity(ctx).AttemptID)
	if len(events) != 403 {
		t.Fatalf("并发记录丢失 %d", len(events))
	}
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			t.Fatalf("并发序号缺口 %d", e.Sequence)
		}
	}
}

func TestWorkspaceIdentityCanonicalAndSecretPatterns(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if WorkspaceID(root) != WorkspaceID(alias) {
		t.Fatal("别名工作区身份不一致")
	}
	value := Redact(`{"api_key":"hello-secret-key"} Bearer tok123456 https://user:pass@example.com sk-secret-12345`)
	for _, secret := range []string{"hello-secret-key", "tok123456", "user:pass", "sk-secret-12345"} {
		if strings.Contains(value, secret) {
			t.Fatal("凭据格式未脱敏")
		}
	}
}

func TestHeartbeatStopsAtReturned(t *testing.T) {
	writer := &memoryWriter{}
	_, a := start(context.Background(), Options{}, Config{Dir: t.TempDir(), HeartbeatInterval: 5 * time.Millisecond}, func(string, string) (eventWriter, error) { return writer, nil })
	deadline := time.After(time.Second)
	for !bytes.Contains(writer.bytes(), []byte(`"event_type":"heartbeat"`)) {
		select {
		case <-deadline:
			t.Fatal("未生成活动信号")
		case <-time.After(time.Millisecond):
		}
	}
	a.Returned(Payload{Status: "ANSWERED"})
	time.Sleep(15 * time.Millisecond)
	a.Close()
	events := decode(t, writer.bytes())
	returned := find(events, "run_returned")
	for _, event := range events {
		if event.EventType == "heartbeat" && event.Sequence > returned.Sequence {
			t.Fatal("返回后仍生成活动信号")
		}
	}
}

func TestSyncFailureIsNotComplete(t *testing.T) {
	writer := &memoryWriter{failSync: true}
	_, a := start(context.Background(), Options{}, Config{Dir: t.TempDir()}, func(string, string) (eventWriter, error) { return writer, nil })
	a.Returned(Payload{Status: "ANSWERED"})
	a.Close()
	events := decode(t, writer.bytes())
	gap := find(events, "observation_gap")
	if gap == nil || !gap.Payload.Incomplete || gap.Payload.Dropped == 0 {
		t.Fatalf("同步失败却声明记录完整: %#v", events)
	}
}
