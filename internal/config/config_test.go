package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePrecedenceAndPlanDefault(t *testing.T) {
	env := map[string]string{"CLAW_MODEL": "环境模型", "CLAW_MAX_TURNS": "3", "CLAW_PLAN_MODE": "true"}
	getenv := func(key string) string { return env[key] }
	cfg, err := Parse([]string{"-model", "命令行模型", "-max-turns", "4", "-plan-mode=false"}, getenv, Defaults())
	if err != nil || cfg.Model != "命令行模型" || cfg.MaxTurns != 4 || cfg.PlanMode {
		t.Fatalf("优先级错误: %+v %v", cfg, err)
	}
	env["CLAW_MAX_TURNS"] = "非法数字"
	cfg, err = Parse([]string{"-max-turns", "4"}, getenv, Defaults())
	if err != nil || cfg.MaxTurns != 4 {
		t.Fatalf("命令行已覆盖的环境值不应阻止解析: %+v %v", cfg, err)
	}
	cfg, err = Parse(nil, func(string) string { return "" }, Defaults())
	if err != nil || cfg.PlanMode || cfg.MaxTotalTokens != 100000 || cfg.BaseURL == "" {
		t.Fatalf("默认配置错误: %+v %v", cfg, err)
	}
	if _, err := Parse(nil, func(key string) string {
		if key == "CLAW_TIMEOUT" {
			return "bad"
		}
		return ""
	}, Defaults()); err == nil {
		t.Fatal("非法环境值未拒绝")
	}
}

func TestCostBudgetRequiresMatchingPrice(t *testing.T) {
	cfg := Defaults()
	cfg.BaseURL = "https://api.example/v1"
	cfg.MaxCost, cfg.CostCurrency = "1.00", "USD"
	if _, err := cfg.RunOptions(); err == nil {
		t.Fatal("缺价却启用费用预算")
	}
	path := filepath.Join(t.TempDir(), "price.json")
	data := `{"version":"v1","rates":[{"provider":"openai","base_url":"https://api.example/v1","model":"gpt-5.6-sol","currency":"USD","input_per_million":"1","output_per_million":"2"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.PricingFile = path
	opts, err := cfg.RunOptions()
	if err != nil || opts.MaxCostMicros != 1000000 || opts.Price == nil {
		t.Fatalf("费用预算无效: %+v %v", opts, err)
	}
	cfg.CostCurrency = "CNY"
	if _, err := cfg.RunOptions(); err == nil {
		t.Fatal("币种不匹配未拒绝")
	}
}

func TestParseRejectsInvalidConfigurationBeforeRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"无效 Provider", []string{"-provider", "unknown"}},
		{"空模型", []string{"-model", ""}},
		{"远程 HTTP", []string{"-base-url", "http://example.com/v1"}},
		{"无效时限", []string{"-timeout", "0s"}},
		{"无效 Token", []string{"-max-total-tokens", "0"}},
		{"无效布尔", []string{"-thinking=maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.args, func(string) string { return "" }, Defaults()); err == nil {
				t.Fatal("无效配置被接受")
			}
		})
	}
}
