package monitor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func (x *Index) scanObservations(tasks map[string]Task, attempts map[string]Attempt) {
	dirs, e := readDir(x.config.ObserveDir, ".")
	if e != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() || !validID(d.Name()) {
			continue
		}
		s := x.observations[d.Name()]
		if s == nil {
			s = &observationSource{}
			x.observations[d.Name()] = s
		}
		s.poll(x.config.ObserveDir, filepath.Join(d.Name(), "events.jsonl"), d.Name())
		if len(s.events) == 0 {
			continue
		}
		wid, tid := "", ""
		for _, ev := range s.events {
			if ev.WorkspaceID != "" {
				wid = ev.WorkspaceID
			}
			if ev.TaskID != "" {
				tid = ev.TaskID
			}
		}
		if _, ok := x.workspaces[wid]; !ok {
			continue
		}
		id := stableID(wid + "/" + tid)
		if tid == "" {
			id = "attempt-" + d.Name()
		}
		t, exists := tasks[id]
		if !exists {
			t = Task{ID: id, TaskID: tid, WorkspaceID: wid, RecordQuality: "unconfirmed_end", Activity: "unknown", Alerts: []Alert{}, Evidence: []Evidence{}}
		}
		a := Attempt{ID: d.Name(), Activity: "unknown", RecordQuality: "normal", Events: s.events, Activities: []Activity{}}
		active := map[string]Activity{}
		models := map[string]bool{}
		usage := map[string]map[string]any{}
		returned := false
		var configured *Budget
		for _, ev := range s.events {
			p := ev.Payload
			if ev.RecordedAt != nil {
				a.LastObservedAt = ev.RecordedAt
			}
			if ev.EventType == "run_attempt_started" {
				a.StartedAt = ev.RecordedAt
				a.Entry = textField(p, "entry")
				if t.Goal == "" {
					t.Goal = textField(p, "goal")
					t.Mode = textField(p, "mode")
					t.Kind = textField(p, "kind")
				}
				if t.Budget.MaxModelCalls == 0 {
					configured = &Budget{int(number(p, "max_model_calls")), int(number(p, "max_tool_calls")), int(number(p, "max_turns")), int64(number(p, "timeout_ms"))}
				}
			}
			key := ""
			kind := ""
			switch ev.EventType {
			case "run_configured":
				configured = &Budget{int(number(p, "max_model_calls")), int(number(p, "max_tool_calls")), int(number(p, "max_turns")), int64(number(p, "timeout_ms"))}
			case "model_started":
				key = "model:" + ev.ModelCallID
				kind = "model"
				models[ev.ModelCallID] = true
			case "tool_handler_started":
				key = "tool:" + ev.ActionID
				kind = "tool"
			case "approval_waiting":
				key = "approval:" + ev.ActionID
				kind = "approval"
			case "agent_queued":
				key = "agent:" + ev.AgentID
				kind = "queued"
			case "agent_started":
				delete(active, "agent:"+ev.AgentID)
			case "model_finished":
				delete(active, "model:"+ev.ModelCallID)
			case "tool_finished", "tool_result":
				delete(active, "tool:"+ev.ActionID)
			case "approval_resolved":
				delete(active, "approval:"+ev.ActionID)
			case "agent_finished":
				delete(active, "agent:"+ev.AgentID)
			case "model_usage":
				if ev.ModelCallID != "" {
					models[ev.ModelCallID] = true
					usage[ev.ModelCallID] = p
				}
			case "run_finalized":
				a.Activity = "finalizing"
			case "run_returned":
				returned = true
				a.Activity = "returned"
				a.Status = textField(p, "status")
				a.StopCode = textField(p, "stop_code")
				a.ResultRunID = textField(p, "result_run_id")
				active = map[string]Activity{}
			}
			if key != "" && !returned {
				active[key] = Activity{Kind: kind, AgentID: ev.AgentID, ID: strings.SplitN(key, ":", 2)[1], StartedAt: ev.RecordedAt}
			}
			if number(p, "dropped") > 0 || p["incomplete"] == true || ev.EventType == "gap" || ev.EventType == "observation_gap" {
				s.gap = true
			}
			if artifact := textField(p, "artifact_id"); validID(artifact) && tid != "" {
				w := x.workspaces[wid]
				ref := x.addEvidence(w.Path, ".claw/tasks/"+tid+"/artifacts/"+artifact, "工具证据 "+artifact)
				if !containsEvidence(t.Evidence, ref.ID) {
					t.Evidence = append(t.Evidence, ref)
				}
			}
		}
		for _, v := range active {
			a.Activities = append(a.Activities, v)
		}
		sort.Slice(a.Activities, func(i, j int) bool {
			return a.Activities[i].Kind+a.Activities[i].ID < a.Activities[j].Kind+a.Activities[j].ID
		})
		if !returned {
			if len(a.Activities) > 0 {
				a.Activity = "active"
			} else if a.Activity != "finalizing" {
				a.Activity = "unknown"
			}
			a.RecordQuality = "unconfirmed_end"
		}
		if s.gap {
			a.RecordQuality = "incomplete"
		}
		if s.err != "" {
			a.RecordQuality = "corrupt"
			t.Alerts = append(t.Alerts, Alert{Code: "observation_corrupt", Message: s.err, AttemptID: a.ID})
		}
		for mid := range models {
			p := usage[mid]
			in, iok := p["input_tokens"].(float64)
			out, ook := p["output_tokens"].(float64)
			if iok && ook {
				a.Usage.KnownInput += int64(in)
				a.Usage.KnownOutput += int64(out)
			} else {
				a.Usage.UnknownTokens++
			}
			cost, cok := p["cost"].(float64)
			currency := textField(p, "currency")
			if cok && currency != "" && (a.Usage.Currency == "" || a.Usage.Currency == currency) {
				a.Usage.KnownCost += cost
				a.Usage.Currency = currency
			} else {
				a.Usage.UnknownCost++
			}
		}
		if len(models) > 0 && a.Usage.UnknownTokens == 0 && !s.gap && s.err == "" {
			in, out := a.Usage.KnownInput, a.Usage.KnownOutput
			a.Usage.InputTokens = &in
			a.Usage.OutputTokens = &out
		}
		if len(models) > 0 && a.Usage.UnknownCost == 0 && !s.gap && s.err == "" {
			cost := a.Usage.KnownCost
			a.Usage.Cost = &cost
		}
		// 匹配新观测的历史分段只保留一个入口，不把同次动作展示两遍。
		filtered := t.Attempts[:0]
		for _, old := range t.Attempts {
			duplicate := false
			if old.Legacy && !duplicate && len(old.Events) > 0 {
				for _, ev := range s.events {
					if ev.EventType == "task_event_committed" && ev.SourceTaskSequence == old.Events[0].SourceTaskSequence {
						duplicate = true
						break
					}
				}
			}
			if !duplicate {
				filtered = append(filtered, old)
			}
		}
		t.Attempts = append(filtered, a)
		attempts[a.ID] = a
		if t.LastObservedAt == nil || (a.LastObservedAt != nil && a.LastObservedAt.After(*t.LastObservedAt)) {
			t.LastObservedAt = a.LastObservedAt
		}
		latestAt := a.StartedAt
		if latestAt == nil {
			latestAt = a.LastObservedAt
		}
		if t.latestAttemptAt == nil || (latestAt != nil && latestAt.After(*t.latestAttemptAt)) {
			t.latestAttemptAt = latestAt
			if configured != nil && (x.sources[id] == nil || x.sources[id].view.Sequence == 0) {
				t.Budget = *configured
			}

			t.Activity = a.Activity
			if t.RecordQuality != "corrupt" {
				t.RecordQuality = a.RecordQuality
			}
			if returned && t.EngineStatus != "" && (t.EngineStatus != a.Status || t.StopCode != a.StopCode) {
				t.FinalText = ""
				t.Alerts = append(t.Alerts, Alert{Code: "result_mismatch", Message: "入口返回观察与历史持久结果不同，请分别核对。", AttemptID: a.ID})
			}
		}
		tasks[id] = t
	}
}
func containsEvidence(es []Evidence, id string) bool {
	for _, e := range es {
		if e.ID == id {
			return true
		}
	}
	return false
}
func (x *Index) detect(t *Task) {
	add := func(code, message string) { t.Alerts = append(t.Alerts, Alert{Code: code, Message: message}) }
	if t.Budget.MaxModelCalls > 0 && t.Budget.MaxModelCalls-t.Usage.ModelCalls <= x.config.ModelRemaining {
		add("model_budget", "模型调用额度接近上限或已耗尽")
	}
	if t.Budget.MaxToolCalls > 0 && t.Budget.MaxToolCalls-t.Usage.ToolCalls <= x.config.ToolRemaining {
		add("tool_budget", "工具调用额度接近上限或已耗尽")
	}
	for _, e := range t.Effects {
		if e.Effect == "UNKNOWN" {
			add("unknown_effect", "动作 "+e.ActionID+" 的效果未知，须核实后再决定后续动作")
		}
	}
	if !t.HistoryComplete {
		add("history_incomplete", "历史效果记录不完整")
	}
	now := time.Now()
	for _, a := range t.Attempts {
		if a.Legacy {
			continue
		}
		if a.RecordQuality == "incomplete" || a.RecordQuality == "corrupt" {
			t.Alerts = append(t.Alerts, Alert{Code: "observation_gap", Message: "观测记录存在缺口，不能据此认定没有发生其他动作", AttemptID: a.ID})
		}
		if a.Activity != "returned" && a.LastObservedAt != nil && now.Sub(*a.LastObservedAt) > x.config.StaleAfter {
			t.Alerts = append(t.Alerts, Alert{Code: "activity_unknown", Message: "活动信号缺失，执行状态未知，不能据此判定崩溃", AttemptID: a.ID})
			if t.LastObservedAt != nil && a.LastObservedAt.Equal(*t.LastObservedAt) {
				t.Activity = "unknown"
			}
		}
		for _, v := range a.Activities {
			threshold := x.config.ModelSlow
			if v.Kind == "tool" {
				threshold = x.config.ToolSlow
			} else if v.Kind != "model" {
				continue
			}
			if v.StartedAt != nil && now.Sub(*v.StartedAt) > threshold {
				t.Alerts = append(t.Alerts, Alert{Code: "slow_call", Message: fmt.Sprintf("%s 调用耗时偏长", v.Kind), AttemptID: a.ID})
			}
		}
		type repetition struct {
			digest string
			count  int
		}
		rep := map[string]repetition{}
		requests := map[string]string{}
		for _, ev := range a.Events {
			p := ev.Payload
			tool := textField(p, "tool")
			if ev.ActionID != "" && textField(p, "request_digest") != "" {
				requests[ev.ActionID] = textField(p, "request_digest")
			}
			if ev.EventType != "tool_finished" && ev.EventType != "tool_result" {
				continue
			}
			if tool != "read_file" && tool != "search_text" && tool != "list_dir" {
				rep[ev.AgentID] = repetition{}
				continue
			}
			req := requests[ev.ActionID]
			result := textField(p, "result_digest")
			if req == "" || result == "" {
				continue
			}
			digest := tool + req + result
			r := rep[ev.AgentID]
			if r.digest == digest {
				r.count++
			} else {
				r = repetition{digest, 1}
			}
			rep[ev.AgentID] = r
			if r.count == x.config.RepeatCount {
				t.Alerts = append(t.Alerts, Alert{Code: "repeated_exploration", Message: "连续相同只读请求取得相同结果，可能存在重复探索", AttemptID: a.ID, Sequence: ev.Sequence})
			}
		}
	}
}

// 页面仅接收经过脱敏的受限 DTO；不向客户端公开数据源路径映射。
func (x *Index) Workspaces() []Workspace {
	x.mu.RLock()
	defer x.mu.RUnlock()
	items := []Workspace{}
	for _, w := range x.workspaces {
		w.Path = observe.Redact(w.Path)
		items = append(items, w)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items
}
func (x *Index) Tasks(workspace, status, q string) []Task {
	x.mu.RLock()
	defer x.mu.RUnlock()
	items := []Task{}
	q = strings.ToLower(q)
	for _, t := range x.tasks {
		if workspace != "" && workspace != t.WorkspaceID || status != "" && status != t.EngineStatus || q != "" && !strings.Contains(strings.ToLower(t.Goal+" "+t.TaskID), q) {
			continue
		}
		items = append(items, t)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.LastObservedAt != nil && b.LastObservedAt != nil && !a.LastObservedAt.Equal(*b.LastObservedAt) {
			return a.LastObservedAt.After(*b.LastObservedAt)
		}
		if a.LastObservedAt != nil && b.LastObservedAt == nil {
			return true
		}
		return a.ID < b.ID
	})
	return items
}
func (x *Index) Task(id string) (Task, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	v, ok := x.tasks[id]
	return v, ok
}
func (x *Index) Attempt(id string) (Attempt, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	v, ok := x.attempts[id]
	return v, ok
}
func (x *Index) Evaluations() []Evaluation {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return append([]Evaluation{}, x.evaluations...)
}
