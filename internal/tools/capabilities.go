package tools

import "github.com/dk264874293/go-agent-claw/internal/policy"

func (*ReadFileTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace}
}
func (*WriteFileTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.WriteWorkspace}
}
func (*EditFileTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.ReadWorkspace, policy.WriteWorkspace}
}
func (*SubagentTool) RequiredCapabilities() []policy.Capability {
	return []policy.Capability{policy.Delegate}
}

// capabilityTool 的声明由本地工具代码给出，不信任模型请求中的权限字段。
type capabilityTool interface{ RequiredCapabilities() []policy.Capability }
