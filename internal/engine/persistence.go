package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func modeLevel(mode tools.Mode) int {
	switch mode {
	case tools.ModeReadOnly:
		return 1
	case tools.ModeWorkspace:
		return 2
	case tools.ModeTrusted:
		return 3
	default:
		return 0
	}
}

func (e *AgentEngine) openTask(store *taskstore.Store, workDir, sessionID string, spec TaskSpec) (*taskstore.Task, taskState, error) {
	if spec.Mode == "" {
		spec.Mode = e.options.Mode
	}
	if modeLevel(spec.Mode) == 0 || modeLevel(spec.Mode) > modeLevel(e.options.Mode) {
		return nil, taskState{}, errors.New("任务执行模式超出当前权限")
	}
	if !spec.Resume {
		spec.Goal = strings.TrimSpace(spec.Goal)
		if spec.Goal == "" {
			return nil, taskState{}, errors.New("任务目标不能为空")
		}
		spec.Plan = spec.Plan || e.options.Plan
		if spec.Kind == "" {
			spec.Kind = TaskChange
			if spec.Mode == tools.ModeReadOnly && spec.VerifyCommand == "" {
				spec.Kind = TaskAnswer
			}
		}
		if spec.Kind != TaskAnswer && spec.Kind != TaskChange {
			return nil, taskState{}, errors.New("任务类型必须为 answer 或 change")
		}
		if spec.Analysis && (spec.Mode != tools.ModeReadOnly || spec.Kind != TaskAnswer || spec.VerifyCommand != "") {
			return nil, taskState{}, errors.New("分析模式仅支持 readonly + answer，不能指定验收命令")
		}
		if err := NormalizeReadFeatures(&spec); err != nil {
			return nil, taskState{}, err
		}
		if spec.ReadPageBytes == 0 {
			spec.ReadPageBytes = 8000
		}
		if spec.ReadPageBytes != 8000 && spec.ReadPageBytes != 16000 {
			return nil, taskState{}, errors.New("读取档位必须为8000或16000")
		}
		if err := NormalizeReadScope(&spec); err != nil {
			return nil, taskState{}, err
		}
		if spec.OutputStyle == "" {
			spec.OutputStyle = "concise"
		}
		if spec.OutputStyle != "concise" && spec.OutputStyle != "detailed" {
			return nil, taskState{}, errors.New("输出偏好必须为 concise 或 detailed")
		}
		if err := NormalizeQualitySpec(&spec); err != nil {
			return nil, taskState{}, err
		}
		if err := NormalizeDeliveryReviewSpec(&spec); err != nil {
			return nil, taskState{}, err
		}
		if spec.DeliveryReview != nil && e.options.EnableThinking {
			return nil, taskState{}, errors.New("交付前处理不能组合额外思考")
		}
		if spec.Quality != nil && e.options.EnableThinking {
			return nil, taskState{}, errors.New("质量模式不能组合额外思考")
		}
		if err := NormalizeReportSpec(&spec); err != nil {
			return nil, taskState{}, err
		}
		delegation, err := initializeDelegation(&spec)
		if err != nil {
			return nil, taskState{}, err
		}
		task, err := store.CreateTask(spec.ID)
		if err != nil {
			return nil, taskState{}, fmt.Errorf("%w：创建任务失败: %w", errStorage, err)
		}
		version := 1
		var quality *QualityState
		var deliveryReview *DeliveryReviewState
		if spec.DeliveryReview != nil {
			version = 3
			deliveryReview = &DeliveryReviewState{Version: 1, Status: "not_run"}
		}
		if spec.Quality != nil {
			version = 2
			quality = &QualityState{Version: 1, Status: "exploring", CheckResult: "not_run", ReviewStatus: "not_run", RevisionStatus: "not_run"}
		}
		return task, taskState{Version: version, DeliveryReview: deliveryReview, Quality: quality, Spec: spec, WorkDir: workDir, SessionID: sessionID, Limits: e.options.Budget.defaults(), Delegation: delegation,
			EffectLedger: &Delivery{HistoryComplete: true, Effects: []EffectReceipt{}}}, nil
	}
	task, err := store.OpenTask(spec.ID)
	if err != nil {
		return nil, taskState{}, err
	}
	state, err := loadTaskState(task)
	if err == nil {
		err = validateReadFeaturesResume(spec, state.Spec)
	}
	if err == nil {
		err = validateDeliveryReviewResume(spec, state.Spec)
	}
	if err == nil && state.Spec.DeliveryReview != nil && (e.options.EnableThinking || e.options.Plan) {
		err = errors.New("交付前处理恢复不能组合额外思考或规划")
	}
	if err == nil && spec.Quality != nil {
		requested := *spec.Quality
		if requested.Version == 0 {
			requested.Version = 1
		}
		if state.Spec.Quality == nil || *state.Spec.Quality != requested {
			err = errors.New("恢复时不能开启或更改质量策略")
		}
	}
	if err == nil && state.Spec.Quality != nil && e.options.EnableThinking {
		err = errors.New("质量任务恢复不能启用额外思考")
	}
	if err == nil && (state.Spec.ID != spec.ID || state.WorkDir != workDir || state.SessionID != sessionID) {
		err = errors.New("任务身份、工作区或会话不匹配")
	}
	if err == nil && (modeLevel(state.Spec.Mode) == 0 || !state.Limits.valid()) {
		err = errors.New("持久任务的权限或预算配置无效")
	}
	if err == nil && spec.ReadPageBytes != 0 && spec.ReadPageBytes != effectivePageBytes(state.Spec.ReadPageBytes) {
		err = errors.New("恢复时不能修改持久读取档位")
	}
	if err == nil && spec.OutputStyle != "" && spec.OutputStyle != state.Spec.OutputStyle {
		err = errors.New("恢复时不能修改持久输出偏好")
	}
	if err == nil && spec.ReadScope != nil {
		if scopeErr := NormalizeReadScope(&spec); scopeErr != nil {
			err = scopeErr
		} else {
			requested, _ := json.Marshal(spec.ReadScope)
			saved, _ := json.Marshal(state.Spec.ReadScope)
			if string(requested) != string(saved) {
				err = errors.New("恢复时不能修改持久资料范围")
			}
		}
	}
	if err == nil && len(state.Plan) != 0 {
		err = checkPlan(state.Plan)
	}
	if err != nil {
		task.Close()
		return nil, taskState{}, fmt.Errorf("%w：%v", errBlocked, err)
	}
	b := e.options.Budget
	state.Limits.MaxTurns = max(state.Limits.MaxTurns, b.MaxTurns)
	state.Limits.MaxModelCalls = max(state.Limits.MaxModelCalls, b.MaxModelCalls)
	state.Limits.MaxToolCalls = max(state.Limits.MaxToolCalls, b.MaxToolCalls)
	state.Limits.Timeout = max(state.Limits.Timeout, b.Timeout)
	return task, state, nil
}

func loadTaskState(task *taskstore.Task) (taskState, error) {
	events, err := task.Events()
	if err != nil {
		return taskState{}, err
	}
	snapshot, err := task.LoadSnapshot()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return taskState{}, err
	}
	if len(snapshot.State) != 0 {
		var checkpoint taskState
		if err := json.Unmarshal(snapshot.State, &checkpoint); err != nil || ValidateTaskStateVersion(checkpoint) != nil {
			return taskState{}, fmt.Errorf("任务检查点状态版本或内容无效")
		}
		if err := validateReadonlyState(checkpoint); err != nil {
			return taskState{}, fmt.Errorf("任务检查点状态无效: %w", err)
		}
		if err := validateReportState(checkpoint); err != nil {
			return taskState{}, err
		}
		if err := validateReadMemoryState(checkpoint); err != nil {
			return taskState{}, err
		}
		if err := validateReadPolicy(checkpoint); err != nil {
			return taskState{}, err
		}
		if err := validateAnalysisState(checkpoint); err != nil {
			return taskState{}, err
		}
		if err := ValidateDelegationState(checkpoint); err != nil {
			return taskState{}, err
		}
	}
	state := taskState{}
	for _, event := range events {
		if event.Kind == "elapsed" {
			var clock elapsedEvent
			if err := json.Unmarshal(event.Payload, &clock); err != nil || clock.Version != state.Version || clock.Elapsed < state.Usage.Elapsed {
				return state, fmt.Errorf("计时事件 %d 无效", event.Sequence)
			}
			state.Usage.Elapsed = clock.Elapsed
			continue
		}
		// 日志中的每个状态均带版本，拒绝未知的迁移格式。
		var next taskState
		if err := json.Unmarshal(event.Payload, &next); err != nil {
			return state, fmt.Errorf("任务事件 %d 无效: %w", event.Sequence, err)
		}
		if state.Version != 0 && (state.Version != next.Version || (state.Spec.Quality == nil) != (next.Spec.Quality == nil)) {
			return state, fmt.Errorf("同一任务不能更换质量策略或状态版本")
		}
		if state.Version != 0 {
			if err := validateDeliveryReviewTransition(state, next); err != nil {
				return state, err
			}
			if err := validateReadFeatureTransition(state, next); err != nil {
				return state, err
			}
			if err := validateQualityTransition(state, next); err != nil {
				return state, err
			}
		}
		if err := ValidateTaskStateVersion(next); err != nil {
			return state, fmt.Errorf("任务状态无效: %w", err)
		}
		if err := validateReadonlyState(next); err != nil {
			return state, fmt.Errorf("任务事件 %d 的只读停止状态无效: %w", event.Sequence, err)
		}
		if err := validateReportState(next); err != nil {
			return state, err
		}
		if err := validateReadMemoryState(next); err != nil {
			return state, err
		}
		if err := validateReadPolicy(next); err != nil {
			return state, err
		}
		if err := validateAnalysisState(next); err != nil {
			return state, fmt.Errorf("任务事件 %d 的分析状态无效: %w", event.Sequence, err)
		}
		if err := ValidateDelegationState(next); err != nil {
			return state, fmt.Errorf("任务事件 %d 委派状态无效: %w", event.Sequence, err)
		}
		state = next
	}
	if ValidateTaskStateVersion(state) != nil {
		return state, errors.New("任务没有可恢复的状态")
	}
	return state, nil
}

func (e *AgentEngine) reconcile(ctx context.Context, r *runState) (string, error) {
	for id, pending := range r.state.Pending {
		if pending.Before == nil || pending.Expected == nil {
			return fmt.Sprintf("动作 %s（%s）的效果未知，须人工核实后再继续", id, pending.Call.Name), nil
		}
		version, err := tools.FileVersion(ctx, pending.Before.Path)
		if err != nil {
			return fmt.Sprintf("无法核实动作 %s 的文件版本: %v", id, err), nil
		}
		if version != pending.Expected.Version && version != pending.Before.Version {
			return fmt.Sprintf("动作 %s 对应文件已变更，须人工核实", id), nil
		}
		if err := r.record("reconciled", func(s *taskState) error {
			delete(s.Pending, id)
			receipt := EffectReceipt{ActionID: id, Tool: pending.Call.Name, Effect: schema.EffectNone}
			if version == pending.Expected.Version {
				if s.Completed == nil {
					s.Completed = make(map[string]schema.ToolResult)
				}
				s.Completed[actionKey(pending.StepID, pending.Call)] = schema.ToolResult{
					Status: schema.ToolSucceeded, Effect: schema.EffectConfirmed,
					Output: "恢复时已核实文件效果，无需重新执行。",
				}
				receipt = effectReceipt(pending, schema.ToolResult{Status: schema.ToolSucceeded, Effect: schema.EffectConfirmed})
				r.note(s, "已核实动作 "+id+" 的文件效果")
			}
			setEffect(s, receipt)
			return nil
		}); err != nil {
			return "", err
		}
	}
	return "", nil
}
