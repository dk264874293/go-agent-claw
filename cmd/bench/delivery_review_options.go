package main

import (
	"fmt"

	observability "github.com/dk264874293/go-agent-claw/internal/eval"
)

func benchmarkDeliveryReviewSwitch(value string, explicit bool) (*bool, error) {
	if value != "on" && value != "off" {
		return nil, fmt.Errorf("-delivery-review必须为on或off")
	}
	if !explicit {
		return nil, nil
	}
	enabled := value == "on"
	return &enabled, nil
}

func applyBenchmarkDeliveryReviewDefaults(cases []observability.TestCase, global *bool) {
	if global == nil {
		return
	}
	for i := range cases {
		if cases[i].DeliveryReview == nil {
			copy := *global
			cases[i].DeliveryReview = &copy
		}
	}
}
