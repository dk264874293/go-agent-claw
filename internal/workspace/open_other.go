//go:build !linux && !darwin

package workspace

import (
	"fmt"
	"os"
)

func openNonblocking(*os.Root, string) (*os.File, error) {
	return nil, fmt.Errorf("当前平台尚未支持受限文件访问")
}
