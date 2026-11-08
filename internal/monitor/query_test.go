package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func queryFixtureIndex() *Index {
	x := &Index{revision: 42, tasks: map[string]Task{}, workspaces: map[string]Workspace{
		"w1": {ID: "w1", Path: "/第一个工作区", Health: "normal"},
		"w2": {ID: "w2", Path: "/第二个工作区", Health: "error", Error: "无法读取任务目录"},
	}}
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 205; i++ {
		at := base.Add(-time.Duration(i) * time.Minute)
		id := fmt.Sprintf("任务-%03d", i)
		activity := "result_recorded"
		switch {
		case i < 100:
			activity = "active"
		case i < 120:
			activity = "finalizing"
		case i < 160:
			activity = "returned"
		case i < 200:
			activity = "unknown"
		}
		latest := Attempt{ID: "最新尝试", Activity: activity}
		if i < 10 {
			latest.Activities = []Activity{{Kind: "approval", ID: "当前审批"}}
		}
		task := Task{ID: id, TaskID: "原始-" + id, WorkspaceID: "w1", Goal: "检查 API " + id,
			Activity: activity, EngineStatus: "NEEDS_VERIFICATION", LastObservedAt: &at,
			FinalText: "不应进入轻量列表的答复", Evidence: []Evidence{{ID: "证据正文引用"}},
			Attempts: []Attempt{latest, {ID: "旧尝试", Activities: []Activity{{Kind: "approval"}}}},
		}
		if i%5 == 0 {
			for n := 0; n < i%3+1; n++ {
				task.Alerts = append(task.Alerts, Alert{Code: "model_budget", Message: "预算提示", AttemptID: "尝试", Sequence: uint64(n + 1)})
			}
		}
		x.tasks[id] = task
	}
	x.tasks["其他工作区任务"] = Task{ID: "其他工作区任务", WorkspaceID: "w2", Activity: "active", Alerts: []Alert{{Code: "record_corrupt"}}}
	return x
}

func TestOverviewCountsCoverAllTasksAndRespectWorkspace(t *testing.T) {
	x := queryFixtureIndex()
	view := x.Overview("w1")
	if view.Revision != 42 || view.Total != 205 || view.Counts != (OverviewCounts{Running: 120, Approval: 10, Alerts: 41, Unknown: 40}) {
		t.Fatalf("全量概览统计错误: %+v", view)
	}
	if len(view.Recent) != 6 || view.Recent[0].ID != "任务-000" || view.Recent[5].ID != "任务-005" {
		t.Fatalf("最近任务没有按观察时间限量: %+v", view.Recent)
	}
	if len(view.Attention) != 6 || view.Attention[0].ID != "任务-005" || view.Attention[1].ID != "任务-020" || view.Attention[0].AlertCount != 3 {
		t.Fatalf("提示任务没有按提示数后时间排序: %+v", view.Attention)
	}
	if len(view.Workspaces) != 1 || view.Workspaces[0].ID != "w1" {
		t.Fatal("工作区健康状态未随筛选更新")
	}
	all := x.Overview("")
	if all.Total != 206 || all.Counts.Running != 121 || all.Counts.Alerts != 42 || len(all.Workspaces) != 2 {
		t.Fatalf("跨工作区概览丢失任务: %+v", all)
	}
	empty := x.Overview("不存在")
	raw, _ := json.Marshal(empty)
	if empty.Total != 0 || strings.Contains(string(raw), "null") {
		t.Fatalf("空概览应提供空数组: %s", raw)
	}
}

func TestQueryActivitiesDoNotUseHistoricalApproval(t *testing.T) {
	x := queryFixtureIndex()
	for _, tc := range []struct {
		group string
		count int
	}{{"running", 120}, {"approval", 10}, {"unknown", 40}, {"returned", 40}, {"recorded", 5}, {"不存在", 0}} {
		items := x.QueryTasks(TaskQuery{Workspace: "w1", ActivityGroup: tc.group})
		if len(items) != tc.count {
			t.Errorf("活动 %s 数量错误: %d", tc.group, len(items))
		}
	}
	for _, task := range []Task{
		{Activity: "returned", Attempts: []Attempt{{Activities: []Activity{{Kind: "approval"}}}}},
		{Activity: "active", Attempts: []Attempt{{Legacy: true, Activities: []Activity{{Kind: "approval"}}}, {Activities: nil}}},
		{Activity: "active", Attempts: []Attempt{{Activities: nil}, {Activities: []Activity{{Kind: "approval"}}}}},
	} {
		for _, group := range taskActivityGroups(task) {
			if group == "approval" {
				t.Errorf("历史或已返回审批错误计入当前等待: %+v", task)
			}
		}
	}
	yes, no := true, false
	if got := x.QueryTasks(TaskQuery{Workspace: "w1", Status: "NEEDS_VERIFICATION", Q: "api 任务-000", ActivityGroup: "approval", HasAlerts: &yes}); len(got) != 1 || got[0].ID != "任务-000" {
		t.Fatalf("组合筛选或不区分大小写搜索错误: %+v", got)
	}
	if got := x.QueryTasks(TaskQuery{Workspace: "w1", Q: "原始-任务-005", HasAlerts: &no}); len(got) != 0 {
		t.Fatal("无提示筛选没有与搜索组合")
	}
}

func TestTaskSortingIsDeterministicWithMissingTimes(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	before := at.Add(-time.Minute)
	x := &Index{tasks: map[string]Task{
		"a": {ID: "a"}, "b": {ID: "b", LastObservedAt: &at}, "c": {ID: "c", LastObservedAt: &at},
		"d": {ID: "d", LastObservedAt: &before, Alerts: []Alert{{Code: "slow_call"}}}, "e": {ID: "e"},
	}}
	for _, tc := range []struct {
		sort string
		want []string
	}{{"recent", []string{"b", "c", "d", "a", "e"}}, {"alerts", []string{"d", "b", "c", "a", "e"}}, {"未知排序", []string{"b", "c", "d", "a", "e"}}} {
		for i := 0; i < 10; i++ {
			items := x.QueryTasks(TaskQuery{Sort: tc.sort})
			ids := []string{}
			for _, item := range items {
				ids = append(ids, item.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("排序 %s 不稳定: %v", tc.sort, ids)
			}
		}
	}
}

func TestAlertsPaginateMatchingTasksAndKeepEvidence(t *testing.T) {
	x := queryFixtureIndex()
	task := x.tasks["任务-000"]
	task.Alerts = append(task.Alerts, Alert{Code: "unknown_effect", Message: "未知效果", AttemptID: "尝试-2", Sequence: 9, EvidenceID: "证据-2"}, Alert{Code: "slow_call"}, Alert{Code: "activity_unknown"}, Alert{Code: "repeated_exploration"}, Alert{Code: "result_mismatch"})
	x.tasks[task.ID] = task
	page := x.Alerts("w1", "", "budget", 2, 7)
	if page.Total != 41 || page.AlertTotal != 82 || page.Page != 2 || page.PageSize != 7 || len(page.Items) != 7 || page.Items[0].Task.ID != "任务-035" {
		t.Fatalf("按匹配任务分页或总数错误: %+v", page)
	}
	unknown := x.Alerts("w1", "api 任务-000", "unknown", 1, 50)
	if unknown.Total != 1 || unknown.AlertTotal != 1 || len(unknown.Items) != 1 || len(unknown.Items[0].Alerts) != 1 {
		t.Fatalf("类别与搜索筛选错误: %+v", unknown)
	}
	a := unknown.Items[0].Alerts[0]
	if a.Code != "unknown_effect" || a.Category != "unknown" || a.AttemptID != "尝试-2" || a.Sequence != 9 || a.EvidenceID != "证据-2" {
		t.Fatalf("提示语义或定位证据丢失: %+v", a)
	}
	for category, code := range map[string]string{"budget": "model_budget", "unknown": "unknown_effect", "repeat": "repeated_exploration", "slow": "slow_call", "activity": "activity_unknown", "record": "result_mismatch"} {
		filtered := x.Alerts("w1", "任务-000", category, 1, 50)
		if len(filtered.Items) != 1 || len(filtered.Items[0].Alerts) != 1 || filtered.Items[0].Alerts[0].Code != code {
			t.Errorf("分类 %s 错误: %+v", category, filtered)
		}
	}
	for _, page := range []AlertPage{x.Alerts("w1", "", "不存在", 1, 50), x.Alerts("w1", "", "budget", 100, 50), x.Alerts("w2", "", "budget", 1, 50)} {
		if len(page.Items) != 0 || page.Items == nil {
			t.Fatalf("空分页应为空数组: %+v", page)
		}
	}
}

func TestOverviewUsesOneSnapshotDuringConcurrentQueries(t *testing.T) {
	x := &Index{revision: 1, tasks: map[string]Task{"1": {ID: "1", Activity: "unknown"}}, workspaces: map[string]Workspace{}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			n := uint64(i%2 + 1)
			tasks := map[string]Task{"1": {ID: "1", Activity: "unknown"}}
			if n == 2 {
				tasks["2"] = Task{ID: "2", Activity: "unknown"}
			}
			x.mu.Lock()
			x.tasks, x.revision = tasks, n
			x.mu.Unlock()
		}
	}()
	for i := 0; i < 500; i++ {
		view := x.Overview("")
		if view.Total != int(view.Revision) || view.Counts.Unknown != view.Total || len(view.Recent) != view.Total {
			t.Errorf("概览混用了不同快照: %+v", view)
			break
		}
	}
	wg.Wait()
}

func TestQueryHTTPCompatibilityPaginationAndSecurity(t *testing.T) {
	x, _, hs, cookie := authenticated(t)
	seed := queryFixtureIndex()
	x.mu.Lock()
	x.tasks, x.workspaces, x.revision = seed.tasks, seed.workspaces, seed.revision
	x.evaluations = []Evaluation{{ID: "评分-1", WorkspaceID: "w1"}, {ID: "评分-2", WorkspaceID: "w2"}}
	task := x.tasks["任务-000"]
	task.Goal = "检查 API sk-abcdefghijklmnopqrstuvw"
	x.tasks[task.ID] = task
	x.mu.Unlock()
	get := func(path, origin, access string) (int, map[string]json.RawMessage, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, hs.URL+path, nil)
		if access != "" {
			req.Header.Set("Cookie", access)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("接口返回无效 JSON: %s", raw)
		}
		return resp.StatusCode, body, string(raw)
	}
	status, full, _ := get("/api/v1/tasks?workspace=w1&page_size=1", "", cookie)
	var fullItems []map[string]json.RawMessage
	json.Unmarshal(full["items"], &fullItems)
	if status != 200 || len(fullItems) != 1 || string(fullItems[0]["final_text"]) != `"不应进入轻量列表的答复"` {
		t.Fatal("默认列表不再兼容完整 Task")
	}
	_, body, raw := get("/api/v1/tasks?workspace=w1&view=summary&page=2&page_size=50", "", cookie)
	var items []map[string]json.RawMessage
	json.Unmarshal(body["items"], &items)
	if string(body["total"]) != "205" || len(items) != 50 || string(items[0]["id"]) != `"任务-050"` || strings.Contains(raw, "final_text") || strings.Contains(raw, "attempts") || strings.Contains(raw, "evidence") || string(items[0]["alert_count"]) != "3" {
		t.Fatalf("轻量列表或分页错误: %s", raw)
	}
	_, body, _ = get("/api/v1/tasks?workspace=w1&view=summary&activity_group=approval&has_alerts=true&sort=alerts", "", cookie)
	json.Unmarshal(body["items"], &items)
	if string(body["total"]) != "2" || string(items[0]["id"]) != `"任务-005"` {
		t.Fatal("HTTP 未接入组合筛选与提示排序")
	}
	_, body, _ = get("/api/v1/tasks?workspace=w1&view=summary&page=0&page_size=999&has_alerts=invalid&sort=invalid", "", cookie)
	json.Unmarshal(body["items"], &items)
	if string(body["page"]) != "1" || string(body["page_size"]) != "200" || len(items) != 200 || string(body["total"]) != "205" {
		t.Fatal("分页上限或非法可选参数回退错误")
	}
	_, body, _ = get("/api/v1/alerts?workspace=w1&category=budget&page_size=1&page=2", "", cookie)
	var alertPage AlertPage
	rawBody, _ := json.Marshal(body)
	json.Unmarshal(rawBody, &alertPage)
	if alertPage.Total != 41 || alertPage.AlertTotal != 82 || len(alertPage.Items) != 1 || alertPage.Items[0].Task.ID != "任务-005" || alertPage.Items[0].Alerts[0].Category != "budget" {
		t.Fatalf("异常 HTTP 分页错误: %s", rawBody)
	}
	_, body, _ = get("/api/v1/evaluations?workspace=w2", "", cookie)
	var evaluations []Evaluation
	json.Unmarshal(body["items"], &evaluations)
	if len(evaluations) != 1 || evaluations[0].ID != "评分-2" {
		t.Fatal("评测未按工作区筛选")
	}
	for _, endpoint := range []string{"/api/v1/overview", "/api/v1/tasks?view=summary", "/api/v1/alerts"} {
		status, _, raw := get(endpoint, "", cookie)
		if status != 200 || strings.Contains(raw, "sk-abcdefghijkl") {
			t.Errorf("新查询未统一脱敏: %s", endpoint)
		}
		status, _, _ = get(endpoint, "", "")
		if status != 401 {
			t.Errorf("新查询绕过认证: %s", endpoint)
		}
		status, _, _ = get(endpoint, "https://外部.example", cookie)
		if status != 403 {
			t.Errorf("新查询允许跨源: %s", endpoint)
		}
	}
}
