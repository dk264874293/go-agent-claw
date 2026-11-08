package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

const (
	MissingVersion   = "missing"
	maxEditableBytes = 16 * 1024 * 1024
)

// 文件更新串行校验版本，避免进程内的竞争覆盖。
var fileWrites sync.Mutex

func filePath(ctx context.Context, raw string, allowAncestor bool) (string, error) {
	path, err := cleanPath(raw)
	if err != nil {
		return "", err
	}
	if protectedRuntimePath(path) || !ScopeFromContext(ctx).allowsPath(path, allowAncestor) {
		return "", reject("路径越界", fmt.Errorf("路径不在本次任务范围内: %s", path))
	}
	return path, nil
}

func protectedRuntimePath(path string) bool {
	path = strings.ToLower(path)
	return pathWithin(path, ".claw") && !pathWithin(path, filepath.Join(".claw", "skills"))
}

func writablePath(ctx context.Context, raw string) (string, error) {
	path, err := filePath(ctx, raw, false)
	if err != nil {
		return "", err
	}
	if path == "." || pathWithin(strings.ToLower(path), ".claw") {
		return "", reject("路径越界", fmt.Errorf("该路径由运行时管理或不是文件: %s", path))
	}
	return path, nil
}

func rootError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Err.Error() == "path escapes from parent" {
		return reject("路径越界", fmt.Errorf("路径指向工作区之外: %w", err))
	}
	return err
}

func openRoot(ctx context.Context, fallback string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := workDir(ctx, fallback)
	if dir == "" {
		return nil, reject("路径越界", fmt.Errorf("未指定工作区"))
	}
	return os.OpenRoot(dir)
}

// 子任务的路径范围比工作区窄，拒绝通过符号链接切换到同工作区的其他子目录。
func checkScopedPath(ctx context.Context, root *os.Root, path string) error {
	if len(ScopeFromContext(ctx).AllowedPaths) == 0 || path == "." {
		return nil
	}
	return checkPathLinks(root, path, false)
}

func checkPathLinks(root *os.Root, path string, allowMissing bool) error {
	current := "."
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return rootError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return reject("路径越界", fmt.Errorf("任务路径不能经过符号链接: %s", current))
		}
	}
	return nil
}

// resolveReadPath 逐段解析相对链接，确保真实路径仍在当前任务范围内。
func resolveReadPath(ctx context.Context, root *os.Root, path string, allowAncestor bool) (string, error) {
	if err := checkScopedPath(ctx, root, path); err != nil {
		return "", err
	}
	parts := strings.Split(path, string(filepath.Separator))
	var resolved []string
	links := 0
	for len(parts) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		component := parts[0]
		parts = parts[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return "", reject("路径越界", fmt.Errorf("符号链接指向工作区外: %s", path))
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		name := filepath.Join(filepath.Join(resolved...), component)
		info, err := root.Lstat(name)
		if err != nil {
			return "", rootError(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, component)
			continue
		}
		links++
		if links > 40 {
			return "", reject("路径越界", fmt.Errorf("符号链接层级过多: %s", path))
		}
		target, err := root.Readlink(name)
		if err != nil {
			return "", rootError(err)
		}
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
			return "", reject("路径越界", fmt.Errorf("不允许读取绝对符号链接: %s", path))
		}
		parts = append(strings.Split(target, string(filepath.Separator)), parts...)
	}
	name := filepath.Join(resolved...)
	if name == "" {
		name = "."
	}
	return filePath(ctx, name, allowAncestor)
}

func openRegular(ctx context.Context, root *os.Root, path string) (*os.File, os.FileInfo, error) {
	path, err := resolveReadPath(ctx, root, path, false)
	if err != nil {
		return nil, nil, err
	}
	info, err := root.Stat(path)
	if err != nil {
		return nil, nil, rootError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, reject("参数错误", fmt.Errorf("路径不是普通文件: %s", path))
	}
	// 即使路径在检查之后被切换成 FIFO，打开也不会阻塞。
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, rootError(err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		if err == nil {
			err = reject("版本冲突", fmt.Errorf("读取期间文件已变化，请重新读取: %s", path))
		}
		return nil, nil, err
	}
	if err := checkPathLinks(root, path, false); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

func contentVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func fileVersion(ctx context.Context, root *os.Root, path string) (string, error) {
	file, _, err := openRegular(ctx, root, path)
	if errors.Is(err, os.ErrNotExist) {
		// 失效链接不能被当作待创建文件。
		if entry, statErr := root.Lstat(path); statErr == nil && entry.Mode()&os.ModeSymlink != 0 {
			return "", reject("路径越界", fmt.Errorf("目标是失效的符号链接: %s", path))
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", rootError(statErr)
		}
		return MissingVersion, nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx, file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileVersion 计算当前文件的内容版本，用于验收和恢复前的核对。
func FileVersion(ctx context.Context, rawPath string) (string, error) {
	path, err := filePath(ctx, rawPath, false)
	if err != nil {
		return "", err
	}
	root, err := openRoot(ctx, "")
	if err != nil {
		return "", err
	}
	defer root.Close()
	return fileVersion(ctx, root, path)
}

type FilePreview struct {
	Path   string
	Before string
	After  string
}

type fileMutation struct {
	preview FilePreview
	content []byte
	perm    os.FileMode
}

func expectedVersion(raw string, allowMissing bool) error {
	if allowMissing && (raw == "" || raw == MissingVersion) {
		return nil
	}
	if _, err := hex.DecodeString(raw); err != nil || len(raw) != sha256.Size*2 || raw != strings.ToLower(raw) {
		return reject("参数错误", fmt.Errorf("expected_version 必须是 read_file 返回的 SHA-256 版本"))
	}
	return nil
}

func requireVersion(expected, actual string) error {
	if expected != actual {
		return reject("版本冲突", fmt.Errorf("文件版本已变化，请重新调用 read_file。当前版本: %s", actual))
	}
	return nil
}

func prepareMutation(ctx context.Context, root *os.Root, name string, args json.RawMessage) (fileMutation, error) {
	var path, before string
	var data []byte
	perm := os.FileMode(0644)
	switch name {
	case "write_file":
		input, err := parseWrite(ctx, args)
		if err != nil {
			return fileMutation{}, err
		}
		path, before, data = input.Path, input.ExpectedVersion, []byte(input.Content)
		if err := checkPathLinks(root, path, true); err != nil {
			return fileMutation{}, err
		}
	case "edit_file":
		input, err := parseEdit(ctx, args)
		if err != nil {
			return fileMutation{}, err
		}
		path, before = input.Path, input.ExpectedVersion
		if err := checkPathLinks(root, path, false); err != nil {
			return fileMutation{}, err
		}
		file, info, err := openRegular(ctx, root, path)
		if err != nil {
			return fileMutation{}, err
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxEditableBytes+1))
		if err != nil {
			return fileMutation{}, err
		}
		if len(content) > maxEditableBytes {
			return fileMutation{}, reject("文件过大", fmt.Errorf("编辑超过 %d 字节的文件不受支持", maxEditableBytes))
		}
		if err := requireVersion(before, contentVersion(content)); err != nil {
			return fileMutation{}, err
		}
		updated, err := fuzzyReplace(string(content), input.OldText, input.NewText)
		if err != nil {
			return fileMutation{}, reject("内容不匹配", err)
		}
		perm, data = info.Mode().Perm(), []byte(updated)
	default:
		return fileMutation{}, reject("参数错误", fmt.Errorf("工具 %s 不是文件写入工具", name))
	}
	if info, err := root.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fileMutation{}, reject("路径越界", fmt.Errorf("只允许写入普通文件，不能写入符号链接或目录: %s", path))
		}
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fileMutation{}, rootError(err)
	}
	if name == "write_file" {
		version, err := fileVersion(ctx, root, path)
		if err != nil {
			return fileMutation{}, err
		}
		if err := requireVersion(before, version); err != nil {
			return fileMutation{}, err
		}
	}
	return fileMutation{FilePreview{Path: path, Before: before, After: contentVersion(data)}, data, perm}, nil
}

// PreviewWrite 使用与执行相同的版本校验和编辑逻辑，供任务日志记录预期效果。
func PreviewWrite(ctx context.Context, call schema.ToolCall) (FilePreview, error) {
	if err := requireTool(ctx, call.Name); err != nil {
		return FilePreview{}, err
	}
	root, err := openRoot(ctx, "")
	if err != nil {
		return FilePreview{}, err
	}
	defer root.Close()
	mutation, err := prepareMutation(ctx, root, call.Name, call.Arguments)
	return mutation.preview, err
}

func mutateFile(ctx context.Context, dir, name string, args json.RawMessage) (string, error) {
	if err := requireTool(ctx, name); err != nil {
		return "", err
	}
	fileWrites.Lock()
	defer fileWrites.Unlock()

	root, err := openRoot(ctx, dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	mutation, err := prepareMutation(ctx, root, name, args)
	if err != nil {
		return "", err
	}
	p := mutation.preview
	if p.Before != p.After {
		if err := commitFile(ctx, root, mutation); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("文件 %s 已写入，版本: %s", p.Path, p.After), nil
}

func commitFile(ctx context.Context, root *os.Root, mutation fileMutation) error {
	return commitFileWithParents(ctx, root, mutation, true)
}

// 报告只允许在已有父目录中新建文件，不能随提交创建额外目录。
func commitFileWithParents(ctx context.Context, root *os.Root, mutation fileMutation, createParents bool) error {
	p := mutation.preview
	parent := filepath.Dir(p.Path)
	if err := checkPathLinks(root, p.Path, true); err != nil {
		return err
	}
	if createParents {
		if err := root.MkdirAll(parent, 0755); err != nil {
			return rootError(err)
		}
	} else if err := existingReportParent(root, parent); err != nil {
		return err
	}
	tmp := filepath.Join(parent, ".claw-write-"+rand.Text())
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mutation.perm)
	if err != nil {
		return rootError(err)
	}
	defer root.Remove(tmp)
	_, err = io.Copy(file, contextReader{ctx, bytes.NewReader(mutation.content)})
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	version, err := fileVersion(ctx, root, p.Path)
	if err != nil {
		return err
	}
	if err := requireVersion(p.Before, version); err != nil {
		return err
	}
	if err := checkPathLinks(root, p.Path, true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Before == MissingVersion {
		if err := root.Link(tmp, p.Path); err != nil {
			return rootError(err)
		}
		if err := root.Remove(tmp); err != nil {
			return rootError(err)
		}
	} else if err := root.Rename(tmp, p.Path); err != nil {
		return rootError(err)
	}
	// 数据和目录项都需要落盘，才能在重启后核对效果。
	dir, err := root.Open(parent)
	if err != nil {
		return rootError(err)
	}
	defer dir.Close()
	return dir.Sync()
}
