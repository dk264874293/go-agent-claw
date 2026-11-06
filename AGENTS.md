<!--
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-17 11:38:50
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-17 13:38:17
 * @FilePath: /go-agent-claw/AGENTS.md
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
-->
# AGENTS.md

## 项目

go-agent-claw 是一个 Go 编写的 ReAct 风格 AI 智能体引擎（Think → Act → Observe 循环），作为学习项目逐步复刻 Claude-Code 式的能力：会话、上下文压缩、子智能体、计划模式、链路追踪与成本统计。代码注释、日志信息和面向用户的字符串均使用中文——请保持这一风格。

`CLAUDE.md` 是指向本文件的引用，无需单独维护。

## 常用命令

```bash
go build -o claw ./cmd/claw        # 构建 CLI
go run ./cmd/claw                  # 直接运行（不带 -prompt 进入交互对话模式 REPL，带 -prompt 单次执行后退出）
go build ./cmd/... ./internal/...  # 构建模块内所有包
go run ./cmd/bench                 # 基准测试 runner（会真实调用 LLM API，产生费用）
```

- **不要使用 `go build ./...`**：会因 `workspace/main.go` 是空草稿文件而失败。`workspace/` 是智能体的演示/测试工作目录（未纳入 git），不属于模块。
- 测试：`go test ./cmd/... ./internal/...`。共享预算、账本、存储和审批改动用 `go test -race ./internal/...` 验证。
- CLI、Bench 和独立飞书服务默认读取 `DEEPSEEK_API_KEY`，使用 `deepseek-v4.1-flash-1017`。自动选择 `gpt-*` 模型时读取 `OPENAI_API_KEY`，`glm-*` 读取 `ZHIPU_API_KEY`，飞书服务配置见 README。
- REPL 内置斜杠命令：`/help` `/exit` `/cost` `/status` `/clear` `/verify` `/resume`。设置验收和清空对话在本地处理，`/resume` 恢复持久任务。输入独占一行的 `"""` 进入多行输入模式，再一行 `"""` 结束提交。

## 架构

```
cmd/claw/main.go            — CLI 入口：装配 provider → CostTracker → registry → engine
cmd/bench/main.go           — 基准测试 runner（SetupScript / TaskPrompt / ValidateScript 测试用例）
internal/schema/            — 共享类型：Message、Role、ToolCall、ToolResult、ToolDefinition
internal/provider/          — LLMProvider 接口 + OpenAI 兼容 (openai.go) 与
                              Anthropic 兼容 (claude.go) 两套实现
internal/engine/loop.go     — AgentEngine：ReAct 主循环 + 子智能体的 RunSub
internal/engine/            — 调度、预算、验收、恢复、子智能体、Reporter
internal/context/           — Session（历史 + 工作记忆）、Compactor、RecoveryManager、
                              PromptComposer（系统提示词、计划模式）、技能加载器
internal/tools/             — BaseTool 接口、支持中间件的 Registry、
                              read/write/edit_file/bash 工具、子智能体工具
internal/observability/     — CostTracker（包裹 provider，为 Session 计费）、Span 链路追踪，
                              导出到 <workDir>/.claw/traces
internal/taskstore/         — 任务日志、检查点、证据、工作区锁
internal/feishu/            — 群聊队列、去重、审批（cmd/agentops）
```

### 分层规则

- 依赖向内流动：`schema` 不依赖任何包；`engine` 编排 `provider`、`tools`、`context`、`observability`。
- **WorkDir 挂在 Session 上**。工具按 ExecutionScope 准入，文件操作使用工作区根句柄。
- 工具结果以携带 `ToolCallID` 的 `RoleUser` 消息回传给模型（没有独立的 tool 角色）。
- 工具参数是 `json.RawMessage`；各工具惰性反序列化自己的参数。

## 关键机制与坑

- **工作记忆**：主循环只发送最近 20 条消息（`session.GetWorkingMemory(20)`），再由 `Compactor.CompactWithin` 按字节预算分诊。任务目标单独注入。改动截断逻辑时必须保住 ToolCall↔ToolResult 配对——配对断裂会导致 LLM API 返回 400 Bad Request；智谱 GLM 还要求首条非 system 消息必须是普通 user 消息（否则报 400 code=1214）。`GetWorkingMemory` 用两档策略处理窗口滑动：回退补数 ≤3 条时向前补齐到普通 user 消息块边界；超过 3 条则保持窗口大小，剔除头部孤儿 tool 结果并注入合成 user 前缀。
- **并发工具执行**：连续的独立只读调用限量并发，写入和 Bash 作为顺序屏障。同一工作区由 taskstore 独占锁保护。
- **包名怪癖**：`internal/eval/benchmark.go` 位于 `internal/eval/` 目录却声明 `package observability`。导入路径是 `internal/eval`，但引用时写作 `observability.*`（见 `cmd/bench/main.go`）。
- **错误自愈**：工具报错会经过 `RecoveryManager.AnalyzeAndInject`，在模型看到输出前追加救援提示。
- **模型入口**：CLI、Bench 和 AgentOps 通过 `provider.ResolveModelConfig` 与 `provider.NewConfiguredProvider` 选择模型和渠道，默认模型为 Deepseek 的 `deepseek-v4.1-flash-1017`。自动选择时 `deepseek*` 使用 Deepseek、`glm-*` 使用智谱，其余使用 OpenAI；显式 `-provider` 可覆盖渠道。真实对照评测继续显式使用 `gpt-6.1-sol`。
- **执行模式**：CLI 默认 `workspace`，允许工作区内读写；可用 `-mode readonly` 显式只读，可信本机命令须用 `-mode trusted` 启用。子智能体只使用只读工具。
- 引擎构造签名为 `NewAgentEngine(provider, registry, EngineOptions)`。通过 `Run(ctx, session, TaskSpec, reporter)` 执行，调用方从 `RunResult` 获取任务终态。

## 新增东西

- **新工具**：在 `internal/tools/` 实现 `BaseTool`（Name/Definition/Execute），在 `cmd/claw/main.go` 通过 `registry.Register(...)` 注册。引擎会自动感知。
- **新 Provider**：在 `internal/provider/` 实现 `LLMProvider.Generate(ctx, messages, tools)`，加一个读环境变量的构造函数，在 `cmd/claw/main.go` 接线。
- **工具拦截**：通过 `registry.Use(...)` 添加 `MiddlewareFunc`——中间件在执行前运行，可以带理由拒绝调用，模型必须读该理由。
