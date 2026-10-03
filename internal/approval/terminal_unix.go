//go:build linux || darwin

package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/policy"
)

func terminalPrompt(ctx context.Context, request policy.Request) (bool, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, policy.ErrApprovalUnavailable
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "\n工具审批 | RunID: %s | 工具: %s\n参数摘要: %s\n参数 SHA-256: %s\n输入 yes 批准本次调用，其他输入拒绝: ", request.RunID, request.Tool, request.Summary, request.ArgumentsDigest); err != nil {
		return false, err
	}
	var line []byte
	var buffer [256]byte
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := syscall.Read(int(file.Fd()), buffer[:])
		if n > 0 {
			line = append(line, buffer[:n]...)
			if len(line) > 1024 {
				return false, policy.ErrApprovalRejected
			}
			if strings.Contains(string(line), "\n") {
				return strings.TrimSpace(string(line)) == "yes", nil
			}
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return false, err
		}
		if n == 0 && err == nil {
			return false, policy.ErrApprovalRejected
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
