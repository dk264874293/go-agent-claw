package learning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// countWords 只计算输入，不访问外部系统。新工具的Schema不会自动授予只读权限。
type countWords struct{ executions int }

func (*countWords) Name() string { return "count_words" }
func (*countWords) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "count_words", Description: "按空白统计输入字段数",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}
}

func (t *countWords) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Text *string `json:"text"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", tools.NewRejectedError("参数错误", "invalid_args", err)
	}
	if input.Text == nil {
		return "", tools.NewRejectedError("参数错误", "invalid_args", fmt.Errorf("必须提供text字符串"))
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", tools.NewRejectedError("参数错误", "invalid_args", fmt.Errorf("只接受一个JSON对象"))
	}
	t.executions++
	return strconv.Itoa(len(strings.Fields(*input.Text))), nil
}

func extension(ctx context.Context, root string, p *printer) ([]Trial, error) {
	var trials []Trial
	for _, mode := range []tools.Mode{tools.ModeReadOnly, tools.ModeTrusted} {
		dir, err := subdir(root, string(mode))
		if err != nil {
			return trials, err
		}
		tool := &countWords{}
		model := &script{p: p, next: func(n int, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
			if n == 1 {
				visible := false
				for _, def := range defs {
					visible = visible || def.Name == tool.Name()
				}
				if err := require(visible == (mode == tools.ModeTrusted), "仅注册新工具不会授予readonly权限"); err != nil {
					return nil, err
				}
				return call("custom", tool.Name(), map[string]any{"text": "模型 候选 程序"})
			}
			if n != 2 {
				return nil, fmt.Errorf("扩展课程出现额外请求")
			}
			status, text := schema.ToolRejected, ""
			if mode == tools.ModeTrusted {
				status, text = schema.ToolSucceeded, "3"
			}
			if err := observation(messages, "custom", status, text); err != nil {
				return nil, err
			}
			return answer("已核对新工具的可见性、实际准入与回执分类。")
		}}
		trial, err := execute(ctx, dir, "新工具仅注册："+string(mode), p, model, engine.EngineOptions{Mode: mode}, engine.TaskSpec{Goal: "统计三个输入字段，观察新工具扩展契约", Kind: engine.TaskAnswer}, nil, tool)
		trial.Facts["custom_handler_executions"] = tool.executions
		trials = append(trials, trial)
		if err != nil {
			return trials, err
		}
		if err := require(trial.Result.Status == engine.Answered && len(trial.Dispatched) == 1, "扩展回执应正常回传"); err != nil {
			return trials, err
		}
		if mode == tools.ModeReadOnly {
			err = require(tool.executions == 0 && trial.Dispatched[0].Effect == schema.EffectNone, "未准入的新工具不应进入Handler")
		} else {
			err = require(tool.executions == 1 && trial.Dispatched[0].Output == "3" && trial.Dispatched[0].Effect == schema.EffectUnknown && len(trial.Result.Delivery.Effects) == 0, "未分类工具的成功不能冒充已知副作用或持久动作账")
		}
		if err != nil {
			return trials, err
		}
	}
	return trials, nil
}
