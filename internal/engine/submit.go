package engine

import (
	"context"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	runpkg "github.com/dk264874293/go-agent-claw/internal/run"
)

// Run 为已准备好历史的内部调用取得会话运行权。
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter, opts runpkg.Options) (runpkg.Result, error) {
	if session == nil {
		return runpkg.Result{}, runpkg.ErrInvalidOptions
	}
	return e.runQueued(ctx, session, reporter, opts, nil, session.ReserveRun())
}

// Submit 把追加用户消息和整轮运行放在同一串行边界内。
func (e *AgentEngine) Submit(ctx context.Context, session *ctxpkg.Session, prompt string, reporter Reporter, opts runpkg.Options) (runpkg.Result, error) {
	if session == nil {
		return runpkg.Result{}, runpkg.ErrInvalidOptions
	}
	return e.runQueued(ctx, session, reporter, opts, &prompt, session.ReserveRun())
}

type Outcome struct {
	Result runpkg.Result
	Err    error
}

// Start 在返回之前登记票据，异步完成后向有缓冲通道返回结果。
func (e *AgentEngine) Start(ctx context.Context, session *ctxpkg.Session, prompt string, reporter Reporter, opts runpkg.Options) <-chan Outcome {
	done := make(chan Outcome, 1)
	if session == nil {
		done <- Outcome{Err: runpkg.ErrInvalidOptions}
		close(done)
		return done
	}
	ticket := session.ReserveRun()
	go func() {
		result, err := e.runQueued(ctx, session, reporter, opts, &prompt, ticket)
		done <- Outcome{Result: result, Err: err}
		close(done)
	}()
	return done
}
