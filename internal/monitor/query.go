package monitor

import (
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

// TaskQuery 只筛选已有投影，不重新读取记录或改变任务状态。
type TaskQuery struct {
	Workspace     string
	Status        string
	Q             string
	ActivityGroup string
	HasAlerts     *bool
	Sort          string
}

func taskActivityGroups(t Task) []string {
	groups := []string{}
	switch t.Activity {
	case "active", "finalizing":
		groups = append(groups, "running")
		// Attempts 已由索引按新到旧排序；历史尝试的审批不能代表当前等待。
		for _, a := range t.Attempts {
			if a.Legacy {
				continue
			}
			for _, activity := range a.Activities {
				if activity.Kind == "approval" {
					groups = append(groups, "approval")
					return groups
				}
			}
			break
		}
	case "unknown":
		groups = append(groups, "unknown")
	case "returned":
		groups = append(groups, "returned")
	case "result_recorded":
		groups = append(groups, "recorded")
	}
	return groups
}

func taskSummary(t Task) TaskSummary {
	return TaskSummary{
		ID: t.ID, WorkspaceID: t.WorkspaceID, TaskID: t.TaskID, Goal: t.Goal,
		Mode: t.Mode, Kind: t.Kind, Activity: t.Activity, ActivityGroups: taskActivityGroups(t),
		EngineStatus: t.EngineStatus, StopCode: t.StopCode, RecordQuality: t.RecordQuality,
		LastObservedAt: t.LastObservedAt, Budget: t.Budget, Usage: t.Usage, AlertCount: len(t.Alerts),
	}
}

func recentTaskLess(a, b Task) bool {
	if a.LastObservedAt == nil && b.LastObservedAt != nil {
		return false
	}
	if a.LastObservedAt != nil && b.LastObservedAt == nil {
		return true
	}
	if a.LastObservedAt != nil && !a.LastObservedAt.Equal(*b.LastObservedAt) {
		return a.LastObservedAt.After(*b.LastObservedAt)
	}
	return a.ID < b.ID
}

func sortTasks(items []Task, order string) {
	sort.Slice(items, func(i, j int) bool {
		if order == "alerts" && len(items[i].Alerts) != len(items[j].Alerts) {
			return len(items[i].Alerts) > len(items[j].Alerts)
		}
		return recentTaskLess(items[i], items[j])
	})
}

func (x *Index) queryTasksLocked(query TaskQuery) []Task {
	items := []Task{}
	q := strings.ToLower(query.Q)
	for _, t := range x.tasks {
		if query.Workspace != "" && query.Workspace != t.WorkspaceID ||
			query.Status != "" && query.Status != t.EngineStatus ||
			q != "" && !strings.Contains(strings.ToLower(t.Goal+" "+t.TaskID), q) ||
			query.HasAlerts != nil && *query.HasAlerts != (len(t.Alerts) > 0) {
			continue
		}
		if query.ActivityGroup != "" {
			matches := false
			for _, group := range taskActivityGroups(t) {
				matches = matches || group == query.ActivityGroup
			}
			if !matches {
				continue
			}
		}
		items = append(items, t)
	}
	sortTasks(items, query.Sort)
	return items
}

func (x *Index) QueryTasks(query TaskQuery) []Task {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.queryTasksLocked(query)
}

// Overview 在同一索引快照中生成全量计数、限量列表与工作区健康状态。
func (x *Index) Overview(workspace string) Overview {
	x.mu.RLock()
	defer x.mu.RUnlock()
	result := Overview{Revision: x.revision, Recent: []TaskSummary{}, Attention: []TaskSummary{}, Workspaces: []Workspace{}}
	for _, w := range x.workspaces {
		if workspace == "" || w.ID == workspace {
			w.Path = observe.Redact(w.Path)
			result.Workspaces = append(result.Workspaces, w)
		}
	}
	sort.Slice(result.Workspaces, func(i, j int) bool { return result.Workspaces[i].Path < result.Workspaces[j].Path })
	tasks := x.queryTasksLocked(TaskQuery{Workspace: workspace})
	result.Total = len(tasks)
	attention := []Task{}
	for _, t := range tasks {
		for _, group := range taskActivityGroups(t) {
			switch group {
			case "running":
				result.Counts.Running++
			case "approval":
				result.Counts.Approval++
			case "unknown":
				result.Counts.Unknown++
			}
		}
		if len(t.Alerts) > 0 {
			result.Counts.Alerts++
			attention = append(attention, t)
		}
	}
	for _, t := range tasks[:min(6, len(tasks))] {
		result.Recent = append(result.Recent, taskSummary(t))
	}
	sortTasks(attention, "alerts")
	for _, t := range attention[:min(6, len(attention))] {
		result.Attention = append(result.Attention, taskSummary(t))
	}
	return result
}

func alertCategory(code string) string {
	switch {
	case strings.Contains(code, "budget") || strings.Contains(code, "limit"):
		return "budget"
	case strings.Contains(code, "effect") || strings.Contains(code, "pending"):
		return "unknown"
	case strings.Contains(code, "repeat"):
		return "repeat"
	case strings.Contains(code, "slow") || strings.Contains(code, "long"):
		return "slow"
	case strings.Contains(code, "activity") || strings.Contains(code, "stale") || strings.Contains(code, "heartbeat"):
		return "activity"
	default:
		return "record"
	}
}

// Alerts 按任务分页，total 与 alert_total 分别统计匹配任务和匹配提示。
func (x *Index) Alerts(workspace, q, category string, page, size int) AlertPage {
	x.mu.RLock()
	defer x.mu.RUnlock()
	result := AlertPage{Items: []AlertGroup{}, Page: page, PageSize: size}
	start := (page - 1) * size
	for _, t := range x.queryTasksLocked(TaskQuery{Workspace: workspace, Q: q}) {
		matches := []CategorizedAlert{}
		for _, alert := range t.Alerts {
			kind := alertCategory(alert.Code)
			if category == "" || category == kind {
				matches = append(matches, CategorizedAlert{Alert: alert, Category: kind})
			}
		}
		if len(matches) == 0 {
			continue
		}
		if result.Total >= start && len(result.Items) < size {
			result.Items = append(result.Items, AlertGroup{Task: taskSummary(t), Alerts: matches})
		}
		result.Total++
		result.AlertTotal += len(matches)
	}
	return result
}

func (x *Index) WorkspaceEvaluations(workspace string) []Evaluation {
	x.mu.RLock()
	defer x.mu.RUnlock()
	items := []Evaluation{}
	for _, e := range x.evaluations {
		if workspace == "" || e.WorkspaceID == workspace {
			items = append(items, e)
		}
	}
	return items
}
