package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func existingReportParent(root *os.Root, parent string) error {
	if err := checkPathLinks(root, parent, false); err != nil {
		return err
	}
	info, err := root.Stat(parent)
	if err != nil {
		return reject("路径越界", fmt.Errorf("报告父目录必须已经存在: %w", err))
	}
	if !info.IsDir() {
		return reject("路径越界", fmt.Errorf("报告父路径不是目录: %s", parent))
	}
	return nil
}

func prepareReportMutation(ctx context.Context, root *os.Root, raw string, content []byte) (fileMutation, error) {
	if err := requireTool(ctx, "write_report"); err != nil {
		return fileMutation{}, err
	}
	path, err := writablePath(ctx, raw)
	if err != nil {
		return fileMutation{}, err
	}
	if filepath.Ext(path) != ".md" {
		return fileMutation{}, reject("参数错误", fmt.Errorf("报告目标必须为 .md 文件"))
	}
	if err := existingReportParent(root, filepath.Dir(path)); err != nil {
		return fileMutation{}, err
	}
	if err := checkPathLinks(root, path, true); err != nil {
		return fileMutation{}, err
	}
	actual, err := fileVersion(ctx, root, path)
	if err != nil {
		return fileMutation{}, err
	}
	if err := requireVersion(MissingVersion, actual); err != nil {
		return fileMutation{}, err
	}
	data := bytes.Clone(content)
	return fileMutation{preview: FilePreview{Path: path, Before: MissingVersion, After: contentVersion(data)}, content: data, perm: 0644}, nil
}

// PreviewReportWrite 用待发布的冻结字节建立创建预览，不产生文件副作用。
func PreviewReportWrite(ctx context.Context, path string, content []byte) (FilePreview, error) {
	if err := requireTool(ctx, "write_report"); err != nil {
		return FilePreview{}, err
	}
	root, err := openRoot(ctx, "")
	if err != nil {
		return FilePreview{}, err
	}
	defer root.Close()
	mutation, err := prepareReportMutation(ctx, root, path, content)
	return mutation.preview, err
}

// WriteReportFile 与普通写工具共用版本校验、串行锁和原子提交；不分发第二次工具调用。
func WriteReportFile(ctx context.Context, path string, content []byte) (string, error) {
	if err := requireTool(ctx, "write_report"); err != nil {
		return "", err
	}
	fileWrites.Lock()
	defer fileWrites.Unlock()
	root, err := openRoot(ctx, "")
	if err != nil {
		return "", err
	}
	defer root.Close()
	mutation, err := prepareReportMutation(ctx, root, path, content)
	if err != nil {
		return "", err
	}
	// Registry 已消费的钩子会被清空；直接使用该原语也先提交执行记录。
	if hook, _ := ctx.Value(executionHookKey{}).(func(context.Context) error); hook != nil {
		if err := hook(ctx); err != nil {
			return "", err
		}
	}
	if err := commitFileWithParents(ctx, root, mutation, false); err != nil {
		return "", err
	}
	return fmt.Sprintf("报告 %s 已写入，版本: %s", mutation.preview.Path, mutation.preview.After), nil
}
