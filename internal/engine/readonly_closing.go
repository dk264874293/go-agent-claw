package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// ReadonlyFailureBoundary 只记录根任务已确认的局部目录类型错误，不从旧中文日志推导。
type ReadonlyFailureBoundary struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Tool         string `json:"tool"`
	Path         string `json:"path"`
	StepID       string `json:"step_id,omitempty"`
	ErrorCode    string `json:"error_code"`
	Count        int    `json:"count"`
	LastActionID string `json:"last_action_id"`
	Stopped      bool   `json:"stopped"`
	Resolved     bool   `json:"resolved"`
}

// ReadonlyClosing 与 BudgetClosing 各自保存边界，沿用相同的禁工具请求流程。
type ReadonlyClosing struct {
	OriginRunID string `json:"origin_run_id"`
	Status      string `json:"status"`
	Text        string `json:"text,omitempty"`
	ErrorKind   string `json:"error_kind,omitempty"`
}

type readonlyRepeatStop struct{ Boundary ReadonlyFailureBoundary }

func (s readonlyRepeatStop) Error() string {
	return fmt.Sprintf("%s：只读工具 %s 对路径 %q 已失败 %d 次，停止继续探索", errBlocked, s.Boundary.Tool, s.Boundary.Path, s.Boundary.Count)
}
func (s readonlyRepeatStop) Unwrap() error { return errBlocked }

type readonlyClosingKey struct{}

var errClosingIneligible = errors.New("当前停止原因不满足只读收尾条件")

func (r *runState) unresolvedReadonlyBoundary(id string) *ReadonlyFailureBoundary {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.state.ReadonlyFailures {
		if b.ID == id && b.Stopped && !b.Resolved {
			copy := *b
			return &copy
		}
	}
	return nil
}

func readonlyTask(ctx context.Context, s *taskState) bool {
	scope := tools.ScopeFromContext(ctx)
	return s.Spec.Kind == TaskAnswer && s.Spec.Mode == tools.ModeReadOnly && scope.Mode == tools.ModeReadOnly && !scope.DisableSubagent
}

func readonlyPath(call schema.ToolCall) (string, bool) {
	if call.Name != "list_dir" {
		return "", false
	}
	args := struct {
		Path string `json:"path"`
	}{Path: "."}
	if json.Unmarshal(call.Arguments, &args) != nil || args.Path == "" || !filepath.IsLocal(args.Path) {
		return "", false
	}
	return filepath.Clean(args.Path), true
}

// noteReadonlyResult 在整个批次结果已经返回后更新稳定边界；主循环负责把全部观察配对回会话。
func (r *runState) noteReadonlyResult(ctx context.Context, stepID string, call schema.ToolCall, result schema.ToolResult) (*ReadonlyFailureBoundary, error) {
	path, ok := readonlyPath(call)
	if !ok {
		return nil, nil
	}
	r.mu.Lock()
	eligible := readonlyTask(ctx, &r.state)
	ref := r.actionRefs[observe.Identity(ctx).AgentID+"\x00"+call.ID]
	hasBoundary := false
	for _, b := range r.state.ReadonlyFailures {
		hasBoundary = hasBoundary || (!b.Resolved && b.Path == path && b.StepID == stepID)
	}
	r.mu.Unlock()
	if !eligible {
		return nil, nil
	}
	if result.Status == schema.ToolSucceeded {
		if !hasBoundary {
			return nil, nil
		}
		return nil, r.record("readonly_failure_resolved", func(s *taskState) error {
			for _, b := range s.ReadonlyFailures {
				if !b.Resolved && b.Path == path && b.StepID == stepID {
					b.Resolved = true
					if s.ReadonlyStopID == b.ID {
						s.ReadonlyStopID = ""
					}
				}
			}
			return nil
		})
	}
	if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || result.ErrorCode != tools.ErrPathNotDirectory || result.Retryable == nil || *result.Retryable || ref == "" {
		return nil, nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(stepID+"\x00"+call.Name+"\x00"+path+"\x00"+result.ErrorCode)))
	var boundary ReadonlyFailureBoundary
	err := r.record("readonly_failure", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if s.ReadonlyFailures == nil {
			s.ReadonlyFailures = make(map[string]*ReadonlyFailureBoundary)
		}
		b := s.ReadonlyFailures[key]
		if b == nil || b.Resolved {
			b = &ReadonlyFailureBoundary{ID: newID(), Key: key, Tool: call.Name, Path: path, StepID: stepID, ErrorCode: result.ErrorCode}
			s.ReadonlyFailures[key] = b
		}
		b.Count++
		b.LastActionID = ref
		b.Stopped = b.Count >= 3
		boundary = *b
		return nil
	})
	return &boundary, err
}

// resumedReadonlyStop 只核对相关路径的类型，不扫描工作区，也不重放模型或工具。
func (r *runState) resumedReadonlyStop(ctx context.Context) error {
	r.mu.Lock()
	if !readonlyTask(ctx, &r.state) {
		r.mu.Unlock()
		return nil
	}
	var boundaries []ReadonlyFailureBoundary
	for _, b := range r.state.ReadonlyFailures {
		if b.ID == r.state.ReadonlyStopID && b.Stopped && !b.Resolved {
			boundaries = append(boundaries, *b)
		}
	}
	r.mu.Unlock()
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Key < boundaries[j].Key })
	for _, b := range boundaries {
		directory, err := tools.PathIsDirectory(ctx, b.Path)
		if directory || errors.Is(err, fs.ErrNotExist) {
			if err := r.record("readonly_failure_resolved", func(s *taskState) error {
				s.ReadonlyFailures[b.Key].Resolved = true
				if s.ReadonlyStopID == b.ID {
					s.ReadonlyStopID = ""
				}
				return nil
			}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w：无法核实停止路径 %q 的当前类型", errBlocked, b.Path)
		}
		if err := r.record("readonly_failure", func(s *taskState) error { s.ReadonlyStopID = b.ID; return nil }); err != nil {
			return err
		}
		return readonlyRepeatStop{Boundary: b}
	}
	return nil
}

func (r *runState) readonlyBoundaryForStop(ctx context.Context) *ReadonlyFailureBoundary {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !readonlyTask(ctx, &r.state) || r.fatalStorage != nil || len(r.state.Pending) != 0 {
		return nil
	}
	if r.state.EffectLedger != nil {
		for _, effect := range r.state.EffectLedger.Effects {
			if effect.Effect == schema.EffectUnknown {
				return nil
			}
		}
	}
	var selected *ReadonlyFailureBoundary
	for _, b := range r.state.ReadonlyFailures {
		if b.ID == r.state.ReadonlyStopID && b.Stopped && !b.Resolved && b.Tool == "list_dir" && b.ErrorCode == tools.ErrPathNotDirectory && b.LastActionID != "" {
			copy := *b
			selected = &copy
		}
	}
	return selected
}

func (r *runState) readonlyClosing(id string) *ReadonlyClosing {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.state.ReadonlyClosings[id]; c != nil {
		copy := *c
		return &copy
	}
	return nil
}

func (r *runState) reserveReadonlyClosing(ctx context.Context, id string) error {
	return r.record("readonly_closing_started", func(s *taskState) error {
		if r.fatalStorage != nil {
			return r.fatalStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !readonlyTask(ctx, s) || len(s.Pending) != 0 {
			return errClosingIneligible
		}
		if s.EffectLedger != nil {
			for _, effect := range s.EffectLedger.Effects {
				if effect.Effect == schema.EffectUnknown {
					return errClosingIneligible
				}
			}
		}
		var boundary *ReadonlyFailureBoundary
		for _, b := range s.ReadonlyFailures {
			if b.ID == id && b.Stopped && !b.Resolved && b.Tool == "list_dir" && b.ErrorCode == tools.ErrPathNotDirectory && b.Count >= 3 && b.LastActionID != "" {
				boundary = b
				break
			}
		}
		if boundary == nil {
			return errClosingIneligible
		}
		if s.ReadonlyClosings[id] != nil {
			return errClosingUsed
		}
		if s.Usage.ModelCalls >= s.Limits.MaxModelCalls {
			return budgetLimit{code: "model_limit", label: "模型调用次数"}
		}
		if s.Usage.Turns >= s.Limits.MaxTurns {
			return budgetLimit{code: "turn_limit", label: "主循环轮数"}
		}
		elapsed := r.elapsed
		if !r.started.IsZero() {
			elapsed += time.Since(r.started)
		}
		if elapsed >= s.Limits.Timeout {
			return context.DeadlineExceeded
		}
		if s.ReadonlyClosings == nil {
			s.ReadonlyClosings = make(map[string]*ReadonlyClosing)
		}
		s.Usage.ModelCalls++
		s.Usage.Turns++
		s.ReadonlyClosings[id] = &ReadonlyClosing{OriginRunID: r.runID, Status: "started"}
		return nil
	})
}

func (e *AgentEngine) partialForReadonlyStop(ctx context.Context, r *runState, session *ctxpkg.Session, system schema.Message, result RunResult) (*PartialReport, error) {
	b := r.readonlyBoundaryForStop(ctx)
	if b == nil || ctx.Err() != nil {
		return r.recordedPartial(result.Reason, ""), nil
	}
	if c := r.readonlyClosing(b.ID); c != nil {
		if c.Status == "completed" && strings.TrimSpace(c.Text) != "" {
			return &PartialReport{Text: c.Text, Source: "model", OriginRunID: c.OriginRunID}, nil
		}
		message := "当前失败边界已有收尾记录，不重新请求。"
		if c.Status == "started" {
			message = "此前只读收尾已准入但结果未确认，不重新请求。"
		}
		return r.recordedPartial(result.Reason, message), nil
	}
	closingCtx := context.WithValue(ctx, readonlyClosingKey{}, b.ID)
	msg, requestErr := e.requestPartial(closingCtx, r, session, system, "只读探索重复失败，现在停止继续探索并汇总已有证据。停止原因："+result.Reason)
	if errors.Is(requestErr, errStorage) {
		return r.recordedPartial(result.Reason, "收尾记录未可靠保存。"), requestErr
	}
	if r.readonlyClosing(b.ID) == nil {
		return r.recordedPartial(result.Reason, "剩余额度、时间或上下文不足，未调用模型收尾。"), nil
	}
	text, kind := partialResponse(msg, requestErr)
	if err := r.record("readonly_closing_finished", func(s *taskState) error {
		c := s.ReadonlyClosings[b.ID]
		if text != "" {
			c.Status, c.Text = "completed", text
		} else {
			c.Status, c.ErrorKind = "failed", kind
		}
		return nil
	}); err != nil {
		return r.recordedPartial(result.Reason, "收尾结果未可靠保存。"), err
	}
	if text != "" {
		return &PartialReport{Text: text, Source: "model", OriginRunID: r.runID}, nil
	}
	return r.recordedPartial(result.Reason, "模型未产生合格汇总，已采用记录摘要（"+kind+"）。"), nil
}
