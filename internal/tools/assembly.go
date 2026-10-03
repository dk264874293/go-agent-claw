package tools

import (
	"github.com/dk264874293/go-agent-claw/internal/executor"
	"github.com/dk264874293/go-agent-claw/internal/policy"
)

// NewWorkspaceRegistry 在运行前完成工具装配，权限策略决定对模型公开的能力。
func NewWorkspaceRegistry(dir string, permissions policy.Policy, approver policy.Approver, shell executor.Executor, network bool) Registry {
	r := NewRegistryWithPolicy(permissions, approver)
	r.Register(NewReadFileTool(dir))
	r.Register(NewListFilesTool(dir))
	r.Register(NewSearchTextTool(dir))
	// GetAvailableTools 过滤公开列表，Execute 再次校验，包含模型绕过列表的请求。
	r.Register(NewWriteFileTool(dir))
	r.Register(NewEditFileTool(dir))
	r.Register(NewBashToolWithExecutor(dir, shell, network))
	return r
}
