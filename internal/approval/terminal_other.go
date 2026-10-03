//go:build !linux && !darwin

package approval

import (
	"context"
	"github.com/dk264874293/go-agent-claw/internal/policy"
)

func terminalPrompt(context.Context, policy.Request) (bool, error) {
	return false, policy.ErrApprovalUnavailable
}
