package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type Mode string

const (
	ModeReadOnly  Mode = "readonly"
	ModeWorkspace Mode = "workspace"
	ModeTrusted   Mode = "trusted"
)

// ExecutionScope 由调用方设置，模型只能在允许的路径与工具范围内执行。
type ExecutionScope struct {
	Mode            Mode
	WorkDir         string
	AllowedPaths    []string
	DisableSubagent bool
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope ExecutionScope) context.Context {
	scope.AllowedPaths = append([]string(nil), scope.AllowedPaths...)
	return context.WithValue(ctx, scopeKey{}, scope)
}

func ScopeFromContext(ctx context.Context) ExecutionScope {
	scope, _ := ctx.Value(scopeKey{}).(ExecutionScope)
	if scope.Mode == "" {
		scope.Mode = ModeReadOnly
	}
	return scope
}

func IsReadOnly(name string) bool {
	switch name {
	case "read_file", "read_lines", "list_dir", "search_text", "read_evidence", "read_report", "find_files":
		return true
	}
	return false
}

func (s ExecutionScope) allowsTool(name string) bool {
	switch s.Mode {
	case ModeReadOnly, ModeWorkspace, ModeTrusted:
	default:
		return false
	}
	switch {
	case IsReadOnly(name):
		return true
	case name == "spawn_subagent":
		return !s.DisableSubagent
	case name == "write_file" || name == "edit_file":
		return s.Mode == ModeWorkspace || s.Mode == ModeTrusted
	default:
		return s.Mode == ModeTrusted
	}
}

func requireTool(ctx context.Context, name string) error {
	if !ScopeFromContext(ctx).allowsTool(name) {
		return reject("权限不足", fmt.Errorf("当前执行模式不允许使用 %s", name))
	}
	return nil
}

func workDir(ctx context.Context, fallback string) string {
	if dir := ScopeFromContext(ctx).WorkDir; dir != "" {
		return dir
	}
	return fallback
}

// cleanPath 在打开文件之前拒绝绝对路径和任何上级目录跳转。
func cleanPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.ContainsRune(path, 0) {
		return "", reject("路径越界", fmt.Errorf("请提供工作区内的相对路径"))
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", reject("路径越界", fmt.Errorf("路径不能包含上级目录跳转: %s", path))
		}
	}
	return filepath.Clean(path), nil
}

func pathWithin(path, base string) bool {
	return base == "." || path == base || strings.HasPrefix(path, base+string(filepath.Separator))
}

func (s ExecutionScope) allowsPath(path string, allowAncestor bool) bool {
	if len(s.AllowedPaths) == 0 {
		return true
	}
	for _, raw := range s.AllowedPaths {
		base, err := cleanPath(raw)
		if err == nil && (pathWithin(path, base) || (allowAncestor && pathWithin(base, path))) {
			return true
		}
	}
	return false
}
