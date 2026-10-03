// Package approval 提供受信任的本地审批来源。
package approval

import (
	"context"
	"fmt"
	"os"

	"github.com/dk264874293/go-agent-claw/internal/policy"
)

type Local struct {
	gate    chan struct{}
	verify  func(context.Context, policy.Request) (bool, error)
	subject string
}

func NewLocal() *Local {
	return &Local{gate: make(chan struct{}, 1), verify: terminalPrompt, subject: fmt.Sprintf("local-uid:%d", os.Getuid())}
}

func (l *Local) Approve(ctx context.Context, request policy.Request) (policy.Grant, error) {
	if request.RunID == "" || request.SessionID == "" || request.ID == "" {
		return policy.Grant{}, policy.ErrApprovalRejected
	}
	ctx, cancel := context.WithDeadline(ctx, request.ExpiresAt)
	defer cancel()
	select {
	case l.gate <- struct{}{}:
		defer func() { <-l.gate }()
	case <-ctx.Done():
		return policy.Grant{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return policy.Grant{}, err
	}
	allowed, err := l.verify(ctx, request)
	if err != nil {
		return policy.Grant{}, err
	}
	if !allowed {
		return policy.Grant{}, policy.ErrApprovalRejected
	}
	if err := ctx.Err(); err != nil {
		return policy.Grant{}, err
	}
	return policy.GrantFor(request, l.subject), nil
}
