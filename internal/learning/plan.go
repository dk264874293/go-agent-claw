package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type teachingStep struct {
	ID        string   `json:"id"`
	Goal      string   `json:"goal"`
	DependsOn []string `json:"depends_on"`
	Done      bool     `json:"done,omitempty"`
}

func stepPrompt(messages []schema.Message, goal string) error {
	encoded, _ := json.Marshal(goal)
	return require(len(messages) > 0 && strings.Contains(messages[0].Content, `"step":`+string(encoded)), "当前计划步骤不符合依赖顺序")
}

func planLesson(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, resume := range []bool{false, true} {
		name := "反序列表按依赖执行"
		if resume {
			name = "恢复保留已完成步骤"
		}
		dir, err := subdir(root, name)
		if err != nil {
			return trials, err
		}
		const create, check = "创建课程产物", "读取并核对课程产物"
		steps := []teachingStep{{ID: "check", Goal: check, DependsOn: []string{"create"}}, {ID: "create", Goal: create, DependsOn: []string{}, Done: resume}}
		session := ctxpkg.NewSession("计划课程", dir)
		spec := engine.TaskSpec{Goal: "先创建课程文件再读取核对", Mode: tools.ModeWorkspace, Kind: engine.TaskChange, Plan: true}
		if resume {
			if err := fixture(dir, "result.md", "计划产物"); err != nil {
				return trials, err
			}
			id, err := seed(dir, session, spec, map[string]any{"plan": steps})
			if err != nil {
				return trials, err
			}
			spec = engine.TaskSpec{ID: id, Resume: true}
		}
		model := &script{p: p, next: func(n int, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
			if resume {
				n += 3
			}
			switch n {
			case 1:
				if err := require(len(defs) == 0, "生成计划阶段不应开放工具"); err != nil {
					return nil, err
				}
				data, err := json.Marshal(steps)
				if err != nil {
					return nil, err
				}
				return answer(string(data))
			case 2:
				if err := stepPrompt(messages, create); err != nil {
					return nil, err
				}
				return call("create-result", "write_file", map[string]any{"path": "result.md", "content": "计划产物", "expected_version": "missing"})
			case 3:
				if err := stepPrompt(messages, create); err != nil {
					return nil, err
				}
				if err := observation(messages, "create-result", schema.ToolSucceeded, ""); err != nil {
					return nil, err
				}
				return answer("当前创建步骤的对话已结束。")
			case 4:
				if err := stepPrompt(messages, check); err != nil {
					return nil, err
				}
				return call("check-result", "read_file", map[string]any{"path": "result.md"})
			case 5:
				if err := observation(messages, "check-result", schema.ToolSucceeded, "计划产物"); err != nil {
					return nil, err
				}
				return answer("已通过实际读取核对课程产物，尚未执行用户验收。")
			default:
				return nil, fmt.Errorf("计划课程出现额外模型请求")
			}
		}}
		trial, err := execute(ctx, dir, name, p, model, engine.EngineOptions{Mode: tools.ModeWorkspace}, spec, session)
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		state, err := savedState(dir, trial.Result.TaskID)
		if err != nil {
			return trials, err
		}
		var completed []teachingStep
		if err := json.Unmarshal(state["plan"], &completed); err != nil {
			return trials, err
		}
		wantModels, wantDispatch := 5, 2
		if resume {
			wantModels, wantDispatch = 2, 1
		}
		if err := require(trial.Result.Status == engine.NeedsVerification && trial.ModelRequests == wantModels && len(trial.Dispatched) == wantDispatch && len(completed) == 2 && completed[0].ID == "check" && completed[0].Done && completed[1].ID == "create" && completed[1].Done, "步骤必须按依赖执行，恢复不得重新运行DONE"); err != nil {
			return trials, err
		}
		trials[len(trials)-1].Facts["completed_steps"] = 2
	}
	dir, err := subdir(root, "invalid-plan")
	if err != nil {
		return trials, err
	}
	model := &script{p: p, next: func(n int, _ []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if n != 1 || len(defs) != 0 {
			return nil, fmt.Errorf("非法依赖未在执行前停止")
		}
		return answer(`[{"id":"bad","goal":"不应执行","depends_on":["missing"]}]`)
	}}
	trial, runErr := execute(ctx, dir, "非法依赖", p, model, engine.EngineOptions{Mode: tools.ModeWorkspace}, engine.TaskSpec{Goal: "校验非法依赖", Plan: true}, nil)
	trials = append(trials, trial)
	return trials, require(runErr != nil && trial.Result.Status == engine.Failed && strings.Contains(trial.Result.Reason, "未知依赖") && trial.ModelRequests == 1 && len(trial.Dispatched) == 0, "非法计划应在实际动作前失败")
}
