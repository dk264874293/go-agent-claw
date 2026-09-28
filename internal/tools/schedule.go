package tools

import (
	"context"
	"sync"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// ExecutionOptions 约束同一批次的工具执行。
type ExecutionOptions struct {
	Timeout       time.Duration
	MaxConcurrent int
}

type executionKey struct{}

func WithExecutionOptions(ctx context.Context, options ExecutionOptions) context.Context {
	return context.WithValue(ctx, executionKey{}, options)
}

func ExecutionOptionsFrom(ctx context.Context) ExecutionOptions {
	options, _ := ctx.Value(executionKey{}).(ExecutionOptions)
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 4
	}
	return options
}

// ExecuteBatch 仅并行相邻的已声明只读工具，其他调用作为顺序屏障。
func ExecuteBatch(ctx context.Context, registry Registry, calls []schema.ToolCall) []schema.ToolResult {
	options := ExecutionOptionsFrom(ctx)
	results := make([]schema.ToolResult, len(calls))
	parallel := func(name string) bool {
		policy, ok := registry.(interface{ CanRunConcurrently(string) bool })
		return ok && policy.CanRunConcurrently(name)
	}
	execute := func(i int) {
		if ctx.Err() != nil {
			results[i] = cancelledResult(ctx, calls[i])
			return
		}
		toolCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		defer cancel()
		results[i] = registry.Execute(toolCtx, calls[i])
	}
	for start := 0; start < len(calls); {
		if !parallel(calls[start].Name) {
			execute(start)
			start++
			continue
		}
		end := start + 1
		for end < len(calls) && parallel(calls[end].Name) {
			end++
		}
		workers := min(options.MaxConcurrent, end-start)
		var wg sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func(first, limit, stride int) {
				defer wg.Done()
				for i := first; i < limit; i += stride {
					execute(i)
				}
			}(start+worker, end, workers)
		}
		wg.Wait()
		start = end
	}
	return results
}
