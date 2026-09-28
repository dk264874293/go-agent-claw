package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
)

// Config 保存入口解析后的非密钥配置。
type Config struct {
	Prompt, WorkDir, SessionID         string
	Provider, Model, BaseURL           string
	Thinking, PlanMode                 bool
	MaxTurns                           int
	Timeout, ToolTimeout               time.Duration
	MaxConcurrentTools                 int
	MaxTotalTokens                     int64
	MaxOutputTokens                    int
	MaxCost, CostCurrency, PricingFile string
}

func Defaults() Config {
	return Config{WorkDir: ".", SessionID: "cli_default_session", Provider: "openai", Model: "gpt-5.6-sol",
		MaxTurns: 20, Timeout: 5 * time.Minute, ToolTimeout: 30 * time.Second, MaxConcurrentTools: 4,
		MaxTotalTokens: 100000, MaxOutputTokens: 4096}
}

// Parse 的优先级为命令行、进程环境、传入的默认值。
func Parse(args []string, getenv func(string) string, defaults Config) (Config, error) {
	cfg := defaults
	fs := flag.NewFlagSet("claw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Prompt, "prompt", cfg.Prompt, "任务描述")
	fs.StringVar(&cfg.WorkDir, "dir", cfg.WorkDir, "工作目录")
	fs.StringVar(&cfg.SessionID, "session", cfg.SessionID, "会话 ID")
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "Provider")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "模型")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "API 地址")
	fs.BoolVar(&cfg.Thinking, "thinking", cfg.Thinking, "启用 Thinking")
	fs.BoolVar(&cfg.PlanMode, "plan-mode", cfg.PlanMode, "启用计划模式")
	fs.IntVar(&cfg.MaxTurns, "max-turns", cfg.MaxTurns, "最大轮数")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "总超时")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", cfg.ToolTimeout, "单工具超时")
	fs.IntVar(&cfg.MaxConcurrentTools, "max-concurrent-tools", cfg.MaxConcurrentTools, "只读并发上限")
	fs.Int64Var(&cfg.MaxTotalTokens, "max-total-tokens", cfg.MaxTotalTokens, "每次运行 Token 预算")
	fs.IntVar(&cfg.MaxOutputTokens, "max-output-tokens", cfg.MaxOutputTokens, "每请求输出上限")
	fs.StringVar(&cfg.MaxCost, "max-cost", cfg.MaxCost, "每次运行费用预算")
	fs.StringVar(&cfg.CostCurrency, "cost-currency", cfg.CostCurrency, "费用预算币种")
	fs.StringVar(&cfg.PricingFile, "pricing-file", cfg.PricingFile, "价目表路径")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if len(fs.Args()) != 0 {
		return cfg, fmt.Errorf("不支持的位置参数: %s", strings.Join(fs.Args(), " "))
	}
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	envValue := func(flagName, envName string) string {
		if explicit[flagName] {
			return ""
		}
		return getenv(envName)
	}
	for _, entry := range []struct {
		flagName, envName string
		target            *string
	}{{"provider", "CLAW_PROVIDER", &cfg.Provider}, {"model", "CLAW_MODEL", &cfg.Model}, {"base-url", "CLAW_BASE_URL", &cfg.BaseURL},
		{"max-cost", "CLAW_MAX_COST", &cfg.MaxCost}, {"cost-currency", "CLAW_COST_CURRENCY", &cfg.CostCurrency}, {"pricing-file", "CLAW_PRICING_FILE", &cfg.PricingFile}} {
		if value := envValue(entry.flagName, entry.envName); value != "" {
			*entry.target = value
		}
	}
	for _, entry := range []struct {
		flagName, envName string
		target            *bool
	}{{"thinking", "CLAW_THINKING", &cfg.Thinking}, {"plan-mode", "CLAW_PLAN_MODE", &cfg.PlanMode}} {
		if value := envValue(entry.flagName, entry.envName); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return cfg, fmt.Errorf("%s 无效: %w", entry.envName, err)
			}
			*entry.target = parsed
		}
	}
	for _, entry := range []struct {
		flagName, envName string
		target            *int
	}{{"max-turns", "CLAW_MAX_TURNS", &cfg.MaxTurns}, {"max-concurrent-tools", "CLAW_MAX_CONCURRENT_TOOLS", &cfg.MaxConcurrentTools}, {"max-output-tokens", "CLAW_MAX_OUTPUT_TOKENS", &cfg.MaxOutputTokens}} {
		if value := envValue(entry.flagName, entry.envName); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return cfg, fmt.Errorf("%s 无效: %w", entry.envName, err)
			}
			*entry.target = parsed
		}
	}
	if value := envValue("max-total-tokens", "CLAW_MAX_TOTAL_TOKENS"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("CLAW_MAX_TOTAL_TOKENS 无效: %w", err)
		}
		cfg.MaxTotalTokens = parsed
	}
	for _, entry := range []struct {
		flagName, envName string
		target            *time.Duration
	}{{"timeout", "CLAW_TIMEOUT", &cfg.Timeout}, {"tool-timeout", "CLAW_TOOL_TIMEOUT", &cfg.ToolTimeout}} {
		if value := envValue(entry.flagName, entry.envName); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return cfg, fmt.Errorf("%s 无效: %w", entry.envName, err)
			}
			*entry.target = parsed
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = provider.DefaultURL(cfg.Provider)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if provider.DefaultURL(c.Provider) == "" || strings.TrimSpace(c.Model) == "" || c.MaxTurns <= 0 || c.Timeout <= 0 || c.ToolTimeout <= 0 || c.MaxConcurrentTools <= 0 || c.MaxTotalTokens <= 0 || c.MaxOutputTokens <= 0 {
		return fmt.Errorf("运行配置无效")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return fmt.Errorf("API 地址无效")
	}
	if c.MaxCost != "" {
		amount, err := budget.ParseMicros(c.MaxCost)
		if err != nil || amount <= 0 || c.CostCurrency == "" {
			return fmt.Errorf("费用预算和币种无效")
		}
	}
	return nil
}

func (c Config) RunOptions() (runpkg.Options, error) {
	if err := c.Validate(); err != nil {
		return runpkg.Options{}, err
	}
	price, err := budget.LoadPrice(c.PricingFile, c.Provider, c.BaseURL, c.Model)
	if err != nil {
		return runpkg.Options{}, err
	}
	var maxCost int64
	if c.MaxCost != "" {
		maxCost, err = budget.ParseMicros(c.MaxCost)
		if err != nil {
			return runpkg.Options{}, err
		}
		if price == nil || price.Currency != c.CostCurrency {
			return runpkg.Options{}, fmt.Errorf("费用上限缺少匹配价目或币种")
		}
	}
	return runpkg.Options{MaxTurns: c.MaxTurns, Timeout: c.Timeout, ToolTimeout: c.ToolTimeout, MaxConcurrentTools: c.MaxConcurrentTools,
		MaxTotalTokens: c.MaxTotalTokens, MaxOutputTokens: c.MaxOutputTokens, MaxCostMicros: maxCost, Price: price, Provider: c.Provider, Model: c.Model, ConfigVersion: runpkg.ConfigSchemaVersion}, nil
}
