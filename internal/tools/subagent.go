/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-13 20:24:18
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-14 11:11:57
 * @FilePath: /go-agent-claw/internal/tools/subagent.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/tools/subagent.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type SubtaskSpec struct {
	Goal             string   `json:"task_prompt"`
	AllowedPaths     []string `json:"allowed_paths,omitempty"`
	ExpectedEvidence string   `json:"expected_evidence,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
}

type EvidenceRef struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Version    string `json:"version"`
	EndLine    int    `json:"end_line,omitempty"`
	ActionID   string `json:"action_id,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
	Quote      string `json:"quote,omitempty"`
}

// ReportFinding 是可按身份读取的完整发现。Status 由引擎核对，不采纳模型自报值。
type ReportFinding struct {
	ID               string        `json:"id"`
	Statement        string        `json:"statement"`
	Context          string        `json:"context,omitempty"`
	Limitations      string        `json:"limitations,omitempty"`
	ScopeRequirement string        `json:"scope_requirement,omitempty"`
	DependsOn        []string      `json:"depends_on,omitempty"`
	Evidence         []EvidenceRef `json:"evidence,omitempty"`
	Status           string        `json:"status,omitempty"`
}

// ReportHandoff 的四个状态具有独立含义，未运行脚本不使有效阅读依据失效。
type ReportHandoff struct {
	ReportID          string                  `json:"report_id"`
	ArtifactID        string                  `json:"artifact_id"`
	OriginRunID       string                  `json:"origin_run_id"`
	Execution         string                  `json:"execution"`
	Citations         string                  `json:"citations"`
	Verification      string                  `json:"verification"`
	StopCode          string                  `json:"stop_code,omitempty"`
	Coverage          *schema.ReadingCoverage `json:"coverage,omitempty"`
	Items             []ReportFinding         `json:"items"`
	Omitted           int                     `json:"omitted"`
	NextAfterID       string                  `json:"next_after_id,omitempty"`
	UnverifiedOmitted int                     `json:"unverified_omitted,omitempty"`
}

type SubtaskReport struct {
	Findings   string               `json:"findings"`
	Evidence   []EvidenceRef        `json:"evidence"`
	Unverified []string             `json:"unverified"`
	Status     string               `json:"status"`
	Coverage   []schema.ReadReceipt `json:"coverage,omitempty"`
	ArtifactID string               `json:"artifact_id,omitempty"`
	Handoff    *ReportHandoff       `json:"handoff,omitempty"`
}

// AgentRunner 避免工具包与引擎包循环依赖。
type AgentRunner interface {
	RunSub(ctx context.Context, spec SubtaskSpec, readOnlyRegistry Registry, reporter any) (SubtaskReport, error)
}

type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
	reporter         any
}

func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry, reporter any) *SubagentTool {
	return &SubagentTool{runner: runner, readOnlyRegistry: readOnlyRegistry, reporter: reporter}
}

func (t *SubagentTool) Name() string { return "spawn_subagent" }

func (t *SubagentTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "委派一个只读探索任务，返回发现、文件证据和未核实事项。", map[string]any{
		"task_prompt":       stringParam("子任务的明确目标"),
		"allowed_paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "允许访问的工作区相对路径，默认继承父任务范围"},
		"expected_evidence": stringParam("需要核对和返回的证据"),
		"max_turns":         intParam("子任务最大轮数，默认 10", 1, 10),
	}, "task_prompt")
}

func parseSubtask(ctx context.Context, args json.RawMessage) (SubtaskSpec, error) {
	spec := SubtaskSpec{MaxTurns: 10}
	if err := decodeArgs(args, &spec, "task_prompt"); err != nil {
		return spec, err
	}
	if strings.TrimSpace(spec.Goal) == "" || spec.MaxTurns < 1 || spec.MaxTurns > 10 {
		return spec, reject("参数错误", fmt.Errorf("请提供子任务目标和有效轮数（1 至 10）"))
	}
	for i, raw := range spec.AllowedPaths {
		path, err := filePath(ctx, raw, false)
		if err != nil {
			return spec, err
		}
		spec.AllowedPaths[i] = path
	}
	return spec, nil
}

func (t *SubagentTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseSubtask(ctx, args)
	return err
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireTool(ctx, t.Name()); err != nil {
		return "", err
	}
	spec, err := parseSubtask(ctx, args)
	if err != nil {
		return "", err
	}
	log.Printf("[子智能体] 开始只读探索")
	report, runErr := t.runner.RunSub(ctx, spec, t.readOnlyRegistry, t.reporter)
	encoded, err := EncodeSubtaskHandoff(report)
	if err != nil {
		return "", err
	}
	if runErr != nil {
		if rejected, ok := runErr.(interface{ DelegationRejected() bool }); ok && rejected.DelegationRejected() {
			retryable := false
			return string(encoded), &executionError{status: schema.ToolRejected, effect: schema.EffectNone, kind: "委派未准入", code: "delegation_not_admitted", retryable: &retryable, err: runErr}
		}
		return string(encoded), fmt.Errorf("子智能体执行失败: %w", runErr)
	}
	return string(encoded), nil
}

// EncodeSubtaskHandoff 按完整条目缩减，任何返回都是完整 JSON；完整材料仍在 ArtifactID。
func EncodeSubtaskHandoff(report SubtaskReport) ([]byte, error) {
	if report.Handoff == nil {
		return json.Marshal(report) // 非目标修改任务及旧外部Runner保持原返回行为。
	}
	copy := report
	handoff := *report.Handoff
	copy.Handoff = &handoff
	handoff.Items = append([]ReportFinding(nil), handoff.Items...)
	copy.Coverage = nil
	copy.Evidence = nil
	copy.Findings = "结构化子报告已保存，请优先复用目录；需要细节时使用 read_report。"
	if handoff.Coverage != nil {
		c := *handoff.Coverage
		c.Files = nil
		handoff.Coverage = &c
	}
	// 限制项不能靠截断字符串变成另一条陈述，仅按整项省略。
	copy.Unverified = append([]string(nil), report.Unverified...)
	for {
		handoff.NextAfterID = ""
		if handoff.Omitted > 0 && len(handoff.Items) > 0 {
			handoff.NextAfterID = handoff.Items[len(handoff.Items)-1].ID
		}
		encoded, err := json.Marshal(copy)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= 8192 {
			return encoded, nil
		}
		if len(copy.Unverified) > 0 {
			copy.Unverified = copy.Unverified[:len(copy.Unverified)-1]
			handoff.UnverifiedOmitted++
			continue
		}
		if len(handoff.Items) > 0 {
			handoff.Items = handoff.Items[:len(handoff.Items)-1]
			handoff.Omitted++
			continue
		}
		return nil, fmt.Errorf("子报告元数据超过 8 KiB，请通过材料引用读取")
	}
}
