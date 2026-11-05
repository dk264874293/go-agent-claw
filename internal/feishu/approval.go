package feishu

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type approvalTarget struct {
	ChatID   string
	ActorID  string
	ActionID string
	Tool     string
	Path     string
	Before   string
	After    string
	Digest   [32]byte
}

type pendingApproval struct {
	target    approvalTarget
	approvers []string
	expires   time.Time
	decision  chan bool
}

type approvalManager struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

func (m *approvalManager) wait(ctx context.Context, target approvalTarget, approvers []string, notify func(context.Context, string, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	expiry, _ := ctx.Deadline()
	id := rand.Text()
	entry := pendingApproval{target: target, approvers: approvers, expires: expiry, decision: make(chan bool, 1)}
	m.mu.Lock()
	if m.pending == nil {
		m.pending = make(map[string]pendingApproval)
	}
	m.pending[id] = entry
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()

	if err := notify(ctx, id, expiry); err != nil {
		return fmt.Errorf("无法发送审批请求: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case allowed := <-entry.decision:
		if err := ctx.Err(); err != nil {
			return err
		}
		if !allowed {
			return errors.New("审批已拒绝")
		}
		return nil
	}
}

func (m *approvalManager) resolve(chatID, approver, id string, allow bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.pending[id]
	if !ok || entry.target.ChatID != chatID || !slices.Contains(entry.approvers, approver) || !time.Now().Before(entry.expires) {
		return false
	}
	delete(m.pending, id)
	entry.decision <- allow
	return true
}

type sendMessage func(context.Context, string, string) error

func (m *approvalManager) guard(chatID, actorID string, binding ChatBinding, send sendMessage) tools.MiddlewareFunc {
	return func(ctx context.Context, call schema.ToolCall) (bool, string) {
		scope := tools.ScopeFromContext(ctx)
		if scope.WorkDir != binding.WorkDir || scope.Mode != binding.Mode {
			return false, "动作的工作区或权限不符合群聊配置"
		}
		if tools.IsReadOnly(call.Name) || call.Name == "spawn_subagent" {
			return true, ""
		}
		if err := ctx.Err(); err != nil {
			return false, "任务已取消或到期"
		}
		if len(call.Arguments) > 8000 {
			return false, "审批参数过长，请缩小单次动作以便人工核对"
		}
		actionID := engine.ActionID(ctx)
		if actionID == "" || len(binding.Approvers) == 0 {
			return false, "动作缺少审批身份或可用审批人"
		}
		target, err := makeApprovalTarget(ctx, chatID, actorID, binding.WorkDir, actionID, call)
		if err != nil {
			return false, err.Error()
		}
		err = m.wait(ctx, target, binding.Approvers, func(notifyCtx context.Context, id string, expiry time.Time) error {
			message := fmt.Sprintf("需要审批 %s\n发起人 %s\n动作 %s · %s\n对象 %s\n当前版本 %s\n预期版本 %s\n参数摘要 %x\n执行参数\n%s\n剩余有效期 %s\n回复 approve %s 或 reject %s",
				id, actorID, actionID, target.Tool, target.Path, target.Before, target.After, target.Digest, call.Arguments,
				time.Until(expiry).Round(time.Second), id, id)
			return send(notifyCtx, chatID, message)
		})
		if err != nil {
			return false, err.Error()
		}
		current, err := makeApprovalTarget(ctx, chatID, actorID, binding.WorkDir, actionID, call)
		if err != nil || target != current {
			return false, "审批后动作参数或对象版本已变化，请重新审批"
		}
		return true, ""
	}
}

func makeApprovalTarget(ctx context.Context, chatID, actorID, workDir, actionID string, call schema.ToolCall) (approvalTarget, error) {
	result := approvalTarget{ChatID: chatID, ActorID: actorID, ActionID: actionID, Tool: call.Name}
	switch call.Name {
	case "write_file", "edit_file":
		preview, err := tools.PreviewWrite(ctx, call)
		if err != nil {
			return result, err
		}
		result.Path, result.Before, result.After = preview.Path, preview.Before, preview.After
	case "bash":
		version, err := engine.WorkspaceVersion(ctx, workDir)
		if err != nil {
			return result, fmt.Errorf("无法核对命令的工作区版本: %w", err)
		}
		result.Path, result.Before, result.After = workDir, version, "由执行结果确认"
	default:
		return result, fmt.Errorf("工具 %s 未配置飞书审批规则", call.Name)
	}
	material, err := json.Marshal(struct {
		Target approvalTarget
		Args   json.RawMessage
	}{result, call.Arguments})
	if err != nil {
		return result, fmt.Errorf("无法生成动作摘要: %w", err)
	}
	result.Digest = sha256.Sum256(material)
	return result, nil
}
