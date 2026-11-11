package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// seed 是显式的历史状态教学夹具，不是生产写状态接口。字段沿用状态版本1，
// 与 loop_test.go 中的中断夹具一致；只在新建的临时工作区构造。
func seed(dir string, session *ctxpkg.Session, spec engine.TaskSpec, extra map[string]any) (string, error) {
	id := taskstore.NewID()
	spec.ID = id
	state := map[string]any{"version": 1, "spec": spec, "session_id": session.ID,
		"work_dir": dir, "limits": limits(), "usage": engine.Usage{}}
	for key, value := range extra {
		state[key] = value
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		return "", err
	}
	defer store.Close()
	task, err := store.CreateTask(id)
	if err != nil {
		return "", err
	}
	defer task.Close()
	if _, err := task.Append("started", state); err != nil {
		return "", err
	}
	return id, task.SaveSnapshot(state)
}

func savedState(dir, id string) (map[string]json.RawMessage, error) {
	store, err := taskstore.Open(dir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	task, err := store.OpenTask(id)
	if err != nil {
		return nil, err
	}
	defer task.Close()
	snapshot, err := task.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	var state map[string]json.RawMessage
	err = json.Unmarshal(snapshot.State, &state)
	return state, err
}

func contextLesson(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, legacy := range []bool{true, false} {
		name := "当前策略2"
		if legacy {
			name = "历史策略0"
		}
		dir, err := subdir(root, name)
		if err != nil {
			return trials, err
		}
		for i := 0; i < 24; i++ {
			if err := fixture(dir, fmt.Sprintf("%02d.md", i), fmt.Sprintf("唯一来源-%02d\n", i)); err != nil {
				return trials, err
			}
		}
		visible := 0
		model := &script{p: p, next: func(n int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			if n <= 2 {
				msg := &schema.Message{Role: schema.RoleAssistant}
				for i := (n - 1) * 12; i < n*12; i++ {
					raw, err := json.Marshal(map[string]any{"path": fmt.Sprintf("%02d.md", i)})
					if err != nil {
						return nil, err
					}
					msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{ID: fmt.Sprintf("r%d", i), Name: "read_file", Arguments: raw})
				}
				return msg, nil
			}
			if n != 3 {
				return nil, fmt.Errorf("上下文课程出现额外请求")
			}
			var text strings.Builder
			for _, m := range messages {
				text.WriteString(m.Content)
			}
			for i := 0; i < 24; i++ {
				if strings.Contains(text.String(), fmt.Sprintf("唯一来源-%02d", i)) {
					visible++
				}
			}
			p.printf("[请求证据] 实际成功读取24份，本次请求可见原文%d份\n", visible)
			if legacy {
				return answer("窗口中仅有部分原文，不能据此声称已综合全部资料。")
			}
			if err := require(visible == 24, "当前策略不得丢失未处理来源"); err != nil {
				return nil, err
			}
			return answer("24份未处理原文均保留在本次请求中，尚未进行语义质量验收。")
		}}
		spec := engine.TaskSpec{Goal: "综合24份来源，保留未处理正文", Mode: tools.ModeReadOnly, Kind: engine.TaskAnswer, Delegation: "off"}
		session := ctxpkg.NewSession("上下文课程", dir)
		if legacy {
			// 新任务的0会规范化为2，故用历史夹具恢复，不增加生产降级开关。
			id, err := seed(dir, session, spec, nil)
			if err != nil {
				return trials, err
			}
			spec = engine.TaskSpec{ID: id, Resume: true}
		}
		trial, err := execute(ctx, dir, name, p, model, engine.EngineOptions{}, spec, session)
		trial.Facts["source_bodies_in_final_request"] = visible
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		if err := require(trial.Result.Status == engine.Answered && len(trial.Dispatched) == 24 && ((!legacy && visible == 24) || (legacy && visible > 0 && visible < 24)), "窗口对照应保留配对并显示不同原文覆盖"); err != nil {
			return trials, err
		}
	}
	// 必要上下文超限时，在调用模型前明确停止。
	dir, err := subdir(root, "hard-limit")
	if err != nil {
		return trials, err
	}
	model := &script{p: p, next: func(int, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return nil, fmt.Errorf("必要上下文超限时不应到达 Provider")
	}}
	trial, runErr := execute(ctx, dir, "必要上下文超限", p, model, engine.EngineOptions{MaxContextBytes: 64}, engine.TaskSpec{Goal: "核对必要上下文超限停止"}, nil)
	trials = append(trials, trial)
	return trials, require(trial.Result.Status == engine.Blocked && trial.Result.StopCode == "blocked" && strings.Contains(trial.Result.Reason, "上下文") && trial.ModelRequests == 0 && len(trial.Dispatched) == 0 && runErr == nil, "必要上下文超限必须在模型前停止")
}

func recovery(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, known := range []bool{true, false} {
		name := "文件效果可核实"
		if !known {
			name = "命令效果未知"
		}
		dir, err := subdir(root, name)
		if err != nil {
			return trials, err
		}
		session := ctxpkg.NewSession("恢复课程", dir)
		action := schema.ToolCall{ID: "original", Name: "bash", Arguments: json.RawMessage(`{"command":"printf x > never-run.txt"}`)}
		pending := map[string]any{"id": "interrupted", "call": action}
		if known {
			action = schema.ToolCall{ID: "original", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.md","content":"已生效","expected_version":"missing"}`)}
			scope := tools.WithScope(ctx, tools.ExecutionScope{Mode: tools.ModeWorkspace, WorkDir: dir})
			preview, err := tools.PreviewWrite(scope, action)
			if err != nil {
				return trials, err
			}
			pending = map[string]any{"id": "interrupted", "call": action,
				"before":   map[string]string{"path": preview.Path, "version": preview.Before},
				"expected": map[string]string{"path": preview.Path, "version": preview.After}}
			if err := fixture(dir, "result.md", "已生效"); err != nil {
				return trials, err
			}
		}
		id, err := seed(dir, session, engine.TaskSpec{Goal: "恢复中断动作并核对效果", Mode: tools.ModeTrusted, Kind: engine.TaskChange},
			map[string]any{"pending": map[string]any{"interrupted": pending}})
		if err != nil {
			return trials, err
		}
		model := &script{p: p, next: func(n int, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			if !known {
				return nil, fmt.Errorf("未知效果未核实前不应调用模型")
			}
			switch n {
			case 1:
				action.ID = "proposed-again"
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{action}}, nil
			case 2:
				if err := observation(messages, "proposed-again", schema.ToolSucceeded, "恢复时已核实文件效果"); err != nil {
					return nil, err
				}
				p.printf("[回执复用] 真实文件版本已核实，本轮未再次分发写入\n")
				return answer("已确认中断前的文件效果，无需重复写入；尚无运行验收。")
			default:
				return nil, fmt.Errorf("恢复课程出现额外请求")
			}
		}}
		trial, err := execute(ctx, dir, name, p, model, engine.EngineOptions{Mode: tools.ModeTrusted}, engine.TaskSpec{ID: id, Resume: true}, session)
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		if err := require(len(trial.Dispatched) == 0, "恢复不能重放已确认或未知动作"); err != nil {
			return trials, err
		}
		if known {
			data, err := os.ReadFile(filepath.Join(dir, "result.md"))
			if err != nil {
				return trials, err
			}
			if err := require(string(data) == "已生效" && trial.ModelRequests == 2 && trial.Result.Status == engine.NeedsVerification && trial.Result.Delivery != nil && len(trial.Result.Delivery.Effects) == 1 && trial.Result.Delivery.Effects[0].Effect == schema.EffectConfirmed, "文件效果必须由真实版本确认"); err != nil {
				return trials, err
			}
		} else {
			_, err := os.Stat(filepath.Join(dir, "never-run.txt"))
			if err := require(os.IsNotExist(err) && trial.ModelRequests == 0 && trial.Result.Status == engine.Blocked && strings.Contains(trial.Result.Reason, "效果未知"), "未知命令必须阻塞且不能执行"); err != nil {
				return trials, err
			}
		}
	}
	return trials, nil
}
