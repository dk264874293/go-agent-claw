package monitor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

// 独立解码评测原始协议，不复用监测 DTO 或索引中的数据来构造期望值。
type realImportRecord struct {
	SchemaVersion     int      `json:"schema_version"`
	ExperimentID      string   `json:"experiment_id"`
	CaseID            string   `json:"case_id"`
	Repeat            int      `json:"repeat"`
	Arm               string   `json:"arm"`
	TaskID            string   `json:"task_id"`
	RunID             string   `json:"run_id"`
	EngineStatus      string   `json:"engine_status"`
	StopCode          string   `json:"stop_code"`
	ArtifactResult    string   `json:"artifact_result"`
	ScopeResult       string   `json:"scope_result"`
	EngineResult      string   `json:"engine_result"`
	EnvironmentResult string   `json:"environment_result"`
	CheckerResult     string   `json:"checker_result"`
	CombinedPass      bool     `json:"combined_pass"`
	Labels            []string `json:"labels"`
	ModelCalls        *int     `json:"model_calls"`
	ToolCalls         *int     `json:"tool_calls"`
	Cost              *float64 `json:"cost"`
}

func (r realImportRecord) key() string {
	return fmt.Sprintf("%s/%s/%d/%s", r.ExperimentID, r.CaseID, r.Repeat, r.Arm)
}

// verifyRealEvaluationRecords 只读原始评分和任务事件，不运行模型、工具、恢复或 oracle。
func verifyRealEvaluationRecords(t *testing.T, base string, index *Index) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(base, "runs", "*", "result.json"))
	if err != nil || len(paths) != 30 {
		t.Fatalf("原始结果数量不符: count=%d err=%v", len(paths), err)
	}
	projected := make(map[string]Evaluation)
	for _, value := range index.Evaluations() {
		key := fmt.Sprintf("%s/%s/%d/%s", value.ExperimentID, value.CaseID, value.Repeat, value.Arm)
		if _, exists := projected[key]; exists {
			t.Fatalf("监测评分身份重复: %s", key)
		}
		projected[key] = value
	}
	totalEffects, fileEffects, multiLabels, completeHistories, incompleteHistories := 0, 0, 0, 0, 0
	for _, path := range paths {
		t.Run("原始协议与持久状态/"+filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record realImportRecord
			if err := json.Unmarshal(raw, &record); err != nil || record.SchemaVersion != 2 {
				t.Fatalf("原始结果协议无效: %v", err)
			}
			value, ok := projected[record.key()]
			if !ok {
				t.Fatalf("缺少原始样本投影: %s", record.key())
			}
			verifyRealEvaluationFields(t, value, record)
			if len(record.Labels) > 1 {
				multiLabels++
			}
			work := filepath.Join(filepath.Dir(path), "workspace")
			workspaceID := observe.WorkspaceID(work)
			if value.WorkspaceID != workspaceID {
				t.Fatal("外部评分绑定了错误工作区")
			}
			task, exists := index.Task(stableID(workspaceID + "/" + record.TaskID))
			if !exists || task.WorkspaceID != workspaceID || task.TaskID != record.TaskID {
				t.Fatal("原始任务未绑定到正确工作区")
			}
			if len(task.Evaluations) != 1 {
				t.Fatalf("任务关联评分数量错误: %d", len(task.Evaluations))
			}
			verifyRealEvaluationFields(t, task.Evaluations[0], record)
			view := inspectOriginalTaskEvents(t, work, record.TaskID)
			if view.Result == nil || view.Running || view.Result.RunID != record.RunID {
				t.Fatal("原始持久终态与评测运行身份不符")
			}
			if task.EngineStatus != record.EngineStatus || task.EngineStatus != string(view.Result.Status) || task.StopCode != record.StopCode || task.StopCode != view.Result.StopCode {
				t.Fatal("引擎终态或停止原因与原始记录不符")
			}
			if view.Delivery == nil {
				t.Fatal("持久事件没有可解释的效果历史")
			}
			if task.HistoryComplete != view.Delivery.HistoryComplete || !reflect.DeepEqual(task.Effects, view.Delivery.Effects) {
				t.Fatalf("效果投影与原始事件不符: monitor=%+v inspection=%+v", task.Effects, view.Delivery.Effects)
			}
			if task.HistoryComplete {
				completeHistories++
			} else {
				incompleteHistories++
			}
			totalEffects += len(task.Effects)
			for _, effect := range task.Effects {
				if effect.File != nil {
					fileEffects++
				}
			}
			// 验收正文允许脱敏和有界裁切，其余版本、通过标记等必须原样保留。
			var expectedVerification *engine.Verification
			if view.Verification != nil {
				copy := *view.Verification
				copy.Output = clip(observe.Redact(copy.Output), 2000)
				expectedVerification = &copy
			}
			if !reflect.DeepEqual(task.Verification, expectedVerification) {
				t.Fatal("引擎验收记录与原始持久状态不符")
			}
			if task.FinalText != observe.Redact(view.Result.FinalText) {
				t.Fatal("最终答复与原始持久状态不符")
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(raw) != sha256.Sum256(after) {
				t.Fatal("只读校验修改了原始评分")
			}
		})
	}
	if multiLabels == 0 || fileEffects == 0 || completeHistories == 0 || incompleteHistories == 0 {
		t.Fatalf("验收数据未覆盖关键情况: 多标签=%d 文件效果=%d 完整历史=%d 不完整历史=%d", multiLabels, fileEffects, completeHistories, incompleteHistories)
	}
	t.Logf("逐例校验完成: 原始结果=30 五维判定=150 多失败标签样本=%d 效果回执=%d 文件版本回执=%d 完整历史=%d 不完整历史=%d；原始评分与任务日志均未改变", multiLabels, totalEffects, fileEffects, completeHistories, incompleteHistories)
}

func verifyRealEvaluationFields(t *testing.T, value Evaluation, record realImportRecord) {
	t.Helper()
	if value.ReadError != "" || value.ExperimentID != record.ExperimentID || value.CaseID != record.CaseID || value.Repeat != record.Repeat || value.Arm != record.Arm || value.TaskID != record.TaskID || value.EngineStatus != record.EngineStatus || value.StopCode != record.StopCode || value.CombinedPass != record.CombinedPass {
		t.Fatal("评测身份、引擎结果或综合判定与原始协议不符")
	}
	for name, pair := range map[string][2]string{
		"artifact_result":    {value.ArtifactResult, record.ArtifactResult},
		"scope_result":       {value.ScopeResult, record.ScopeResult},
		"engine_result":      {value.EngineResult, record.EngineResult},
		"environment_result": {value.EnvironmentResult, record.EnvironmentResult},
		"checker_result":     {value.CheckerResult, record.CheckerResult},
	} {
		if pair[0] != pair[1] || pair[1] == "" {
			t.Errorf("%s 投影不符: got=%q raw=%q", name, pair[0], pair[1])
		}
	}
	if !reflect.DeepEqual(value.Labels, record.Labels) {
		t.Errorf("失败标签被丢弃或重排: got=%v raw=%v", value.Labels, record.Labels)
	}
	if !reflect.DeepEqual(value.ModelCalls, record.ModelCalls) || !reflect.DeepEqual(value.ToolCalls, record.ToolCalls) || !reflect.DeepEqual(value.Cost, record.Cost) {
		t.Error("调用数或未知费用与原始协议不符")
	}
}

func inspectOriginalTaskEvents(t *testing.T, work, taskID string) engine.Inspection {
	t.Helper()
	path := filepath.Join(work, ".claw", "tasks", taskID, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var view engine.Inspection
	for {
		var event taskstore.Event
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if err := view.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(raw) != sha256.Sum256(after) {
		t.Fatal("只读解释修改了原始任务日志")
	}
	return view
}
