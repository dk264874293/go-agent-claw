package engine

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// ReadScopeSpec 声明资料选择条件，不授予读取或写入权限。
// 目录成员仅从本次运行的发现回执取得，不隐式扫描工作区。
type ReadScopeSpec struct {
	Version         int      `json:"version"`
	Paths           []string `json:"paths"`
	NameGlob        string   `json:"name_glob"`
	RequireComplete bool     `json:"require_complete"`
}

// NormalizeReadScope 只做配置规范化；恢复和只读检查应调用 ValidateReadScopeSpec。
func NormalizeReadScope(spec *TaskSpec) error {
	if spec.ReadScope == nil {
		return nil
	}
	copy := *spec.ReadScope
	if copy.Version == 0 {
		copy.Version = 1
	}
	if copy.NameGlob == "" {
		copy.NameGlob = "*"
	}
	if len(copy.Paths) == 0 {
		return errors.New("读取范围必须包含至少一个工作区相对文件或目录")
	}
	seen := map[string]bool{}
	copy.Paths = nil
	for _, raw := range spec.ReadScope.Paths {
		if !filepath.IsLocal(raw) || strings.ContainsAny(raw, "\\\x00\r\n") {
			return fmt.Errorf("读取范围必须是工作区内的相对路径：%q", raw)
		}
		for _, part := range strings.Split(raw, "/") {
			if part == ".." || part == ".claw" {
				return fmt.Errorf("读取范围不能包含上级跳转或任务存储目录：%q", raw)
			}
		}
		p := filepath.ToSlash(filepath.Clean(raw))
		if !seen[p] {
			copy.Paths = append(copy.Paths, p)
			seen[p] = true
		}
	}
	sort.Strings(copy.Paths)
	if copy.Version != 1 {
		return errors.New("不支持的读取范围版本")
	}
	if strings.ContainsAny(copy.NameGlob, "/\\") || strings.Contains(copy.NameGlob, "**") {
		return errors.New("读取范围 name_glob 只支持文件基本名模式，不允许目录分隔符或 **")
	}
	if _, err := path.Match(copy.NameGlob, ""); err != nil {
		return fmt.Errorf("读取范围 name_glob 无效：%w", err)
	}
	spec.ReadScope = &copy
	return nil
}

// ValidateReadScopeSpec 不修改持久设置，也不为历史记录补造范围或修订。
func ValidateReadScopeSpec(scope *ReadScopeSpec) error {
	if scope == nil {
		return nil
	}
	spec := TaskSpec{ReadScope: scope}
	if err := NormalizeReadScope(&spec); err != nil {
		return err
	}
	if !reflect.DeepEqual(scope, spec.ReadScope) {
		return errors.New("持久读取范围不是规范的 version=1 配置")
	}
	return nil
}
