//go:build linux || darwin

package approval

import (
	"context"
	"errors"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestMissingControllingTerminalDeniesApproval(t *testing.T) {
	if os.Getenv("CLAW_APPROVAL_TEST_CHILD") == "1" {
		ctx := policy.WithIdentity(context.Background(), "测试运行", "测试会话")
		request, _ := policy.NewRequest(ctx, "write_file", []byte(`{}`))
		_, err := NewLocal().Approve(ctx, request)
		if !errors.Is(err, policy.ErrApprovalUnavailable) {
			t.Fatalf("没有终端仍允许审批: %v", err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMissingControllingTerminalDeniesApproval$")
	cmd.Env = []string{"CLAW_APPROVAL_TEST_CHILD=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("非交互审批验证失败: %v\n%s", err, out)
	}
}
