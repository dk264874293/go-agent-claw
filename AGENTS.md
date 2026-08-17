# AGENTS.md

Guidance for ZCode agents working in this repository.

## Project

go-agent-claw is a ReAct-style AI agent engine in Go (Think → Act → Observe loop), built as a learning project that incrementally re-implements Claude-Code-like features: sessions, context compaction, subagents, plan mode, tracing, and cost tracking. Comments, log messages, and user-facing strings are in Chinese — keep that style.

`CLAUDE.md` exists but predates the `context/`, `observability/`, `eval/`, and `feishu/` packages; trust this file over it where they disagree.

## Commands

```bash
go build -o claw ./cmd/claw        # build the CLI
go run ./cmd/claw                  # run directly
go build ./cmd/... ./internal/...  # build all module packages
go run ./cmd/bench                 # benchmark runner (hits real LLM API, costs money)
```

- **Do not use `go build ./...`**: it fails because `workspace/main.go` is an empty scratch file. `workspace/` is the agent's demo/test working directory (untracked), not part of the module.
- Tests: `internal/context/session_test.go` covers `GetWorkingMemory` windowing (run `go test ./internal/context/`); no other `_test.go` files exist yet.
- Requires `ZHIPU_API_KEY` in `.env` or environment (hard fatal without it). Feishu bot code additionally needs `FEISHU_APP_ID` / `FEISHU_APP_SECRET`.

## Architecture

```
cmd/claw/main.go            — CLI entry: wires provider → CostTracker → registry → engine
cmd/bench/main.go           — benchmark runner (SetupScript / TaskPrompt / ValidateScript test cases)
internal/schema/            — shared types: Message, Role, ToolCall, ToolResult, ToolDefinition
internal/provider/          — LLMProvider interface + OpenAI-compatible (openai.go) and
                              Anthropic-compatible (claude.go) implementations
internal/engine/loop.go     — AgentEngine: ReAct main loop + RunSub for subagents
internal/engine/            — Reporter interface + TerminalReporter, ReminderInjector (anti-death-loop)
internal/context/           — Session (history + working memory), Compactor, RecoveryManager,
                              PromptComposer (system prompt, plan mode), skill loader
internal/tools/             — BaseTool interface, Registry with middleware support,
                              read/write/edit_file/bash tools, subagent tool
internal/observability/     — CostTracker (wraps provider, bills Session), Span tracing
                              exported to <workDir>/.claw/traces
internal/feishu/            — Feishu/Lark bot + approval flow (not wired into main.go currently)
```

### Layer rules

- Dependencies flow inward: `schema` depends on nothing; `engine` orchestrates `provider`, `tools`, `context`, `observability`.
- The engine is stateless w.r.t. files: **WorkDir lives on the Session** and is injected into each tool constructor to constrain file operations.
- Tool results flow back to the model as `RoleUser` messages carrying `ToolCallID` (not a dedicated role).
- Tool args are `json.RawMessage`; each tool lazily deserializes its own arguments.

## Key mechanics and gotchas

- **Working memory**: the main loop sends only the last 20 messages (`session.GetWorkingMemory(20)`), then `Compactor.Compact` masks/truncates oversized content. When touching truncation logic, preserve ToolCall↔ToolResult pairing — broken pairs cause 400 Bad Request from the LLM API (`GetWorkingMemory` has careful handling for this; keep it).
- **Concurrent tool execution**: all tool calls in one turn run in goroutines writing to pre-allocated slice slots (no mutex on the slice); `Session` itself is guarded by an `sync.RWMutex`. Pass loop variables as function args to avoid closure capture bugs.
- **Package-name quirk**: `internal/eval/benchmark.go` declares `package observability` despite living in `internal/eval/`. Import the path `internal/eval` but reference it as `observability.*` (see `cmd/bench/main.go`).
- **Error self-healing**: tool errors pass through `RecoveryManager.AnalyzeAndInject`, which appends recovery hints to the output before the model sees it.
- **Dual providers**: main currently uses `provider.NewZhipuOpenAIProvider("glm-4.7")`; `NewDeepseekClaudeProvider` exists for the Anthropic-compatible route. Changing models means changing constructors + model string.
- **Currently dormant wiring** in `cmd/claw/main.go`: subagent tool registration and the Feishu HTTP server are commented out; re-enable deliberately, not accidentally.
- Engine constructor signature: `NewAgentEngine(provider, registry, enableThinking, planMode)` — thinking phase makes two LLM calls per turn (plan without tools, then act).

## Adding things

- **New tool**: implement `BaseTool` (Name/Definition/Execute) in `internal/tools/`, register in `cmd/claw/main.go` via `registry.Register(...)`. The engine picks it up automatically.
- **New provider**: implement `LLMProvider.Generate(ctx, messages, tools)` in `internal/provider/`, add an env-reading constructor, wire in `cmd/claw/main.go`.
- **Tool interception**: add `MiddlewareFunc` via `registry.Use(...)` — middlewares run before execution and can reject calls with a reason the model must read.
