package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const (
	maxDelegatedTasks       = 2
	smallTaskFiles          = 32
	smallTaskBytes    int64 = 128 * 1024
	subtaskModelLimit       = 4
	subtaskToolLimit        = 8
)

// DelegationState 的累计准入和局部用量属于任务账本，恢复时不能重置。
type DelegationState struct {
	Version         int                          `json:"version"`
	HistoryComplete bool                         `json:"history_complete"`
	Subtasks        map[string]*SubtaskAdmission `json:"subtasks,omitempty"`
}

type SubtaskAdmission struct {
	AgentID     string   `json:"agent_id"`
	ParentID    string   `json:"parent_agent_id"`
	OriginRunID string   `json:"origin_run_id"`
	Paths       []string `json:"paths"`
	ModelCalls  int      `json:"model_calls"`
	ToolCalls   int      `json:"tool_calls"`
	Status      string   `json:"status"`
}

// subtaskLimit 只结束当前子任务，不能包装成根任务的 errBudget。
type subtaskLimit struct{ kind string }

func (e subtaskLimit) SubtaskLimitCode() string { return "subtask_" + e.kind + "_limit" }

func (e subtaskLimit) Error() string {
	if e.kind == "model" {
		return "子任务模型额度已耗尽（最多4次），已有读取和报告仍可交付"
	}
	return "子任务工具额度已耗尽（最多8次），已有读取和报告仍可交付"
}

func readonlyAnswer(spec TaskSpec) bool {
	return spec.Mode == tools.ModeReadOnly && spec.Kind == TaskAnswer
}

func initializeDelegation(spec *TaskSpec) (*DelegationState, error) {
	if !readonlyAnswer(*spec) {
		if spec.Delegation != "" {
			return nil, errors.New("委派策略仅适用于 readonly + answer 任务")
		}
		return nil, nil
	}
	if spec.Delegation == "" {
		spec.Delegation = "auto"
	}
	if spec.Delegation != "auto" && spec.Delegation != "off" && spec.Delegation != "on" {
		return nil, errors.New("委派策略必须是 auto、off 或 on")
	}
	return &DelegationState{Version: 1, HistoryComplete: true, Subtasks: make(map[string]*SubtaskAdmission)}, nil
}

func completeLargeManifest(d schema.DiscoveryReceipt) bool {
	if d.Truncated || d.Unreadable > 0 || len(d.Files) == 0 {
		return false
	}
	var total int64
	for _, file := range d.Files {
		if file.Bytes < 0 {
			return false
		}
		if file.Bytes > smallTaskBytes {
			return true
		}
		total += file.Bytes
		if total > smallTaskBytes {
			return true
		}
	}
	return len(d.Files) > smallTaskFiles
}

type delegationRejection struct{ reason string }

func (e delegationRejection) Error() string            { return "委派未准入：" + e.reason }
func (e delegationRejection) DelegationRejected() bool { return true }
func delegationDenied(reason string) error             { return delegationRejection{reason: reason} }

// delegationVisible 与实际准入共用规模条件；未知范围不能靠自然语言猜测规模。
func (r *runState) delegationVisible(ctx context.Context) bool {
	if tools.ScopeFromContext(ctx).DisableSubagent {
		return false
	}
	r.mu.Lock()
	spec, ledger := r.state.Spec, r.state.Delegation
	legacy := !readonlyAnswer(spec)
	available := ledger != nil && ledger.Version == 1 && ledger.HistoryComplete && len(ledger.Subtasks) < maxDelegatedTasks
	r.mu.Unlock()
	if legacy {
		return true
	}
	if !available || spec.Delegation == "off" {
		return false
	}
	if spec.Delegation == "on" {
		return true
	}
	if spec.Delegation != "auto" {
		return false
	}
	d := r.primaryDiscovery(ctx)
	return d != nil && completeLargeManifest(*d)
}

func canonicalSubtaskPaths(scope tools.ExecutionScope, requested []string) ([]string, error) {
	if len(requested) == 0 {
		requested = scope.AllowedPaths
		if len(requested) == 0 {
			requested = []string{"."}
		}
	}
	var result []string
	for _, raw := range requested {
		path := filepath.ToSlash(filepath.Clean(raw))
		if !filepath.IsLocal(path) || !insideParentPaths(path, scope.AllowedPaths) {
			return nil, delegationDenied("路径超出父任务范围")
		}
		if !containsString(result, path) {
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result, nil
}

func expandDelegationPaths(paths []string, manifest schema.DiscoveryReceipt) ([]string, bool) {
	var selected []string
	for _, path := range paths {
		matched := false
		for _, file := range manifest.Files {
			if file.Path == path || strings.HasPrefix(file.Path, path+"/") || path == "." {
				matched = true
				if !containsString(selected, file.Path) {
					selected = append(selected, file.Path)
				}
			}
		}
		if !matched {
			return nil, false
		}
	}
	sort.Strings(selected)
	return selected, len(selected) > 0
}

func pathsIntersect(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if left == "." || right == "." || left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
				return true
			}
		}
	}
	return false
}

// admitSubtask 必须使用父身份调用，成功后调用方再切换为 childID。
func (r *runState) admitSubtask(ctx context.Context, spec tools.SubtaskSpec, childID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scope := tools.ScopeFromContext(ctx)
	if scope.DisableSubagent {
		return delegationDenied("子任务不允许再次委派")
	}
	r.mu.Lock()
	task := r.state.Spec
	r.mu.Unlock()
	if !readonlyAnswer(task) {
		return nil
	} // 非目标任务沿用原权限与预算机制。
	if childID == "" {
		return delegationDenied("缺少子任务身份")
	}
	if !r.delegationVisible(ctx) {
		return delegationDenied("当前策略关闭委派、范围未知、任务较小或累计子任务已达到2个；请复用已有材料由根任务继续")
	}
	paths, err := canonicalSubtaskPaths(scope, spec.AllowedPaths)
	if err != nil {
		return err
	}
	if task.Delegation == "auto" {
		if len(spec.AllowedPaths) == 0 || strings.TrimSpace(spec.ExpectedEvidence) == "" {
			return delegationDenied("自动委派必须提供 allowed_paths 和 expected_evidence")
		}
		var selected []string
		if manifest := r.primaryDiscovery(ctx); manifest != nil && completeLargeManifest(*manifest) {
			if expanded, ok := expandDelegationPaths(paths, *manifest); ok {
				selected = expanded
			}
		}
		if len(selected) == 0 {
			return delegationDenied("委派范围不属于已完整发现的大任务清单")
		}
		paths = selected
		coverage := r.coverageForPaths(ctx, paths)
		if coverage != nil {
			for _, file := range coverage.Files {
				if file.Complete {
					return delegationDenied("范围包含已经完整读取的文件 " + file.Path + "；请缩小到待读范围")
				}
			}
		}
	}
	return r.record("subtask_admitted", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		ledger := s.Delegation
		if ledger == nil || ledger.Version != 1 || !ledger.HistoryComplete {
			return delegationDenied("历史委派计数未知，不自动开启新的委派")
		}
		if len(ledger.Subtasks) >= maxDelegatedTasks {
			return delegationDenied("本任务累计子任务已达到2个，恢复不会重置")
		}
		if s.Spec.Delegation == "off" {
			return delegationDenied("策略为 off")
		}
		if _, ok := ledger.Subtasks[childID]; ok {
			return delegationDenied("子任务身份重复")
		}
		if s.Spec.Delegation == "auto" {
			for _, admitted := range ledger.Subtasks {
				if pathsIntersect(paths, admitted.Paths) {
					return delegationDenied("自动委派范围与已准入子任务重叠")
				}
			}
		}
		if ledger.Subtasks == nil {
			ledger.Subtasks = make(map[string]*SubtaskAdmission)
		}
		ledger.Subtasks[childID] = &SubtaskAdmission{AgentID: childID, ParentID: observe.Identity(ctx).AgentID, OriginRunID: r.runID, Paths: append([]string(nil), paths...), Status: "admitted"}
		return nil
	})
}

// 调用方已持有 r.mu；必须先检查局部和共享额度，成功后再同时增加两者。
func (r *runState) checkSubBudgetLocked(ctx context.Context, kind string) error {
	ledger := r.state.Delegation
	if ledger == nil {
		return nil
	}
	sub := ledger.Subtasks[observe.Identity(ctx).AgentID]
	if sub == nil {
		return nil
	}
	// 局部工具额度耗尽后也不能再发模型请求补报告；只交付已经记录的材料。
	if kind == "model" && sub.ToolCalls >= subtaskToolLimit {
		return subtaskLimit{kind: "tool"}
	}
	if kind == "model" && sub.ModelCalls >= subtaskModelLimit {
		return subtaskLimit{kind: kind}
	}
	if kind == "tool" && sub.ToolCalls >= subtaskToolLimit {
		return subtaskLimit{kind: kind}
	}
	return nil
}

func (r *runState) chargeSubBudgetLocked(ctx context.Context, kind string) {
	ledger := r.state.Delegation
	if ledger == nil {
		return
	}
	sub := ledger.Subtasks[observe.Identity(ctx).AgentID]
	if sub == nil {
		return
	}
	if kind == "model" {
		sub.ModelCalls++
	}
	if kind == "tool" {
		sub.ToolCalls++
	}
}

func (r *runState) finishSubtask(ctx context.Context, childID, status string) error {
	r.mu.Lock()
	tracked := r.state.Delegation != nil && r.state.Delegation.Subtasks[childID] != nil
	r.mu.Unlock()
	if !tracked {
		return nil
	}
	return r.record("subtask_finished", func(s *taskState) error {
		if s.Delegation != nil {
			if sub := s.Delegation.Subtasks[childID]; sub != nil {
				sub.Status = status
			}
		}
		return nil
	})
}

func (r *runState) admittedSubtaskPaths(childID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Delegation == nil {
		return nil
	}
	if sub := r.state.Delegation.Subtasks[childID]; sub != nil {
		return append([]string(nil), sub.Paths...)
	}
	return nil
}

func ValidateDelegationState(state taskState) error {
	ledger := state.Delegation
	if ledger == nil {
		return nil
	}
	if !readonlyAnswer(state.Spec) || ledger.Version != 1 || len(ledger.Subtasks) > maxDelegatedTasks {
		return errors.New("委派状态版本、任务模式或数量无效")
	}
	if state.Spec.Delegation != "auto" && state.Spec.Delegation != "off" && state.Spec.Delegation != "on" {
		return errors.New("持久委派策略无效")
	}
	for id, sub := range ledger.Subtasks {
		if sub == nil || id == "" || sub.AgentID != id || sub.OriginRunID == "" || len(sub.Paths) == 0 || sub.ModelCalls < 0 || sub.ModelCalls > subtaskModelLimit || sub.ToolCalls < 0 || sub.ToolCalls > subtaskToolLimit || strings.TrimSpace(sub.Status) == "" {
			return errors.New("子任务准入身份或局部用量无效")
		}
		for _, path := range sub.Paths {
			if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path {
				return errors.New("子任务持久路径无效")
			}
		}
	}
	return nil
}
