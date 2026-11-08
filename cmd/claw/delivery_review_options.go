package main

import (
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// applyCLIDeliveryReview 在创建Provider前校验；省略和显式off在恢复时不同。
func applyCLIDeliveryReview(spec *engine.TaskSpec, value string, explicit bool, mode tools.Mode, plan, thinking bool) error {
	if value != "off" && value != "on" {
		return fmt.Errorf("-delivery-review 必须为 on 或 off。")
	}
	if spec.Resume && explicit {
		return fmt.Errorf("恢复时沿用原交付前处理设置，不能指定 -delivery-review（包括off）。")
	}
	if !explicit || value == "off" {
		return nil
	}
	if plan || thinking {
		return fmt.Errorf("交付前处理不能组合规划或额外思考。")
	}
	copy := *spec
	copy.Mode, copy.Plan = mode, plan
	if copy.Kind == "" {
		if mode == tools.ModeReadOnly {
			copy.Kind = engine.TaskAnswer
		} else if mode == tools.ModeWorkspace {
			copy.Kind = engine.TaskChange
		}
	}
	copy.DeliveryReview = &engine.DeliveryReviewSpec{Version: 1}
	if err := engine.NormalizeDeliveryReviewSpec(&copy); err != nil {
		return fmt.Errorf("交付前处理配置无效：%w", err)
	}
	spec.Mode, spec.Kind, spec.DeliveryReview = copy.Mode, copy.Kind, copy.DeliveryReview
	return nil
}
