package main

import (
	"context"

	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
)

type requestDiagnosticConfigKey struct{}

// 诊断是当前入口策略，不写入任务规格；同一REPL的恢复使用当前明确配置。
func withCLIRequestDiagnostics(ctx context.Context, cfg requestdiag.Config) context.Context {
	return context.WithValue(ctx, requestDiagnosticConfigKey{}, cfg)
}

func cliRequestDiagnostics(ctx context.Context) (requestdiag.Config, error) {
	if cfg, ok := ctx.Value(requestDiagnosticConfigKey{}).(requestdiag.Config); ok {
		return cfg, nil
	}
	return requestdiag.ResolveConfig("", "")
}
