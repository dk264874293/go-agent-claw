package main

import (
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// applyCLIQuality 在装配 Provider 之前校验入口，不为质量模式扩大执行权限。
func applyCLIQuality(spec *engine.TaskSpec, enabled bool, mode tools.Mode, plan, thinking bool, explicitDelegation string) error {
	if !enabled {
		return nil
	}
	if spec.Resume {
		return fmt.Errorf("恢复时沿用原质量策略，无需指定 -quality，也不能为旧任务开启质量模式。")
	}
	if plan || thinking || spec.VerifyCommand != "" || explicitDelegation != "" && explicitDelegation != "off" {
		return fmt.Errorf("质量模式不能组合规划、额外思考、验收命令或子任务委派；委派仅可显式关闭。")
	}
	copy := *spec
	copy.Mode, copy.Plan, copy.Quality = mode, plan, &engine.QualitySpec{Version: 1}
	if copy.Analysis && copy.Report == nil {
		copy.Delegation = "off"
	}
	if err := engine.NormalizeQualitySpec(&copy); err != nil {
		return fmt.Errorf("质量模式配置无效：%w", err)
	}
	spec.Quality, spec.Delegation = copy.Quality, copy.Delegation
	return nil
}
