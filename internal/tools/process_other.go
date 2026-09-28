//go:build !linux && !darwin

package tools

import (
	"fmt"
	"os"
	"os/exec"
)

func configureProcess(*exec.Cmd) error { return fmt.Errorf("当前平台尚未支持进程组回收") }
func killProcessGroup(*exec.Cmd) error { return nil }
func openRegular(*os.Root, string) (*os.File, error) {
	return nil, fmt.Errorf("当前平台尚未支持非阻塞文件读取")
}
