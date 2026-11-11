package learning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const source = "Agent 的动作需要由程序检查权限和预算。\n"

func react(ctx context.Context, dir string, p *printer) ([]Trial, error) {
	if err := fixture(dir, "lesson.md", source); err != nil {
		return nil, err
	}
	model := &script{p: p, next: func(n int, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		switch n {
		case 1:
			visible := false
			for _, def := range defs {
				visible = visible || def.Name == "read_file"
			}
			if err := require(visible, "读取工具必须可见"); err != nil {
				return nil, err
			}
			return call("read-lesson", "read_file", map[string]any{"path": "lesson.md"})
		case 2:
			if err := observation(messages, "read-lesson", schema.ToolSucceeded, source); err != nil {
				return nil, err
			}
			return answer("已根据真实文件观察确认：动作需要权限和预算检查。")
		default:
			return nil, fmt.Errorf("最小循环出现额外模型请求 %d", n)
		}
	}}
	trial, err := execute(ctx, dir, "最小循环", p, model, engine.EngineOptions{}, engine.TaskSpec{Goal: "读取 lesson.md 并说明动作边界", Delegation: "off"}, nil)
	if err == nil {
		err = require(trial.Result.Status == engine.Answered && trial.ModelRequests == 2 && len(trial.Dispatched) == 1 && trial.Dispatched[0].Read != nil, "真实读取、两轮模型及回答终态")
	}
	return []Trial{trial}, err
}

func budgetLesson(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, maximum := range []int{1, 3} {
		dir, err := subdir(root, fmt.Sprintf("budget-%d", maximum))
		if err != nil {
			return trials, err
		}
		for i := 1; i <= maximum; i++ {
			if err := fixture(dir, fmt.Sprintf("source-%d.md", i), source); err != nil {
				return trials, err
			}
		}
		model := &script{p: p, next: func(n int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			if n > 1 {
				if err := observation(messages, fmt.Sprintf("read-%d", n-1), schema.ToolSucceeded, source); err != nil {
					return nil, err
				}
			}
			return call(fmt.Sprintf("read-%d", n), "read_file", map[string]any{"path": fmt.Sprintf("source-%d.md", n)})
		}}
		budget := limits()
		budget.MaxModelCalls = maximum
		trial, err := execute(ctx, dir, fmt.Sprintf("模型上限%d", maximum), p, model, engine.EngineOptions{Budget: budget}, engine.TaskSpec{Goal: "持续调查，观察程序何时停止", Delegation: "off"}, nil)
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		if err := require(trial.Result.Status == engine.BudgetExhausted && trial.Result.StopCode == "model_limit" && trial.ModelRequests == maximum && len(trial.Dispatched) == maximum, "模型额度必须约束实际调用"); err != nil {
			return trials, err
		}
	}
	// 工具耗尽后的一次禁工具收尾仍扣原共享模型和轮次预算。
	dir, err := subdir(root, "tool-limit")
	if err != nil {
		return trials, err
	}
	if err := fixture(dir, "lesson.md", source); err != nil {
		return trials, err
	}
	model := &script{p: p, next: func(n int, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		switch n {
		case 1:
			return call("only-read", "read_file", map[string]any{"path": "lesson.md"})
		case 2:
			if err := observation(messages, "only-read", schema.ToolSucceeded, source); err != nil {
				return nil, err
			}
			if err := require(len(defs) == 0, "预算收尾不能开放工具"); err != nil {
				return nil, err
			}
			return answer("工具额度已用尽，已有一份读取证据，任务仍未完成。")
		default:
			return nil, fmt.Errorf("收尾重复调用")
		}
	}}
	budget := limits()
	budget.MaxToolCalls, budget.MaxModelCalls = 1, 2
	trial, err := execute(ctx, dir, "工具上限1及禁工具收尾", p, model, engine.EngineOptions{Budget: budget}, engine.TaskSpec{Goal: "继续调查直到额度边界", Delegation: "off"}, nil)
	trials = append(trials, trial)
	if err == nil {
		err = require(trial.Result.Status == engine.BudgetExhausted && trial.Result.StopCode == "tool_limit" && trial.ModelRequests == 2 && trial.Result.PartialReport != nil && trial.Result.PartialReport.Source == "model" && len(trial.Dispatched) == 1, "收尾不能改写预算耗尽终态")
	}
	return trials, err
}

func permission(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, mode := range []tools.Mode{tools.ModeReadOnly, tools.ModeWorkspace} {
		dir, err := subdir(root, string(mode))
		if err != nil {
			return trials, err
		}
		if err := fixture(dir, "lesson.md", source); err != nil {
			return trials, err
		}
		model := &script{p: p, next: func(n int, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
			switch n {
			case 1:
				visible := false
				for _, def := range defs {
					visible = visible || def.Name == "write_file"
				}
				if err := require(visible == (mode == tools.ModeWorkspace), "工具前沿应符合执行模式"); err != nil {
					return nil, err
				}
				// 即使候选越过可见前沿，真实分发仍必须检查权限。
				return call("proposed-write", "write_file", map[string]any{"path": "result.md", "content": "受控写入", "expected_version": "missing"})
			case 2:
				status := schema.ToolSucceeded
				if mode == tools.ModeReadOnly {
					status = schema.ToolRejected
				}
				if err := observation(messages, "proposed-write", status, ""); err != nil {
					return nil, err
				}
				return call("legal-read", "read_file", map[string]any{"path": "lesson.md"})
			case 3:
				if err := observation(messages, "legal-read", schema.ToolSucceeded, source); err != nil {
					return nil, err
				}
				return answer("已核对实际工具观察；修改是否允许取决于执行模式。")
			default:
				return nil, fmt.Errorf("权限课程出现额外调用")
			}
		}}
		trial, err := execute(ctx, dir, string(mode), p, model, engine.EngineOptions{Mode: mode}, engine.TaskSpec{Goal: "观察同一写入候选在两种模式中的行为"}, nil)
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		if err := require(len(trial.Dispatched) == 2 && trial.ModelRequests == 3, "拒绝后仍应回传观察并继续合法读取"); err != nil {
			return trials, err
		}
		data, readErr := os.ReadFile(filepath.Join(dir, "result.md"))
		if mode == tools.ModeReadOnly {
			err = require(os.IsNotExist(readErr) && trial.Dispatched[0].Effect == schema.EffectNone && trial.Result.Status == engine.Answered, "只读拒绝不得产生文件效果")
		} else {
			err = require(readErr == nil && string(data) == "受控写入" && trial.Dispatched[0].Effect == schema.EffectConfirmed && trial.Result.Status == engine.NeedsVerification, "写入确认仍不代表验收通过")
		}
		if err != nil {
			return trials, err
		}
	}
	return trials, nil
}
