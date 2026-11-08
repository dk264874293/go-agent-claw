package main

import (
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/engine"
)

// 只传入flag.Visit确认的显式项，恢复时连显式off也不能覆盖历史策略。
func applyCLIReadFeatures(spec *engine.TaskSpec, values map[string]string) error {
	features, err := engine.ParseReadFeatureSwitches(values)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	if spec.Resume {
		return fmt.Errorf("恢复时沿用原读取配置，不能指定 -source-views、-local-checks 或 -claim-focus（包括off）。")
	}
	copy := *spec
	copy.ReadFeatures = features
	if err := engine.NormalizeReadFeatures(&copy); err != nil {
		return err
	}
	spec.ReadContextVersion, spec.ReadFeatures = copy.ReadContextVersion, copy.ReadFeatures
	return nil
}
