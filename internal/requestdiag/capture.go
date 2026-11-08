package requestdiag

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

type attemptKey struct{}
type callKey struct{}
type mappingKey struct{}

type Attempt struct {
	mu        sync.Mutex
	config    Config
	identity  Identity
	sink      Sink
	secrets   []string
	key       []byte
	closing   bool
	abandon   bool
	memory    int64
	cache     map[string]*record
	queue     chan *saveJob
	done      chan struct{}
	closeOnce sync.Once
	warnOnce  sync.Once
	build     BuildInfo
}
type Call struct {
	mu       sync.Mutex
	a        *Attempt
	identity Identity
	origins  []Origin
	prior    map[string]*record
	http     []*HTTPAttempt
	finished bool
}
type HTTPAttempt struct {
	call     *Call
	meta     Metadata
	mappings map[int][]string
	body     []byte
	redacted bool
	omitted  []string
	previous *record
	charge   int64
	finished bool
	started  time.Time
}
type record struct {
	meta     Metadata
	body     []byte
	redacted bool
	charge   int64
	refs     int
}
type saveJob struct {
	identity   Identity
	meta       Metadata
	body       []byte
	redacted   bool
	previous   *record
	current    *record
	diagnostic Diagnostic
}

func id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func Start(ctx context.Context, config Config, identity Identity, sink Sink) (context.Context, *Attempt) {
	cfg, err := config.normalized()
	a := &Attempt{config: cfg, identity: identity, sink: sink, secrets: secretsFromEnv(), cache: map[string]*record{}, done: make(chan struct{}), build: readBuildInfo()}
	if err != nil || cfg.Mode == Off {
		if err != nil {
			a.warn("configuration_unavailable")
		}
		a.closing = true
		close(a.done)
		return context.WithValue(ctx, attemptKey{}, a), a
	}
	a.identity.CaptureAttemptID = id()
	a.key = make([]byte, 32)
	if a.identity.CaptureAttemptID == "" {
		a.warn("id_unavailable")
		a.closing = true
		close(a.done)
		return context.WithValue(ctx, attemptKey{}, a), a
	}
	if _, err := rand.Read(a.key); err != nil {
		a.warn("id_unavailable")
		a.closing = true
		close(a.done)
		return context.WithValue(ctx, attemptKey{}, a), a
	}
	a.queue = make(chan *saveJob, cfg.QueueSize)
	go a.writer()
	return context.WithValue(ctx, attemptKey{}, a), a
}

func Mode(ctx context.Context) string {
	a, _ := ctx.Value(attemptKey{}).(*Attempt)
	if a == nil {
		return Off
	}
	return a.config.Mode
}

func BeginCall(ctx context.Context, identity Identity, origins []Origin) (context.Context, *Call) {
	a, _ := ctx.Value(attemptKey{}).(*Attempt)
	if a == nil {
		return ctx, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return ctx, nil
	}
	identity.CaptureAttemptID = a.identity.CaptureAttemptID
	if identity.ObservationAttemptID == "" {
		identity.ObservationAttemptID = a.identity.ObservationAttemptID
	}
	if identity.WorkspaceID == "" {
		identity.WorkspaceID = a.identity.WorkspaceID
	}
	c := &Call{a: a, identity: identity, origins: append([]Origin(nil), origins...), prior: map[string]*record{}}
	// BeginCall冻结已完成的成功调用；失败时不会借用稍后完成或其他Agent的请求。
	for key, previous := range a.cache {
		if previous.meta.Identity.AgentID == identity.AgentID {
			previous.refs++
			c.prior[key] = previous
		}
	}
	return context.WithValue(ctx, callKey{}, c), c
}

func WithWireMappings(ctx context.Context, mappings map[int][]string) context.Context {
	copy := make(map[int][]string, len(mappings))
	for i, paths := range mappings {
		copy[i] = append([]string(nil), paths...)
	}
	return context.WithValue(ctx, mappingKey{}, copy)
}

func BeginHTTP(ctx context.Context, req *http.Request, provider, protocol string) *HTTPAttempt {
	c, _ := ctx.Value(callKey{}).(*Call)
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	a := c.a
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return nil
	}
	h := &HTTPAttempt{call: c, started: time.Now()}
	h.mappings, _ = ctx.Value(mappingKey{}).(map[int][]string)
	h.meta = Metadata{Version: 1, ProjectionVersion: 1, Build: a.build, CaptureMode: a.config.Mode, Identity: c.identity, Provider: safeText(provider, 128, a.secrets), Protocol: safeText(protocol, 128, a.secrets), HTTPAttempt: len(c.http) + 1, StartedAt: h.started.UTC(), CaptureState: "unavailable", CaptureReason: "no_body_copy", BodyCaptureState: "not_captured"}
	if a.config.Mode != OnError {
		h.meta.BodyCaptureState = "not_enabled"
	}
	reserve := a.config.MaxRequestBytes + 64<<10
	if a.config.Mode == OnError {
		reserve += a.config.MaxRequestBytes
	}
	if a.memory+reserve <= a.config.MaxMemoryBytes {
		a.memory += reserve
		h.charge = reserve
	} else {
		h.meta.CaptureReason = "memory_limit"
	}
	a.mu.Unlock()
	if req != nil {
		h.meta.Method = safeText(req.Method, 16, a.secrets)
		if req.URL != nil {
			h.meta.EndpointID = a.fingerprint([]byte(req.URL.Scheme + "://" + req.URL.Host + req.URL.EscapedPath()))
		}
		if req.ContentLength > 0 {
			n := req.ContentLength
			h.meta.RequestBytes = &n
		}
		if req.Body == nil || req.Body == http.NoBody {
			n := int64(0)
			h.meta.RequestBytes = &n
		}
		if n, err := strconv.Atoi(req.Header.Get("X-Stainless-Retry-Count")); err == nil && n >= 0 {
			h.meta.SDKRetryCount = &n
		}
		if h.charge > 0 {
			if req.Body == nil || req.Body == http.NoBody {
				n := int64(0)
				h.meta.RequestBytes = &n
				h.meta.CaptureReason = "empty_body"
			} else if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil || body == nil {
					h.meta.CaptureReason = "body_copy_error"
				} else {
					data, readErr := io.ReadAll(io.LimitReader(body, a.config.MaxRequestBytes+1))
					_ = body.Close()
					if readErr != nil {
						h.meta.CaptureReason = "body_copy_error"
					} else if int64(len(data)) > a.config.MaxRequestBytes {
						h.meta.CaptureReason = "request_size_limit"
					} else {
						n := int64(len(data))
						h.meta.RequestBytes = &n
						h.parse(data)
					}
				}
			}
		}
	}
	if h.meta.Parameters == nil {
		h.meta.Parameters = map[string]Parameter{}
		for _, name := range parameterNames {
			h.meta.Parameters[name] = Parameter{Presence: "unknown"}
		}
	}
	if a.config.Mode == OnError && len(h.body) == 0 {
		h.meta.BodyCaptureState = "unavailable"
	}
	key := cacheKey(c.identity, h.meta)
	h.previous = c.prior[key]
	if h.previous != nil {
		h.meta.PreviousSuccessfulCallID = h.previous.meta.Identity.ModelCallID
		h.meta.AddedPaths, h.meta.RemovedPaths, h.meta.ChangedPaths, h.meta.DiffComplete = changes(&h.meta, &h.previous.meta)
		for _, name := range parameterNames {
			old, _ := json.Marshal(h.previous.meta.Parameters[name])
			current, _ := json.Marshal(h.meta.Parameters[name])
			if string(old) != string(current) {
				h.meta.ChangedParameters = append(h.meta.ChangedParameters, name)
			}
		}
		h.meta.SchemaChanged = h.meta.SchemaFingerprint != h.previous.meta.SchemaFingerprint
	}
	encoded, _ := json.Marshal(h.meta)
	actual := int64(len(encoded) + len(h.body))
	a.mu.Lock()
	if h.charge > 0 {
		a.memory -= h.charge
		h.charge = actual
		a.memory += actual
	}
	a.mu.Unlock()
	c.http = append(c.http, h)
	return h
}

func (h *HTTPAttempt) Finish(resp *http.Response, err error) {
	if h == nil {
		return
	}
	c := h.call
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || h.finished {
		return
	}
	h.finished = true
	h.meta.DurationMS = time.Since(h.started).Milliseconds()
	if resp != nil {
		status := resp.StatusCode
		h.meta.HTTPStatus = &status
		for _, name := range []string{"x-request-id", "request-id", "anthropic-request-id"} {
			if value := resp.Header.Get(name); value != "" {
				v := safeText(value, 256, c.a.secrets)
				h.meta.RequestID = &v
				h.meta.RequestIDSource = "header:" + name
				break
			}
		}
	}
	if err != nil {
		h.meta.Error.Kind = "model_transport_error"
	}
}

func BindError(ctx context.Context, info ErrorInfo) {
	c, _ := ctx.Value(callKey{}).(*Call)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || len(c.http) == 0 {
		return
	}
	h := c.http[len(c.http)-1]
	h.meta.Error = safeError(info, c.a.secrets)
	if h.meta.HTTPStatus == nil && info.HTTPStatus != nil {
		v := *info.HTTPStatus
		h.meta.HTTPStatus = &v
	}
	if h.meta.RequestID == nil && h.meta.Error.RequestID != nil {
		h.meta.RequestID = h.meta.Error.RequestID
		h.meta.RequestIDSource = h.meta.Error.RequestIDSource
	}
}

func (c *Call) Finish(outcome Outcome) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	c.finished = true
	a := c.a
	for i, h := range c.http {
		if h.meta.Error.Kind == "" && !outcome.Success {
			h.meta.Error.Kind = safeText(outcome.ErrorClass, 64, a.secrets)
		}
		if !outcome.Success && outcome.ErrorClass != "" && h.meta.Error.Kind == "model_transport_error" {
			h.meta.Error.Kind = safeText(outcome.ErrorClass, 64, a.secrets)
		}
		failed := !outcome.Success || (h.meta.HTTPStatus != nil && *h.meta.HTTPStatus >= 400)
		d := Diagnostic{Mode: a.config.Mode, State: "pending", OriginalBytes: h.meta.RequestBytes, Redacted: h.redacted, RedactionVersion: 1, OmittedFields: append([]string(nil), h.omitted...)}
		if a.config.Mode == MetadataOnly {
			d.Reason = "metadata_only"
		} else if !failed {
			d.Reason = "success_body_not_saved"
			if len(h.body) > 0 {
				h.meta.BodyCaptureState = "cached_only"
			}
		}
		if h.meta.CaptureReason != "" {
			d.Reason = h.meta.CaptureReason
		}
		if failed && a.config.Mode == OnError {
			d.PreviousAvailable = h.previous != nil && len(h.previous.body) > 0
			d.SavedBytes = int64(len(h.body))
			d.Complete = len(h.body) > 0 && h.meta.BodyCaptureState == "captured"
			if len(h.body) == 0 {
				d.Complete = false
				d.Reason = h.meta.CaptureReason
			}
		}
		rec := &record{meta: h.meta, body: h.body, redacted: h.redacted, charge: h.charge, refs: 1}
		job := &saveJob{identity: c.identity, meta: h.meta, current: rec, diagnostic: d}
		if failed && a.config.Mode == OnError {
			job.body = h.body
			job.redacted = h.redacted
			job.previous = h.previous
		}
		a.mu.Lock()
		if !a.closing {
			if job.previous != nil {
				job.previous.refs++
			}
			select {
			case a.queue <- job:
			default:
				if job.previous != nil {
					a.releaseLocked(job.previous)
				}
				job = nil
			}
		} else {
			job = nil
		}
		if outcome.Success && i == len(c.http)-1 && !a.closing && h.meta.CaptureState == "complete" && h.meta.HTTPStatus != nil && *h.meta.HTTPStatus >= 200 && *h.meta.HTTPStatus < 300 {
			key := cacheKey(c.identity, h.meta)
			if key != "" {
				if old := a.cache[key]; old != nil {
					a.releaseLocked(old)
				}
				rec.refs++
				a.cache[key] = rec
			}
		}
		if outcome.Success && i == len(c.http)-1 && h.meta.CaptureState != "complete" {
			// 最新成功请求未捕获时，不能把更早的请求冒充为“上一成功”。
			for key, old := range a.cache {
				if old.meta.Identity.AgentID == c.identity.AgentID && old.meta.Provider == h.meta.Provider && old.meta.Protocol == h.meta.Protocol && old.meta.EndpointID == h.meta.EndpointID {
					a.releaseLocked(old)
					delete(a.cache, key)
				}
			}
		}
		if job == nil {
			a.warn("queue_full_or_closed")
			a.releaseLocked(rec)
		}
		a.mu.Unlock()
		if job == nil {
			a.emit(Event{Kind: "request_diagnostic", Identity: c.identity, Metadata: &h.meta, Diagnostic: &Diagnostic{Mode: a.config.Mode, State: "dropped", Reason: "queue_full_or_closed"}})
		}
	}
	a.mu.Lock()
	for _, prev := range c.prior {
		a.releaseLocked(prev)
	}
	c.prior = nil
	c.http = nil
	c.origins = nil
	a.mu.Unlock()
}

func (a *Attempt) releaseLocked(r *record) {
	if r == nil {
		return
	}
	r.refs--
	if r.refs == 0 {
		a.memory -= r.charge
		if a.memory < 0 {
			a.memory = 0
		}
	}
}
func (a *Attempt) fingerprint(data []byte) string {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *Attempt) emit(event Event) {
	if a.sink != nil {
		func() { defer func() { _ = recover() }(); a.sink(event) }()
	}
}

func (a *Attempt) Close() {
	if a == nil {
		return
	}
	a.CloseWithin(a.config.CloseTimeout)
}
func (a *Attempt) CloseWithin(timeout time.Duration) {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		for key, rec := range a.cache {
			a.releaseLocked(rec)
			delete(a.cache, key)
		}
		if a.queue != nil {
			close(a.queue)
		}
		a.mu.Unlock()
	})
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-a.done:
	case <-timer.C:
		a.mu.Lock()
		a.abandon = true
		a.mu.Unlock()
		a.warn("close_timeout")
	}
}

func (a *Attempt) warn(reason string) {
	a.warnOnce.Do(func() {
		log.Print("请求诊断记录不完整（" + reason + "），任务继续按原规则执行。")
	})
}

func readBuildInfo() BuildInfo {
	info, _ := debug.ReadBuildInfo()
	return selectedBuildInfo(info)
}

func selectedBuildInfo(info *debug.BuildInfo) BuildInfo {
	var result BuildInfo
	if info != nil {
		result.GoVersion = info.GoVersion
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				result.VCSRevision = setting.Value
			case "vcs.modified":
				if v, err := strconv.ParseBool(setting.Value); err == nil {
					result.VCSModified = &v
				}
			}
		}
		for _, dep := range info.Deps {
			if dep != nil && (dep.Path == "github.com/openai/openai-go/v3" || dep.Path == "github.com/anthropics/anthropic-sdk-go") {
				if result.SDKVersions == nil {
					result.SDKVersions = map[string]string{}
				}
				result.SDKVersions[dep.Path] = dep.Version
			}
		}
	}
	return result
}
