package monitor

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

type taskSource struct {
	reader   *taskstore.ReadOnlyReader
	view     engine.Inspection
	events   []Event
	err      string
	legacyID string
	attempts []Attempt
	lastFile string
}
type observationSource struct {
	anchor      []byte
	workspaceID string
	taskID      string
	info        os.FileInfo
	offset      int64
	sequence    uint64
	events      []Event
	err         string
	gap         bool
}
type evidenceSource struct{ root, relative, label, expectedVersion string }
type Index struct {
	mu           sync.RWMutex
	config       Config
	workspaces   map[string]Workspace
	sources      map[string]*taskSource
	observations map[string]*observationSource
	evidence     map[string]evidenceSource
	tasks        map[string]Task
	attempts     map[string]Attempt
	evaluations  []Evaluation
	health       []string
	revision     uint64
}

func stableID(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:16]) }
func canonical(p string) string {
	p, _ = filepath.Abs(p)
	if real, e := filepath.EvalSymlinks(p); e == nil {
		p = real
	}
	return filepath.Clean(p)
}
func NewIndex(c Config) (*Index, error) {
	c.defaults()
	if c.ObserveDir == "" {
		c.ObserveDir = observe.DefaultDir()
	}
	x := &Index{config: c, workspaces: map[string]Workspace{}, sources: map[string]*taskSource{}, observations: map[string]*observationSource{}, evidence: map[string]evidenceSource{}, tasks: map[string]Task{}, attempts: map[string]Attempt{}}
	for _, p := range c.Workspaces {
		x.addWorkspace(p)
	}
	x.Refresh()
	return x, nil
}
func (x *Index) addWorkspace(p string) string {
	p = canonical(p)
	id := observe.WorkspaceID(p)
	if _, ok := x.workspaces[id]; !ok {
		x.workspaces[id] = Workspace{ID: id, Path: p, Health: "normal"}
	}
	return id
}
func (x *Index) Close() {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, s := range x.sources {
		if s.reader != nil {
			s.reader.Close()
		}
	}
}
func readDir(root, rel string) ([]os.DirEntry, error) {
	r, e := os.OpenRoot(root)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	f, e := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return f.ReadDir(-1)
}
func safeRead(root, rel string, max int64) ([]byte, error) {
	r, e := os.OpenRoot(root)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	f, e := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("证据不是普通文件")
	}
	return io.ReadAll(io.LimitReader(f, max))
}
func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && strings.ToLower(s) == s
}
func (x *Index) addEvidence(root, rel, label string) Evidence {
	id := stableID(root + "\x00" + rel)
	x.evidence[id] = evidenceSource{root: root, relative: rel, label: label}
	return Evidence{id, observe.Redact(label)}
}

// taskArtifactsVisible 以当前持久任务策略限制材料访问，不根据可猜测的文件名分类。
// 未取得有效状态时也不开放材料，避免质量事件提交之前的枚举窗口泄露复核正文。
// 调用方持有索引锁。
func (x *Index) taskArtifactsVisible(root, taskID string) bool {
	source := x.sources[stableID(observe.WorkspaceID(root)+"/"+taskID)]
	return source != nil && source.view.Sequence > 0 && source.view.Spec.Quality == nil
}

func (x *Index) evidenceVisible(src evidenceSource) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(src.relative)), "/")
	if len(parts) == 5 && parts[0] == ".claw" && parts[1] == "tasks" && parts[3] == "artifacts" {
		return x.taskArtifactsVisible(src.root, parts[2]) && x.localCheckArtifactVisible(src.root, parts[2], parts[4])
	}
	return true
}
func (x *Index) Refresh() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	before, _ := json.Marshal(struct {
		T map[string]Task
		E []Evaluation
		W map[string]Workspace
	}{x.tasks, x.evaluations, x.workspaces})
	x.evaluations = nil
	x.health = nil
	x.scanEvaluations()
	tasks := map[string]Task{}
	attempts := map[string]Attempt{}
	for wid, w := range x.workspaces {
		dirs, e := readDir(w.Path, ".claw/tasks")
		if e != nil {
			if !os.IsNotExist(e) {
				w.Health = "error"
				w.Error = "无法读取任务目录"
			} else {
				w.Health = "empty"
				w.Error = ""
			}
			x.workspaces[wid] = w
			continue
		}
		w.Health = "normal"
		w.Error = ""
		x.workspaces[wid] = w
		for _, d := range dirs {
			if !d.IsDir() || !validID(d.Name()) {
				continue
			}
			id := stableID(wid + "/" + d.Name())
			s := x.sources[id]
			if s == nil {
				r, e := taskstore.NewReadOnlyReader(w.Path, d.Name())
				s = &taskSource{reader: r}
				if e != nil {
					s.err = "无法打开任务记录"
				}
				x.sources[id] = s
			}
			if s.reader == nil {
				continue
			}
			es, reset, err := s.reader.Poll()
			if reset {
				s.view = engine.Inspection{}
				s.events = nil
				s.attempts = nil
				s.legacyID = ""
				s.err = ""
			}
			if err != nil {
				s.err = observe.Redact(err.Error())
			}
			for _, ev := range es {
				if e := s.view.Apply(ev); e != nil {
					s.err = e.Error()
					break
				}
				if ev.Kind == "created" {
					continue
				}
				if ev.Kind == "started" || s.legacyID == "" {
					s.legacyID = "legacy-" + stableID(id+fmt.Sprint(ev.Sequence))
					s.attempts = append(s.attempts, Attempt{ID: s.legacyID, Entry: "历史记录", Activity: "unknown", RecordQuality: "historical_incomplete", Legacy: true})
				}
				a := &s.attempts[len(s.attempts)-1]
				if ev.Kind == "elapsed" {
					continue
				}
				p := map[string]any{"source_kind": ev.Kind}
				if act := s.view.Action; act != nil {
					p["tool"] = act.Tool
					p["status"] = act.Result.Status
					p["effect"] = act.Result.Effect
					p["summary"] = clip(observe.Redact(act.Result.Output), 800)
					if act.Tool == "check_evidence" {
						p["summary"] = "局部核对结果已记录；完整输入和回执保存在受限任务材料，仅展示结构化摘要"
					}
					if act.Tool == "spawn_subagent" {
						var result struct {
							Handoff *struct {
								ReportID     string `json:"report_id"`
								Execution    string `json:"execution"`
								Citations    string `json:"citations"`
								Verification string `json:"verification"`
								Coverage     *struct {
									Status string `json:"status"`
								} `json:"coverage"`
							} `json:"handoff"`
						}
						if json.Unmarshal([]byte(act.Result.Output), &result) == nil && result.Handoff != nil {
							h := result.Handoff
							p["report_id"] = observe.Redact(h.ReportID)
							p["handoff_execution"] = observe.Redact(h.Execution)
							p["handoff_citations"] = observe.Redact(h.Citations)
							p["handoff_verification"] = observe.Redact(h.Verification)
							if h.Coverage != nil {
								p["handoff_coverage"] = observe.Redact(h.Coverage.Status)
							}
						}
					}
				}
				oe := Event{SchemaVersion: 1, AttemptID: a.ID, Sequence: ev.Sequence, EventType: ev.Kind, TaskID: d.Name(), WorkspaceID: wid, SourceTaskSequence: ev.Sequence, Payload: p}
				if s.view.Action != nil {
					oe.ActionID = s.view.Action.ID
				}
				a.Events = append(a.Events, oe)
				if ev.Kind == "finished" && s.view.Result != nil {
					a.ResultRunID = s.view.Result.RunID
					a.Status = string(s.view.Result.Status)
					a.StopCode = s.view.Result.StopCode
					a.Activity = "result_recorded"
				}
			}
			v := s.view
			t := Task{ID: id, WorkspaceID: wid, TaskID: d.Name(), Goal: clip(observe.Redact(v.Spec.Goal), 4000), Mode: string(v.Spec.Mode), Kind: string(v.Spec.Kind), Activity: "unknown", RecordQuality: "historical_incomplete", Budget: Budget{v.Limits.MaxModelCalls, v.Limits.MaxToolCalls, v.Limits.MaxTurns, v.Limits.Timeout.Milliseconds()}, Usage: Usage{v.Usage.ModelCalls, v.Usage.ToolCalls, v.Usage.Turns, v.Usage.Elapsed.Milliseconds()}, Plan: append([]engine.InspectionStep(nil), v.Plan...), Attempts: append([]Attempt{}, s.attempts...), Effects: []engine.EffectReceipt{}, Alerts: []Alert{}, Evidence: []Evidence{}}
			t.AnalysisEnabled = v.Spec.Analysis
			t.Delegation = v.Spec.Delegation
			t.ReadPageBytes = v.Spec.ReadPageBytes
			t.ReadContextVersion = v.Spec.ReadContextVersion
			if v.Spec.ReadFeatures != nil {
				copy := *v.Spec.ReadFeatures
				t.ReadFeatures = &copy
			}
			t.OutputStyle = v.Spec.OutputStyle
			if v.Spec.ReadScope != nil {
				copy := *v.Spec.ReadScope
				copy.Paths = append([]string(nil), copy.Paths...)
				for i := range copy.Paths {
					copy.Paths[i] = observe.Redact(copy.Paths[i])
				}
				copy.NameGlob = observe.Redact(copy.NameGlob)
				t.ReadScope = &copy
			}
			if t.ReadPageBytes == 0 {
				t.ReadPageBytes = 8000
			}
			x.projectReadMemory(&t, v.ReadMemory)
			x.projectDeliveryReview(&t, v.DeliveryReview)
			x.projectLocalChecks(&t, v.LocalChecks)
			if q := v.Quality; q != nil {
				x.projectQuality(&t, &engine.QualityDelivery{Version: q.Version, DraftID: q.DraftID, Status: q.Status, CheckResult: q.CheckResult, ReviewStatus: q.ReviewStatus, RevisionStatus: q.RevisionStatus, Unresolved: q.Unresolved})
			}
			if v.Report != nil && v.Spec.Report != nil {
				x.projectReport(&t, &engine.ReportDelivery{
					ReportID: v.Report.ReportID, OriginRunID: v.Report.OriginRunID, Status: v.Report.Status,
					TargetPath: v.Spec.Report.TargetPath, OutputVersion: v.Report.OutputVersion,
					DraftArtifactID: v.Report.DraftArtifactID, RenderedArtifactID: v.Report.RenderedArtifactID,
					ReferenceResult: v.Report.ReferenceResult, SemanticResult: v.Report.SemanticResult, Issues: v.Report.Issues,
				})
			}
			for i := range t.Plan {
				t.Plan[i].Goal = observe.Redact(t.Plan[i].Goal)
			}
			if v.Delivery != nil {
				t.HistoryComplete = v.Delivery.HistoryComplete
				t.Effects = append(t.Effects, v.Delivery.Effects...)
			}
			if v.Result != nil {
				if !v.Running {
					x.projectDeliveryReview(&t, v.Result.DeliveryReview)
				}
				x.projectReport(&t, v.Result.Report)
				x.projectQuality(&t, v.Result.Quality)
				x.projectLocalChecks(&t, v.Result.LocalChecks)
				for _, ref := range v.Result.Artifacts {
					x.projectArtifact(&t, w.Path, ref)
				}
				if v.Result.Reading != nil {
					raw, _ := json.Marshal(v.Result.Reading)
					var reading engine.ReadingCoverage
					if json.Unmarshal(raw, &reading) == nil {
						if reading.ScopeKind == "" {
							reading.ScopeKind = "legacy"
						}
						reading.ManifestID = observe.Redact(reading.ManifestID)
						reading.ScopeID = observe.Redact(reading.ScopeID)
						reading.ScopeRevision = observe.Redact(reading.ScopeRevision)
						for i := range reading.ManifestIDs {
							reading.ManifestIDs[i] = observe.Redact(reading.ManifestIDs[i])
						}
						for i := range reading.UnresolvedPaths {
							reading.UnresolvedPaths[i] = observe.Redact(reading.UnresolvedPaths[i])
						}
						for i := range reading.Files {
							reading.Files[i].Path = observe.Redact(reading.Files[i].Path)
						}
						t.Reading = &reading
					}
				}
				if v.Result.Analysis != nil {
					raw, _ := json.Marshal(v.Result.Analysis)
					var report engine.AnalysisReport
					if json.Unmarshal(raw, &report) == nil {
						for i := range report.Findings {
							f := &report.Findings[i]
							f.Statement = observe.Redact(f.Statement)
							f.Context = observe.Redact(f.Context)
							f.Limitations = observe.Redact(f.Limitations)
							for j := range f.Evidence {
								ref := &f.Evidence[j]
								ref.Quote = observe.Redact(ref.Quote)
								ref.Path = observe.Redact(ref.Path)
							}
						}
						for i := range report.Unverified {
							report.Unverified[i] = observe.Redact(report.Unverified[i])
						}
						for id, text := range report.Summaries {
							report.Summaries[id] = observe.Redact(text)
						}
						t.Analysis = &report
					}
				}
				t.EngineStatus = string(v.Result.Status)
				t.StopCode = v.Result.StopCode
				t.ResultRunID = v.Result.RunID
				t.FinalText = observe.Redact(v.Result.FinalText)
				t.FinalTextDisplay = observe.Redact(engine.DisplayRunFinalText(*v.Result))
				if partial := v.Result.PartialReport; partial != nil {
					copy := *partial
					copy.Text = observe.Redact(copy.Text)
					copy.OriginRunID = observe.Redact(copy.OriginRunID)
					t.PartialReport = &copy
				}
				t.Activity = "result_recorded"
			}
			if v.Verification != nil {
				vv := *v.Verification
				vv.Output = clip(observe.Redact(vv.Output), 2000)
				t.Verification = &vv
			}
			snap, e := safeRead(w.Path, ".claw/tasks/"+d.Name()+"/snapshot.json", 32<<20)
			if e == nil {
				if e = engine.ValidateInspectionSnapshot(snap); e != nil {
					t.Alerts = append(t.Alerts, Alert{Code: "snapshot_corrupt", Message: e.Error()})
					t.RecordQuality = "corrupt"
				}
			}
			if s.err != "" {
				t.RecordQuality = "corrupt"
				t.Alerts = append(t.Alerts, Alert{Code: "record_corrupt", Message: s.err})
			}
			files, _ := readDir(w.Path, ".claw/tasks/"+d.Name()+"/artifacts")
			for _, f := range files {
				if validID(f.Name()) && !f.IsDir() && x.taskArtifactsVisible(w.Path, d.Name()) && x.localCheckArtifactVisible(w.Path, d.Name(), f.Name()) {
					t.Evidence = append(t.Evidence, x.addEvidence(w.Path, ".claw/tasks/"+d.Name()+"/artifacts/"+f.Name(), "工具证据 "+f.Name()))
				}
			}
			for _, a := range t.Attempts {
				attempts[a.ID] = a
			}
			x.attachTraces(&t, w.Path)
			tasks[id] = t
		}
	}
	x.scanObservations(tasks, attempts)
	for id, t := range tasks {
		for _, ev := range x.evaluations {
			if ev.TaskID == t.TaskID && ev.WorkspaceID == t.WorkspaceID {
				t.Evaluations = append(t.Evaluations, ev)
			}
		}
		x.detect(&t)
		sort.SliceStable(t.Attempts, func(i, j int) bool {
			a, b := t.Attempts[i], t.Attempts[j]
			if a.StartedAt != nil && b.StartedAt != nil {
				return a.StartedAt.After(*b.StartedAt)
			}
			if a.StartedAt != nil {
				return true
			}
			if b.StartedAt != nil {
				return false
			}
			if len(a.Events) > 0 && len(b.Events) > 0 {
				return a.Events[0].Sequence > b.Events[0].Sequence
			}
			return a.ID > b.ID
		})
		tasks[id] = t
	}
	x.tasks, x.attempts = tasks, attempts
	after, _ := json.Marshal(struct {
		T map[string]Task
		E []Evaluation
		W map[string]Workspace
	}{x.tasks, x.evaluations, x.workspaces})
	changed := string(before) != string(after)
	if changed {
		x.revision++
	}
	return changed
}
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n] + "…"
}
func (x *Index) scanEvaluations() {
	for _, base := range x.config.EvalRoots {
		base = canonical(base)
		dirs, e := readDir(base, ".")
		if e != nil {
			x.health = append(x.health, "无法读取评测目录")
			continue
		}
		rels := []string{"."}
		for _, d := range dirs {
			if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
				rels = append(rels, d.Name())
			}
		}
		for _, rel := range rels {
			runs, e := readDir(base, filepath.Join(rel, "runs"))
			if e != nil {
				continue
			}
			for _, run := range runs {
				if !run.IsDir() {
					continue
				}
				prefix := filepath.Join(rel, "runs", run.Name())
				raw, e := safeRead(base, filepath.Join(prefix, "result.json"), 1<<20)
				if e != nil {
					continue
				}
				var v Evaluation
				var version struct {
					SchemaVersion int `json:"schema_version"`
				}
				if json.Unmarshal(raw, &v) != nil || json.Unmarshal(raw, &version) != nil || version.SchemaVersion != 2 {
					v.ReadError = "评测协议无效"
				}
				v.ID = stableID(base + "/" + prefix)
				v.Evidence = []Evidence{}
				for _, name := range []string{"verification.txt", "answer.txt", "stderr.txt"} {
					if _, e := safeRead(base, filepath.Join(prefix, name), 1); e == nil {
						v.Evidence = append(v.Evidence, x.addEvidence(base, filepath.Join(prefix, name), name))
					}
				}
				v.WorkspaceID = observe.WorkspaceID(filepath.Join(base, prefix, "workspace"))
				x.evaluations = append(x.evaluations, v)
				candidate := canonical(filepath.Join(base, prefix, "workspace"))
				relative, relErr := filepath.Rel(base, candidate)
				if relErr == nil && filepath.IsLocal(relative) {
					x.addWorkspace(candidate)
				}
			}
		}
	}
	sort.Slice(x.evaluations, func(i, j int) bool { return x.evaluations[i].ID < x.evaluations[j].ID })
}

// pollObservation 同样不修复源文件；观测丢失允许继续展示，但必须保留缺口。
func (s *observationSource) poll(root, rel, attempt string) {
	r, e := os.OpenRoot(root)
	if e != nil {
		s.err = "无法读取观测目录"
		return
	}
	defer r.Close()
	f, e := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		s.err = "无法读取观测流"
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		s.err = "观测流不是普通文件"
		return
	}
	anchorChanged := false
	if s.offset > 0 && len(s.anchor) > 0 {
		check := make([]byte, len(s.anchor))
		n, readErr := f.ReadAt(check, s.offset-int64(len(check)))
		anchorChanged = readErr != nil || n != len(check) || !bytes.Equal(check, s.anchor)
	}
	if s.info != nil && (!os.SameFile(s.info, info) || info.Size() < s.offset || anchorChanged || (info.Size() <= s.offset && !info.ModTime().Equal(s.info.ModTime()))) {
		s.events = nil
		s.offset = 0
		s.sequence = 0
		s.err = ""
		s.gap = false
		s.workspaceID, s.taskID = "", ""
	}
	s.info = info
	reader := bufio.NewReaderSize(io.NewSectionReader(f, s.offset, info.Size()-s.offset), 8193)
	for {
		line, e := reader.ReadSlice('\n')
		if errors.Is(e, io.EOF) {
			return
		}
		if e != nil {
			s.err = "读取观测流失败"
			return
		}
		if len(line) > 8192 {
			s.err = "观测事件过大"
			return
		}
		var typed observe.Event
		if json.Unmarshal(line, &typed) != nil {
			s.err = "观测事件无法解码"
			return
		}
		// 本地文件同样按白名单重新校验；历史或外部写入不能夹带正文及任意参数对象。
		typed.Payload.Request = observe.SanitizeRequestSummary(typed.Payload.Request)
		clean, _ := json.Marshal(typed)
		var ev Event
		if json.Unmarshal(clean, &ev) != nil || ev.RecordedAt == nil || ev.RecordedAt.IsZero() || ev.SchemaVersion != 1 || ev.AttemptID != attempt || ev.Sequence <= s.sequence || ev.EventType == "" || (ev.TaskID != "" && !validID(ev.TaskID)) {
			s.err = "观测事件格式或顺序损坏"
			return
		}
		if !validID(ev.WorkspaceID) || (s.workspaceID != "" && s.workspaceID != ev.WorkspaceID) || (ev.TaskID != "" && s.taskID != "" && ev.TaskID != s.taskID) {
			s.err = "观测流身份发生冲突"
			return
		}
		s.workspaceID = ev.WorkspaceID
		if ev.TaskID != "" {
			s.taskID = ev.TaskID
		}
		if ev.EventType == "model_usage" {
			for _, key := range []string{"input_tokens", "output_tokens", "cost", "input_per_million", "output_per_million"} {
				if n, ok := ev.Payload[key].(float64); ok && n < 0 {
					ev.Payload[key] = nil
					s.gap = true
				}
			}
		}
		if ev.Sequence != s.sequence+1 {
			s.gap = true
		}
		sanitizeMap(ev.Payload)
		s.sequence = ev.Sequence
		s.offset += int64(len(line))
		count := min(int64(128), s.offset)
		s.anchor = make([]byte, count)
		if _, e := f.ReadAt(s.anchor, s.offset-count); e != nil {
			s.err = "观测流读取期间发生变化"
			return
		}
		s.events = append(s.events, ev)
	}
}
func sanitizeMap(m map[string]any) {
	for k, v := range m {
		switch v := v.(type) {
		case string:
			m[k] = observe.Redact(v)
		case map[string]any:
			sanitizeMap(v)
		case []any:
			for i, item := range v {
				switch item := item.(type) {
				case string:
					v[i] = observe.Redact(item)
				case map[string]any:
					sanitizeMap(item)
				}
			}
		}
	}
}
func textField(p map[string]any, k string) string { s, _ := p[k].(string); return s }
func number(p map[string]any, k string) float64   { n, _ := p[k].(float64); return n }

func (x *Index) attachTraces(t *Task, work string) {
	files, _ := readDir(work, ".claw/traces")
	runIDs := map[string]bool{}
	for _, a := range t.Attempts {
		if a.ResultRunID != "" {
			runIDs[a.ResultRunID] = true
		}
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		matched := ""
		for run := range runIDs {
			if f.Name() == "trace_"+run+".json" || strings.HasPrefix(f.Name(), "trace_"+run+"_") {
				matched = run
				break
			}
		}
		if matched == "" {
			continue
		}
		raw, e := safeRead(work, ".claw/traces/"+f.Name(), 4<<20)
		if e != nil {
			continue
		}
		var span observability.Span
		if json.Unmarshal(raw, &span) != nil {
			continue
		}
		var walk func(*observability.Span, int)
		walk = func(s *observability.Span, depth int) {
			if depth > 20 || len(t.Traces) >= 2000 {
				return
			}
			t.Traces = append(t.Traces, TraceSpan{observe.Redact(s.Name), s.StartTime, s.EndTime, s.DurationMs, depth, matched})
			for _, child := range s.Children {
				if child != nil {
					walk(child, depth+1)
				}
			}
		}
		walk(&span, 0)
	}
}
