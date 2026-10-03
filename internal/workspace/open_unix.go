//go:build linux || darwin

package workspace

import (
	"os"
	"syscall"
)

func openNonblocking(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
