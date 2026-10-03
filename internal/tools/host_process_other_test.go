//go:build !linux && !darwin

package tools

import (
	"fmt"
	"os/exec"
)

func configureProcess(*exec.Cmd) error { return fmt.Errorf("当前平台尚未支持进程组回收") }
func killProcessGroup(*exec.Cmd) error { return nil }
