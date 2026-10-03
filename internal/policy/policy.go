// Package policy 独立验证工具权限，不从模型或外部文本读取授权。
package policy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type Capability string

const (
	ReadWorkspace  Capability = "workspace_read"
	WriteWorkspace Capability = "workspace_write"
	ExecuteProcess Capability = "process_execute"
	Network        Capability = "network"
	Delegate       Capability = "delegate"
)

var (
	ErrDenied              = errors.New("工具权限不足")
	ErrApprovalUnavailable = errors.New("没有可信审批来源")
	ErrApprovalRejected    = errors.New("审批被拒绝或已失效")
)

// Policy 是装配时复制的能力集合，不允许工具参数修改。
type Policy struct{ allowed map[Capability]bool }

func New(capabilities ...Capability) Policy {
	p := Policy{allowed: make(map[Capability]bool)}
	for _, capability := range capabilities {
		p.allowed[capability] = true
	}
	return p
}

func ReadOnly() Policy { return New(ReadWorkspace) }
func Development(network bool) Policy {
	p := New(ReadWorkspace, WriteWorkspace, ExecuteProcess, Delegate)
	if network {
		p.allowed[Network] = true
	}
	return p
}

func (p Policy) Allows(capability Capability) bool { return p.allowed[capability] }

type limitKey struct{}

// Restrict 只缩小当前 context 的能力，即使注册表更宽也不能扩大子任务权限。
func Restrict(ctx context.Context, limit Policy) context.Context {
	copy := New()
	for capability := range limit.allowed {
		copy.allowed[capability] = true
	}
	limit = copy
	if previous, ok := ctx.Value(limitKey{}).(Policy); ok {
		for capability := range limit.allowed {
			if !previous.Allows(capability) {
				delete(limit.allowed, capability)
			}
		}
	}
	return context.WithValue(ctx, limitKey{}, limit)
}

func (p Policy) Check(ctx context.Context, capabilities []Capability) error {
	if len(capabilities) == 0 {
		return fmt.Errorf("%w: 工具没有声明能力", ErrDenied)
	}
	limit, limited := ctx.Value(limitKey{}).(Policy)
	for _, capability := range capabilities {
		if !p.Allows(capability) || (limited && !limit.Allows(capability)) {
			return fmt.Errorf("%w: %s", ErrDenied, capability)
		}
	}
	return nil
}

func NeedsApproval(capabilities []Capability) bool {
	for _, capability := range capabilities {
		if capability == WriteWorkspace || capability == ExecuteProcess || capability == Network {
			return true
		}
	}
	return false
}

type Identity struct{ RunID, SessionID string }
type identityKey struct{}

func WithIdentity(ctx context.Context, runID, sessionID string) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{RunID: runID, SessionID: sessionID})
}
func IdentityFrom(ctx context.Context) Identity {
	value, _ := ctx.Value(identityKey{}).(Identity)
	return value
}

type Request struct {
	ID string
	Identity
	Tool, ArgumentsDigest, Summary string
	ExpiresAt                      time.Time
}

type Grant struct {
	RequestID, RunID, SessionID, Tool, ArgumentsDigest, Subject string
	ExpiresAt                                                   time.Time
	Allowed                                                     bool
}

type Approver interface {
	Approve(context.Context, Request) (Grant, error)
}

func NewRequest(ctx context.Context, tool string, arguments []byte) (Request, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Request{}, err
	}
	expires := time.Now().Add(time.Minute)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	return Request{ID: hex.EncodeToString(id[:]), Identity: IdentityFrom(ctx), Tool: tool, ArgumentsDigest: Digest(arguments),
		Summary: fmt.Sprintf("%q", string(arguments[:min(len(arguments), 1024)])), ExpiresAt: expires}, nil
}

func GrantFor(request Request, subject string) Grant {
	return Grant{RequestID: request.ID, RunID: request.RunID, SessionID: request.SessionID, Tool: request.Tool,
		ArgumentsDigest: request.ArgumentsDigest, Subject: subject, ExpiresAt: request.ExpiresAt, Allowed: true}
}

func Digest(arguments []byte) string {
	sum := sha256.Sum256(arguments)
	return hex.EncodeToString(sum[:])
}

// Validate 拒绝主体、运行、参数、工具或有效期不匹配的授权。
func Validate(request Request, grant Grant, now time.Time) error {
	if !grant.Allowed || request.RunID == "" || request.SessionID == "" || grant.Subject == "" ||
		grant.RequestID != request.ID || grant.RunID != request.RunID || grant.SessionID != request.SessionID ||
		grant.Tool != request.Tool || grant.ArgumentsDigest != request.ArgumentsDigest ||
		!now.Before(request.ExpiresAt) || !now.Before(grant.ExpiresAt) || grant.ExpiresAt.After(request.ExpiresAt) {
		return ErrApprovalRejected
	}
	return nil
}
