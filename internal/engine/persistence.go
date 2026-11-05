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
		task, err := store.CreateTask(spec.ID)
		if err != nil {
			return nil, taskState{}, fmt.Errorf("%w：创建任务失败: %w", errStorage, err)
		}
		return task, taskState{Version: 1, Spec: spec, WorkDir: workDir, SessionID: sessionID, Limits: e.options.Budget.defaults(),
			EffectLedger: &Delivery{HistoryComplete: true, Effects: []EffectReceipt{}}}, nil
	}
	task, err := store.OpenTask(spec.ID)
	if err != nil {
		return nil, taskState{}, err
	}
	state, err := loadTaskState(task)
	if err == nil && (state.Spec.ID != spec.ID || state.WorkDir != workDir || state.SessionID != sessionID) {
		err = errors.New("任务身份、工作区或会话不匹配")
	}
	if err == nil && (modeLevel(state.Spec.Mode) == 0 || !state.Limits.valid()) {
		err = errors.New("持久任务的权限或预算配置无效")
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
	state := taskState{}
	for _, event := range events {
		if event.Kind == "elapsed" {
			var clock elapsedEvent
			if err := json.Unmarshal(event.Payload, &clock); err != nil || clock.Version != 1 || clock.Elapsed < state.Usage.Elapsed {
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
		if next.Version != 1 {
			return state, fmt.Errorf("不支持的任务版本 %d", next.Version)
		}
		if event.Sequence >= snapshot.Sequence {
			state = next
		}
	}
	if state.Version != 1 {
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
