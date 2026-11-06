package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// ErrPathNotDirectory 是路径类型错误的稳定错误码，不依赖操作系统报错文字。
const ErrPathNotDirectory = "path_not_directory"

func rejectPathNotDirectory(path string) error {
	retryable := false
	return &executionError{
		status: schema.ToolRejected, effect: schema.EffectNone, kind: "路径类型错误",
		code: ErrPathNotDirectory, retryable: &retryable,
		err: fmt.Errorf("路径不是目录: %s。请跳过该文件，或按任务需要使用 read_file；不要对同一路径重复调用 list_dir", path),
	}
}

// directoryReadError 也处理类型检查后目录被替换为文件的情况。
func directoryReadError(path string, err error) error {
	if errors.Is(err, syscall.ENOTDIR) {
		return rejectPathNotDirectory(path)
	}
	return rootError(err)
}

func directoryInfo(ctx context.Context, root *os.Root, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := root.Stat(path)
	if err != nil {
		return false, rootError(err)
	}
	if err := checkPathLinks(root, path, false); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// PathIsDirectory 在当前工作区和权限内观察路径类型，不缓存，也不替代实际调用时的校验。
// 现存非目录返回 false,nil；缺失返回可用 errors.Is(err,fs.ErrNotExist) 判断的错误。
func PathIsDirectory(ctx context.Context, rawPath string) (bool, error) {
	path, err := filePath(ctx, rawPath, true)
	if err != nil {
		return false, err
	}
	root, err := openRoot(ctx, "")
	if err != nil {
		return false, err
	}
	defer root.Close()
	resolved, err := resolveReadPath(ctx, root, path, true)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return false, nil
		}
		return false, err
	}
	isDirectory, err := directoryInfo(ctx, root, resolved)
	if errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return isDirectory, err
}
