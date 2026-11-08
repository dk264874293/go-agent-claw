package monitor

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

//go:embed web/*
var assets embed.FS

type change struct {
	ID   string
	Kind string
}
type Server struct {
	Index       *Index
	host        string
	token       string
	session     string
	instance    string
	mu          sync.Mutex
	sequence    uint64
	ring        []change
	subscribers map[chan change]bool
}

func randomID() string { var b [32]byte; rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func NewServer(index *Index, host string) *Server {
	return &Server{Index: index, host: host, token: randomID(), session: randomID(), instance: randomID()[:16], subscribers: map[chan change]bool{}}
}
func (s *Server) URL() string { return "http://" + s.host + "/#token=" + s.token }
func (s *Server) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	c := change{fmt.Sprintf("%s:%d", s.instance, s.sequence), "change"}
	s.ring = append(s.ring, c)
	if len(s.ring) > 1024 {
		s.ring = s.ring[len(s.ring)-1024:]
	}
	for ch := range s.subscribers {
		select {
		case ch <- c:
		default:
			close(ch)
			delete(s.subscribers, ch)
		}
	}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || r.Host != s.host {
		fail(w, 403, "仅允许本机登记地址访问")
		return
	}
	origin := r.Header.Get("Origin")
	if strings.HasPrefix(r.URL.Path, "/api/") && (origin != "" && origin != "http://"+s.host || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
		fail(w, 403, "拒绝跨源访问")
		return
	}
	if r.URL.Path == "/api/v1/session" {
		if r.Method != "POST" {
			fail(w, 405, "请求方法无效")
			return
		}
		if origin != "http://"+s.host {
			fail(w, 403, "会话兑换要求同源请求")
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body) != nil || subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.token)) != 1 {
			fail(w, 401, "访问凭据无效")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "claw_monitor", Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		jsonOut(w, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(w, 405, "请求方法无效")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name != "index.html" && name != "app.js" && name != "style.css" {
			http.NotFound(w, r)
			return
		}
		sub, _ := fs.Sub(assets, "web")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
		return
	}
	cookie, e := r.Cookie("claw_monitor")
	if e != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.session)) != 1 {
		fail(w, 401, "请使用启动地址登录观测台")
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "观测台只提供读取接口")
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case p == "workspaces":
		jsonOut(w, map[string]any{"items": s.Index.Workspaces()})
	case p == "overview":
		jsonOut(w, s.Index.Overview(r.URL.Query().Get("workspace")))
	case p == "tasks":
		page := intParam(r, "page", 1, 1, 1000000)
		size := intParam(r, "page_size", 50, 1, 200)
		query := r.URL.Query()
		var hasAlerts *bool
		if raw := query.Get("has_alerts"); raw == "true" || raw == "false" {
			value := raw == "true"
			hasAlerts = &value
		}
		items := s.Index.QueryTasks(TaskQuery{Workspace: query.Get("workspace"), Status: query.Get("status"), Q: query.Get("q"), ActivityGroup: query.Get("activity_group"), HasAlerts: hasAlerts, Sort: query.Get("sort")})
		start := min((page-1)*size, len(items))
		var pageItems any = items[start:min(start+size, len(items))]
		if query.Get("view") == "summary" {
			summaries := []TaskSummary{}
			for _, item := range items[start:min(start+size, len(items))] {
				summaries = append(summaries, taskSummary(item))
			}
			pageItems = summaries
		}
		jsonOut(w, map[string]any{"items": pageItems, "total": len(items), "page": page, "page_size": size})
	case p == "alerts":
		query := r.URL.Query()
		jsonOut(w, s.Index.Alerts(query.Get("workspace"), query.Get("q"), query.Get("category"), intParam(r, "page", 1, 1, 1000000), intParam(r, "page_size", 50, 1, 200)))
	case strings.HasPrefix(p, "tasks/"):
		id := strings.TrimPrefix(p, "tasks/")
		t, ok := s.Index.Task(id)
		if !ok {
			fail(w, 404, "任务不存在")
			return
		}
		jsonOut(w, t)
	case strings.HasPrefix(p, "attempts/") && strings.HasSuffix(p, "/events"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "attempts/"), "/events")
		a, ok := s.Index.Attempt(id)
		if !ok {
			fail(w, 404, "运行尝试不存在")
			return
		}
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		limit := intParam(r, "limit", 200, 1, 200)
		items := []Event{}
		more := false
		next := after
		for _, ev := range a.Events {
			if ev.Sequence <= after {
				continue
			}
			if len(items) >= limit {
				more = true
				break
			}
			items = append(items, ev)
			next = ev.Sequence
		}
		jsonOut(w, map[string]any{"items": items, "next_after": next, "has_more": more})
	case strings.HasPrefix(p, "evidence/"):
		id := strings.TrimPrefix(p, "evidence/")
		s.Index.mu.RLock()
		src, ok := s.Index.evidence[id]
		if ok {
			ok = s.Index.evidenceVisible(src)
		}
		s.Index.mu.RUnlock()
		if !ok {
			fail(w, 404, "证据不存在")
			return
		}
		offset := intParam(r, "offset", 0, 0, 1<<30)
		limit := intParam(r, "limit", 16384, 1, 16384)
		// 先在有界全文上脱敏，再按脱敏内容分页，避免凭据被分页边界拆开而漏检。
		raw, err := safeRead(src.root, src.relative, (1<<20)+1)
		if err != nil {
			fail(w, 404, "证据无法读取或超出登记范围")
			return
		}
		truncated := len(raw) > 1<<20
		if src.expectedVersion != "" {
			if truncated {
				fail(w, 413, "产物超过有界查看上限，无法核对完整版本")
				return
			}
			digest := sha256.Sum256(raw)
			if hex.EncodeToString(digest[:]) != src.expectedVersion {
				fail(w, 409, "产物已经变化，当前内容不再对应写入回执")
				return
			}
		}
		if truncated {
			raw = raw[:1<<20]
		}
		text := strings.ToValidUTF8(string(raw), "�")
		if truncated {
			text = observe.RedactPrefix(text)
		} else {
			text = observe.Redact(text)
		}
		start := min(offset, len(text))
		for start > 0 && start < len(text) && !utf8.RuneStart(text[start]) {
			start--
		}
		end := min(start+limit, len(text))
		for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == start && start < len(text) {
			fail(w, 400, "读取上限不足以容纳一个完整字符，请增加 limit")
			return
		}
		jsonOut(w, map[string]any{"id": id, "text": text[start:end], "next_offset": end, "has_more": end < len(text), "truncated": truncated})
	case p == "evaluations":
		jsonOut(w, map[string]any{"items": s.Index.WorkspaceEvaluations(r.URL.Query().Get("workspace"))})
	case p == "stream":
		s.stream(w, r)
	default:
		fail(w, 404, "接口不存在")
	}
}
func intParam(r *http.Request, key string, def, low, high int) int {
	v, e := strconv.Atoi(r.URL.Query().Get(key))
	if e != nil {
		return def
	}
	return max(low, min(high, v))
}
func jsonOut(w http.ResponseWriter, v any) {
	// 所有出站字符串统一脱敏，覆盖历史版本、评测数组和效果路径等边缘字段。
	// 只处理 JSON 值，不改对象键；UseNumber 保留序号和计数精度。
	raw, err := json.Marshal(v)
	if err != nil {
		fail(w, 500, "响应内容无法编码")
		return
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		fail(w, 500, "响应内容无法编码")
		return
	}
	redact := observe.NewRedactor()
	var clean func(any) any
	clean = func(item any) any {
		switch item := item.(type) {
		case string:
			return redact(item)
		case []any:
			for i := range item {
				item[i] = clean(item[i])
			}
		case map[string]any:
			for key, val := range item {
				item[key] = clean(val)
			}
		}
		return item
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(clean(value))
}
func fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	_, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "当前连接不支持事件流")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan change, 16)
	s.mu.Lock()
	last := r.Header.Get("Last-Event-ID")
	pending := []change{}
	found := false
	if last != "" {
		for i, c := range s.ring {
			if c.ID == last {
				found = true
				pending = append(pending, s.ring[i+1:]...)
				break
			}
		}
	}
	if !found {
		pending = []change{{fmt.Sprintf("%s:%d", s.instance, s.sequence), "reset"}}
	}
	s.subscribers[ch] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }()
	controller := http.NewResponseController(w)
	send := func(c change) error {
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, e := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: {}\n\n", c.ID, c.Kind)
		if e != nil {
			return e
		}
		return controller.Flush()
	}
	// 游标恰好位于最新变化时也立即建立连接，不能等十五秒的心跳才发响应头。
	if len(pending) == 0 {
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if controller.Flush() != nil {
			return
		}
	}
	for _, c := range pending {
		if send(c) != nil {
			return
		}
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case c, open := <-ch:
			if !open || send(c) != nil {
				return
			}
		case <-tick.C:
			controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, e := io.WriteString(w, ": keepalive\n\n"); e != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
