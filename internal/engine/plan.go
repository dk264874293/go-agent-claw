package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type planStep struct {
	ID        string   `json:"id"`
	Goal      string   `json:"goal"`
	DependsOn []string `json:"depends_on"`
	Done      bool     `json:"done,omitempty"`
}

var stepID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

func checkPlan(steps []planStep) error {
	if len(steps) == 0 || len(steps) > 20 {
		return fmt.Errorf("计划需包含 1 至 20 个步骤")
	}
	byID := make(map[string]planStep, len(steps))
	for _, step := range steps {
		if !stepID.MatchString(step.ID) || strings.TrimSpace(step.Goal) == "" {
			return fmt.Errorf("无效的计划步骤 %q", step.ID)
		}
		if _, ok := byID[step.ID]; ok {
			return fmt.Errorf("重复的计划步骤 %q", step.ID)
		}
		byID[step.ID] = step
	}
	seen := make(map[string]uint8, len(steps))
	var visit func(string) error
	visit = func(id string) error {
		switch seen[id] {
		case 1:
			return fmt.Errorf("步骤 %q 存在循环依赖", id)
		case 2:
			return nil
		}
		step, ok := byID[id]
		if !ok {
			return fmt.Errorf("未知依赖 %q", id)
		}
		seen[id] = 1
		for _, dep := range step.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		seen[id] = 2
		return nil
	}
	for _, step := range steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}
	return nil
}

func readyStep(steps []planStep) int {
	done := make(map[string]bool, len(steps))
	for _, step := range steps {
		done[step.ID] = step.Done
	}
	for i, step := range steps {
		if step.Done {
			continue
		}
		ready := true
		for _, dep := range step.DependsOn {
			ready = ready && done[dep]
		}
		if ready {
			return i
		}
	}
	return -1
}

func (e *AgentEngine) proposePlan(ctx context.Context, r *runState, system schema.Message, reason string) ([]planStep, error) {
	instruction := "将任务拆成小步骤。只输出 JSON 数组，每项包含 id、goal、depends_on，依赖是步骤 ID 数组。"
	var frozen []planStep
	for _, step := range r.state.Plan {
		if step.Done {
			frozen = append(frozen, step)
		}
	}
	if reason != "" {
		current, _ := json.Marshal(r.state.Plan)
		instruction += "\n当前计划：" + string(current) + "\n阻塞原因：" + reason +
			"\n只返回待处理步骤的替换列表。完成步骤由程序保留，可引用其 ID。"
	}
	notes := strings.Join(r.state.Progress, "\n")
	instruction += "\n目标：" + r.state.Spec.Goal + "\n已记录进展：\n" + notes
	reply, err := e.generate(ctx, r, []schema.Message{system, {Role: schema.RoleUser, Content: instruction}}, nil)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(reply.Content)
	if strings.HasPrefix(body, "```") {
		if idx := strings.IndexByte(body, '\n'); idx >= 0 {
			body = strings.TrimSpace(strings.TrimSuffix(body[idx+1:], "```"))
		}
	}
	var steps []planStep
	if err := json.Unmarshal([]byte(body), &steps); err != nil {
		return nil, fmt.Errorf("计划格式无效: %w", err)
	}
	for i := range steps {
		steps[i].Done = false
	}
	steps = append(frozen, steps...)
	if err := checkPlan(steps); err != nil {
		return nil, err
	}
	return steps, nil
}

func planViews(state taskState) (string, string) {
	var plan, todo strings.Builder
	fmt.Fprintf(&plan, "# 任务计划\n\n任务 %s\n\n%s\n\n", state.Spec.ID, state.Spec.Goal)
	todo.WriteString("# 执行进度\n\n")
	for _, step := range state.Plan {
		mark := " "
		if step.Done {
			mark = "x"
		}
		fmt.Fprintf(&todo, "- [%s] %s — %s\n", mark, step.ID, step.Goal)
		fmt.Fprintf(&plan, "## %s\n\n%s\n\n", step.ID, step.Goal)
		if len(step.DependsOn) > 0 {
			fmt.Fprintf(&plan, "依赖 %s\n\n", strings.Join(step.DependsOn, "、"))
		}
	}
	return plan.String(), todo.String()
}
