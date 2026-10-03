// Package workspace 提供根目录约束的文件操作，不执行宿主命令。
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxEditBytes = 1024 * 1024

type Root struct{ root *os.Root }

// Path 限定跨平台相对路径；实际链接解析仍交给 os.Root。
func Path(name string, directory bool) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\:\x00") || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("路径必须是工作区内的相对路径")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return "", fmt.Errorf("路径不允许包含 ..")
		}
		if part == ".git" || part == ".env" || strings.HasPrefix(part, ".env.") {
			return "", fmt.Errorf("禁止访问密钥文件或 Git 内部目录")
		}
	}
	clean := filepath.Clean(name)
	if clean == "." && !directory {
		return "", fmt.Errorf("路径必须指向文件")
	}
	return clean, nil
}

func Open(dir string) (*Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("打开工作区失败: %w", err)
	}
	return &Root{root: r}, nil
}

func (r *Root) Close() error { return r.root.Close() }

// Read 以非阻塞方式打开后确认文件类型，并只读 limit+1 字节检测截断。
func (r *Root) Read(ctx context.Context, name string, limit int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	name, err := Path(name, false)
	if err != nil || limit <= 0 || limit > MaxEditBytes {
		if err == nil {
			err = fmt.Errorf("读取上限无效")
		}
		return nil, false, err
	}
	f, err := openNonblocking(r.root, name)
	if err != nil {
		return nil, false, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("只允许读取常规文件")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return data, truncated, nil
}

func (r *Root) OpenDir(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := Path(name, true)
	if err != nil {
		return nil, err
	}
	f, err := openNonblocking(r.root, name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		return nil, fmt.Errorf("路径不是目录")
	}
	return f, nil
}

// Write 通过临时常规文件替换目标，避免截断原文件及跟随目标链接。
func (r *Root) Write(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := Path(name, false)
	if err != nil {
		return err
	}
	if len(data) > MaxEditBytes {
		return fmt.Errorf("单次写入不能超过 %d 字节", MaxEditBytes)
	}
	mode := os.FileMode(0644)
	info, err := r.root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("只允许替换常规文件，拒绝符号链接和特殊文件")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标文件失败: %w", err)
	}
	if err := r.root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := ".claw-write-" + hex.EncodeToString(random[:])
	f, err := r.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.root.Remove(temp)
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.root.Rename(temp, name); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}
	return nil
}
