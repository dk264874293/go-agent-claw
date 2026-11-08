package main

import (
	"testing"

	observability "github.com/dk264874293/go-agent-claw/internal/eval"
)

func TestBenchmarkDeliveryReviewDefaultsKeepExplicitCases(t *testing.T) {
	off, on := false, true
	cases := []observability.TestCase{{}, {DeliveryReview: &off}, {DeliveryReview: &on}}
	applyBenchmarkDeliveryReviewDefaults(cases, nil)
	if cases[0].DeliveryReview != nil {
		t.Fatal("旧用例默认被开启")
	}
	applyBenchmarkDeliveryReviewDefaults(cases, &on)
	if cases[0].DeliveryReview == nil || !*cases[0].DeliveryReview || *cases[1].DeliveryReview || !*cases[2].DeliveryReview {
		t.Fatal("全局设置覆盖了用例显式选择")
	}
	*cases[0].DeliveryReview = false
	if !on || !*cases[2].DeliveryReview {
		t.Fatal("默认值与用例共享可变指针")
	}
	for _, v := range []string{"true", "false", "", "auto"} {
		if _, err := benchmarkDeliveryReviewSwitch(v, true); err == nil {
			t.Fatalf("接受了非法字符串%q", v)
		}
	}
	if v, err := benchmarkDeliveryReviewSwitch("off", false); err != nil || v != nil {
		t.Fatal("默认off必须保持nil")
	}
}
