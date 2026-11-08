package observe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config 的零值使用正式运行默认值。可用于不依赖环境变量的本地验证。
type Config struct {
	Dir               string
	QueueSize         int
	MaxRecordBytes    int
	MaxAttemptBytes   int64
	ReserveBytes      int64
	FlushInterval     time.Duration
	HeartbeatInterval time.Duration
	CloseTimeout      time.Duration
}

func (c Config) defaults() Config {
	if c.Dir == "" {
		c.Dir = DefaultDir()
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 1024
	}
	if c.MaxRecordBytes <= 0 {
		c.MaxRecordBytes = 8 * 1024
	}
	if c.MaxAttemptBytes <= 0 {
		c.MaxAttemptBytes = 16 * 1024 * 1024
	}
	if c.ReserveBytes <= 0 {
		c.ReserveBytes = 64 * 1024
	}
	if c.ReserveBytes >= c.MaxAttemptBytes {
		c.ReserveBytes = c.MaxAttemptBytes / 4
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 250 * time.Millisecond
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 2 * time.Second
	}
	if c.CloseTimeout <= 0 || c.CloseTimeout > time.Second {
		c.CloseTimeout = time.Second
	}
	return c
}

type eventWriter interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}
type writerFactory func(string, string) (eventWriter, error)

// Attempt 是一次入口调用的观察流。所有 Emit 都只在内存中提交，不执行磁盘 I/O。
type Attempt struct {
	mu              sync.Mutex
	ids             IdentityInfo
	taskID          string
	config          Config
	secrets         []string
	queue           []Event
	urgent          []Event
	sequence        uint64
	dropped         uint64
	reportedDropped uint64
	gapRecords      int
	returned        bool
	closing         bool
	disabled        bool
	wake            chan struct{}
	done            chan struct{}
	stopHeartbeat   chan struct{}
	heartbeatOnce   sync.Once
	closeOnce       sync.Once
	warnOnce        sync.Once
}

func StartFromEnv(ctx context.Context, options Options) (context.Context, *Attempt) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CLAW_OBSERVE")), "off") {
		return ctx, &Attempt{disabled: true}
	}
	return Start(ctx, options, Config{})
}

func Start(ctx context.Context, options Options, config Config) (context.Context, *Attempt) {
	return start(ctx, options, config, openWriter)
}

func start(ctx context.Context, options Options, config Config, open writerFactory) (context.Context, *Attempt) {
	config = config.defaults()
	a := &Attempt{config: config, secrets: knownSecrets(), taskID: options.TaskID, wake: make(chan struct{}, 1), done: make(chan struct{}), stopHeartbeat: make(chan struct{}), queue: make([]Event, 0, config.QueueSize)}
	a.ids = IdentityInfo{AttemptID: NewID(), WorkspaceID: WorkspaceID(options.WorkDir), TaskID: options.TaskID, AgentID: NewID()}
	ctx = context.WithValue(ctx, contextKey{}, scope{attempt: a, ids: a.ids})
	go a.run(open)
	Emit(ctx, "run_attempt_started", Payload{Entry: options.Entry, Goal: options.Goal, Kind: options.Kind, Mode: options.Mode, Model: options.Model, MaxModelCalls: int64(options.MaxModelCalls), MaxToolCalls: int64(options.MaxToolCalls), MaxTurns: int64(options.MaxTurns), TimeoutMS: options.TimeoutMS})
	go func() {
		ticker := time.NewTicker(config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-a.stopHeartbeat:
				return
			case <-ticker.C:
				a.emit(a.ids, "heartbeat", Payload{})
			}
		}
	}()
	return ctx, a
}

func terminal(kind string) bool {
	switch kind {
	case "run_finalized", "run_returned", "delivery_attempted", "delivery_finished", "observation_gap", "recorder_closed":
		return true
	}
	return false
}
func validKind(kind string) bool {
	if len(kind) == 0 || len(kind) > 64 {
		return false
	}
	for _, r := range kind {
		if r != '_' && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

func (a *Attempt) emit(ids IdentityInfo, kind string, payload Payload) {
	if a == nil || a.disabled {
		return
	}
	payload = cleanPayload(payload, a.secrets)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing || kind == "heartbeat" && a.returned {
		return
	}
	a.sequence++
	if !validKind(kind) {
		a.dropped++
		a.signal()
		return
	}
	if ids.TaskID == "" {
		ids.TaskID = a.taskID
	}
	e := Event{SchemaVersion: SchemaVersion, AttemptID: a.ids.AttemptID, WorkspaceID: a.ids.WorkspaceID, Sequence: a.sequence, RecordedAt: time.Now().UTC(), EventType: kind, TaskID: bounded(redact(ids.TaskID, a.secrets), 128), AgentID: bounded(ids.AgentID, 128), ParentAgentID: bounded(ids.ParentAgentID, 128), ActionID: bounded(ids.ActionID, 128), ModelCallID: bounded(ids.ModelCallID, 128), SourceTaskSequence: ids.SourceTaskSequence, Payload: payload}
	if data, err := json.Marshal(e); err != nil || len(data)+1 > a.config.MaxRecordBytes {
		a.dropped++
		a.signal()
		return
	}
	if len(a.queue) < a.config.QueueSize {
		a.queue = append(a.queue, e)
	} else if terminal(kind) && len(a.urgent) < 16 {
		a.urgent = append(a.urgent, e)
	} else {
		a.dropped++
	}
	a.signal()
}
func (a *Attempt) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *Attempt) stopClock() { a.heartbeatOnce.Do(func() { close(a.stopHeartbeat) }) }

// Returned 由调用方在 Run 真正返回后调用，不由引擎 defer 代替。
func (a *Attempt) Returned(payload Payload) {
	if a == nil || a.disabled {
		return
	}
	a.mu.Lock()
	if a.returned {
		a.mu.Unlock()
		return
	}
	a.returned = true
	a.mu.Unlock()
	a.stopClock()
	a.emit(a.ids, "run_returned", payload)
}

// Close 仅等待有界刷新。卡住的磁盘不会无限拖住进程收尾。
func (a *Attempt) Close() {
	if a == nil {
		return
	}
	a.CloseWithin(a.config.CloseTimeout)
}

// CloseWithin 允许入口与独立诊断写入共享退出刷新期限。
func (a *Attempt) CloseWithin(timeout time.Duration) {
	if a == nil || a.disabled {
		return
	}
	a.closeOnce.Do(func() { a.stopClock(); a.mu.Lock(); a.closing = true; a.signal(); a.mu.Unlock() })
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(min(timeout, a.config.CloseTimeout))
	defer timer.Stop()
	select {
	case <-a.done:
	case <-timer.C:
		a.warn()
	}
}
func (a *Attempt) warn() {
	a.warnOnce.Do(func() { log.Print("观测记录不完整，任务执行结果不受影响。") })
}
func (a *Attempt) lost(n uint64) { a.mu.Lock(); a.dropped += n; a.mu.Unlock(); a.warn() }

func (a *Attempt) drain() ([]Event, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	batch := append(a.queue, a.urgent...)
	a.queue = make([]Event, 0, a.config.QueueSize)
	a.urgent = nil
	// 缺口摘要也有上限，避免长期超额后的心跳耗尽预留终态空间。
	if a.dropped > a.reportedDropped && a.gapRecords < 8 {
		a.sequence++
		batch = append(batch, Event{SchemaVersion: SchemaVersion, AttemptID: a.ids.AttemptID, WorkspaceID: a.ids.WorkspaceID, TaskID: a.taskID, AgentID: a.ids.AgentID, Sequence: a.sequence, RecordedAt: time.Now().UTC(), EventType: "observation_gap", Payload: Payload{Dropped: a.dropped, Incomplete: true}})
		a.reportedDropped = a.dropped
		a.gapRecords++
	}
	sort.Slice(batch, func(i, j int) bool { return batch[i].Sequence < batch[j].Sequence })
	return batch, a.closing
}

func (a *Attempt) footer() Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sequence++
	return Event{SchemaVersion: SchemaVersion, AttemptID: a.ids.AttemptID, WorkspaceID: a.ids.WorkspaceID, TaskID: a.taskID, AgentID: a.ids.AgentID, Sequence: a.sequence, RecordedAt: time.Now().UTC(), EventType: "recorder_closed", Payload: Payload{Dropped: a.dropped, Incomplete: a.dropped > 0}}
}

func openWriter(dir, id string) (eventWriter, error) {
	if dir == "" {
		return nil, errors.New("未配置观测目录")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(id, 0700); err != nil {
		return nil, err
	}
	return root.OpenFile(filepath.Join(id, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}

func (a *Attempt) run(open writerFactory) {
	defer close(a.done)
	var writer eventWriter
	var size int64
	ticker := time.NewTicker(a.config.FlushInterval)
	defer ticker.Stop()
	defer func() {
		if writer != nil {
			_ = writer.Close()
		}
	}()
	write := func(e Event) bool {
		data, err := json.Marshal(e)
		if err != nil {
			a.lost(1)
			return false
		}
		data = append(data, '\n')
		limit := a.config.MaxAttemptBytes
		if !terminal(e.EventType) {
			limit -= a.config.ReserveBytes
		} else if e.EventType == "observation_gap" {
			// 缺口摘要只可使用预留区的四分之一，其余保留给返回和关闭回执。
			limit -= a.config.ReserveBytes * 3 / 4
		}
		if len(data) > a.config.MaxRecordBytes || size+int64(len(data)) > limit {
			a.lost(1)
			return false
		}
		if writer == nil {
			writer, err = open(a.config.Dir, a.ids.AttemptID)
			if err != nil {
				a.lost(1)
				return false
			}
		}
		n, err := writer.Write(data)
		if err != nil || n != len(data) {
			// 本观测流只有一个 writer。发生半写时回退到最后完整行；无法回退则停止追加。
			if file, ok := writer.(*os.File); ok {
				if truncateErr := file.Truncate(size); truncateErr == nil {
					_, _ = file.Seek(size, io.SeekStart)
				} else {
					_ = writer.Close()
					writer = nil
					open = func(string, string) (eventWriter, error) { return nil, errors.New("观测流已损坏") }
				}
			}
			a.lost(1)
			return false
		}
		size += int64(n)
		return true
	}
	for {
		select {
		case <-a.wake:
		case <-ticker.C:
			if writer != nil {
				if err := writer.Sync(); err != nil {
					a.lost(1)
				}
			}
		}
		batch, closing := a.drain()
		for _, e := range batch {
			write(e)
		}
		if closing {
			write(a.footer())
			if writer != nil {
				if err := writer.Sync(); err != nil {
					a.lost(1)
					correction := a.footer()
					correction.EventType = "observation_gap"
					write(correction)
					_ = writer.Sync()
				}
			}
			return
		}
	}
}
