# go-agent-claw 🐾

> 一个用 Go 从零打造的 ReAct 风格 AI 智能体引擎（Think → Act → Observe 循环），作为学习项目逐步复刻 Claude-Code 式的核心能力：会话管理、上下文压缩、子智能体、计划模式、链路追踪与成本统计。

## ✨ 核心特性

- **ReAct 主循环**：`Think → Act → Observe` 循环驱动，模型自主决定何时调用工具、何时宣告任务完成。
- **双阶段推理（Thinking 模式）**：开启后每轮发两次 LLM 调用——先剥夺工具强制规划，再带工具精准行动。
- **会话与工作记忆**：`Session` 持久化完整历史，主循环只发送最近 20 条消息作为短期工作记忆；窗口滑动时自动保住 `ToolCall↔ToolResult` 配对与首条 user 消息的 API 校验。
- **上下文压缩器（Compactor）**：发给模型前对超长上下文做掩码/截断，保护最近消息不被远古日志吃光 token 预算。
- **并发工具执行**：同一轮的多个工具调用在 goroutine 中并发执行，各自写入预分配切片的不同槽位，无锁聚合。
- **子智能体（Subagent）**：主智能体可通过 `spawn_subagent` 派出只读"探路者"，在最多 10 轮内深度探索并返回精炼摘要。
- **错误自愈（RecoveryManager）**：工具报错在送回模型前自动诊断并注入"救援提示"，引导模型自我修正。
- **死循环探测（ReminderInjector）**：对连续重复的工具调用做指纹识别，触发时注入严厉提醒打破局部执念。
- **可观测性**：`CostTracker` 包裹 Provider 为 Session 计费（token/成本）；`Span` 树形链路追踪自动导出到 `<workDir>/.claw/traces`。
- **计划模式（PlanMode）**：PromptComposer 按 `planMode` 开关组装不同的系统提示词。
- **技能加载（SkillLoader）**：从工作区加载 Markdown 技能文件注入系统提示词。
- **双 Provider 支持**：OpenAI 兼容路线（智谱 GLM）与 Anthropic 兼容路线（DeepSeek），一个接口随时切换。
- **工具中间件**：`registry.Use(...)` 可拦截工具调用，带理由拒绝，模型必须读该理由。
- **基准测试（bench）**：`SetupScript / TaskPrompt / ValidateScript` 三段式评测 runner，客观给智能体打分。

## 📦 目录结构

```
cmd/claw/main.go            — CLI 入口：装配 provider → CostTracker → registry → engine
cmd/bench/main.go           — 基准测试 runner（会真实调用 LLM API，产生费用）
internal/schema/            — 共享类型：Message、Role、ToolCall、ToolResult、ToolDefinition
internal/provider/          — LLMProvider 接口 + OpenAI 兼容 (openai.go) 与
                              Anthropic 兼容 (claude.go) 两套实现
internal/engine/loop.go     — AgentEngine：ReAct 主循环 + 子智能体的 RunSub
internal/engine/            — Reporter 接口 + TerminalReporter、ReminderInjector（防死循环）
internal/context/           — Session（历史 + 工作记忆）、Compactor、RecoveryManager、
                              PromptComposer（系统提示词、计划模式）、技能加载器
internal/tools/             — BaseTool 接口、支持中间件的 Registry、
                              read/write/edit_file/bash 工具、子智能体工具
internal/observability/     — CostTracker（包裹 provider，为 Session 计费）、Span 链路追踪，
                              导出到 <workDir>/.claw/traces
internal/feishu/            — 飞书/Lark 机器人 + 审批流（目前未接入 main.go）
```

### 分层规则

- 依赖向内流动：`schema` 不依赖任何包；`engine` 编排 `provider`、`tools`、`context`、`observability`。
- 引擎对文件无状态：**WorkDir 挂在 Session 上**，注入各工具构造函数以约束文件操作范围。
- 工具结果以携带 `ToolCallID` 的 `RoleUser` 消息回传给模型（没有独立的 tool 角色）。
- 工具参数是 `json.RawMessage`；各工具惰性反序列化自己的参数。

## 🚀 快速开始

### 环境要求

- Go 1.25+
- `ZHIPU_API_KEY`（放在 `.env` 或环境变量中，缺失会直接 fatal）
- 飞书机器人（可选）：`FEISHU_APP_ID` / `FEISHU_APP_SECRET`

### 构建与运行

```bash
# 构建 CLI
go build -o claw ./cmd/claw

# 直接运行
go run ./cmd/claw -prompt "帮我阅读当前目录的代码并写一份总结" -dir ./workspace

# 构建模块内所有包
go build ./cmd/... ./internal/...

# 基准测试（会真实调用 LLM API，产生费用）
go run ./cmd/bench
```

> ⚠️ **不要使用 `go build ./...`**：`workspace/main.go` 是空草稿文件会导致构建失败（`cmd/agentops/main.go` 同理）。`workspace/` 是智能体的演示/测试工作目录（未纳入 git），不属于模块。

### CLI 参数

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `-prompt` | 要交给 Agent 执行的任务描述（必填） | — |
| `-dir` | Agent 运行的工作区目录路径 | 当前目录 |
| `-session` | 会话 ID，支持断点续传 | `cli_default_session` |

运行结束后会打印任务总耗时与 Session 累计成本（美元）和 token 消耗。

### 运行测试

```bash
go test ./internal/context/
```

`internal/context/session_test.go` 覆盖 `GetWorkingMemory` 的窗口裁剪逻辑，其余包暂无 `_test.go` 文件。

## 🧠 工作原理

### 1. 装配流程（cmd/claw/main.go）

```
加载 .env → 校验 ZHIPU_API_KEY
    ↓
Session（绑定 WorkDir） ← GlobalSessionMgr.GetOrCreate
    ↓
Provider (glm-4.7-flashx) → CostTracker 包裹 → trackedProvider
    ↓
Registry：read_file / write_file / bash / edit_file + spawn_subagent
readOnlyRegistry：read_file / bash（专供子智能体）
    ↓
AgentEngine(trackedProvider, registry, enableThinking, planMode)
    ↓
eng.Run(ctx, sess, reporter)  →  ReAct 主循环
```

### 2. ReAct 主循环（internal/engine/loop.go）

每一轮 Turn：

1. **上下文组装**：动态构建 System Prompt（PromptComposer）+ 最近 20 条工作记忆。
2. **压缩**：Compactor 检查字符总量，超标时对早期消息掩码化、超大日志掐头去尾。
3. **Thinking 阶段**（可选）：不带工具调用一次 LLM 强制规划，思考内容持久化进 Session。
4. **Action 阶段**：带工具列表调用 LLM，拿到工具调用请求。
5. **并发执行**：所有工具调用在 goroutine 中并发跑，错误经 RecoveryManager 注入救援提示。
6. **Observation**：结果以携带 `ToolCallID` 的 user 消息回写 Session；ReminderInjector 探测死循环。
7. 模型不再请求工具时，任务宣告完成。

### 3. 工作记忆的两档窗口策略

`GetWorkingMemory` 滑动窗口必须满足两条 API 硬校验：

- `ToolCall↔ToolResult` 配对完整（配对断裂 → LLM API 400 Bad Request）；
- 智谱 GLM 要求首条非 system 消息必须是普通 user 消息（否则 400 code=1214）。

因此窗口起点必须落在"普通 user 消息"块边界上，策略分两档：

- **方案 B（优先）**：回退补数 ≤3 条时向前补齐到 user 消息块边界，窗口略超限制；
- **方案 A（兜底）**：回退代价过大时保持窗口大小，剔除头部孤儿 tool 结果，并注入一条合成 user 前缀。

### 4. 子智能体（internal/tools/subagent.go + engine.RunSub）

主智能体调用 `spawn_subagent` 时，引擎拉起一个不依赖外部 Session 的一次性受限循环：

- 只挂载 `readOnlyRegistry`（read_file + bash），**只能看不能改**；
- 最多 10 个 Turn，超时强制召回；
- System Prompt 严厉警告必须用工具找答案、禁止凭空捏造；
- 不再调用工具即视为汇报完成，返回纯文本摘要给主智能体。

为打破 `tools ↔ engine` 包循环依赖，工具侧只依赖 `AgentRunner` 接口，由外部注入引擎实例。

## 📊 可观测性

- **CostTracker**：装饰器模式包裹任意 `LLMProvider`，每次 Generate 后把 token 与成本累加到 Session（`RecordUsage`），任务结束打印总账。
- **Span 链路追踪**：`StartSpan` 通过 `context.Context` 级联父子关系，形成 `CLI.TaskRun → Agent.Run → Turn-N → LM.Thinking / LLM.Action` 的树形结构，任务结束后 JSON 导出到 `<workDir>/.claw/traces/trace_<sessionID>_<时间戳>.json`，可完整回放执行链路。

## 🧰 基准测试（cmd/bench）

三段式评测用例：

```go
observability.TestCase{
    ID:             "test_001_edit",
    Name:           "测试模糊替换工具的准确性",
    SetupScript:    `echo '{"name": "tiny-claw"}' > config.json`,   // 准备靶机
    TaskPrompt:     `使用 edit_file 工具将 version 改为 v2.0.0`,   // 考题
    ValidateScript: `grep '"version": "v2.0.0"' config.json`,      // 判卷
}
```

内置两个用例：模糊替换准确性（edit_file）、代码阅读 + 单测生成综合能力。⚠️ 跑分会真实调用 LLM API，产生费用。

## 💬 飞书机器人（未接线）

`internal/feishu/` 提供完整的飞书/Lark 机器人 + 审批流能力：`FeishuReporter` 把工具执行轨迹推送到群聊，`ApprovalManager` 对危险命令（`IsDangerousCommand`）挂起等待人工审批。目前 `main.go` 中的 HTTP 服务保持注释状态，启用需额外配置 `FEISHU_APP_ID` / `FEISHU_APP_SECRET`。

## 🔧 扩展指南

### 新增工具

1. 在 `internal/tools/` 实现 `BaseTool` 接口：

```go
type BaseTool interface {
    Name() string                                              // 全局唯一名称
    Definition() schema.ToolDefinition                         // 提交给大模型的 JSON Schema
    Execute(ctx context.Context, args json.RawMessage) (string, error) // 执行业务逻辑
}
```

2. 在 `cmd/claw/main.go` 通过 `registry.Register(...)` 注册，引擎每轮动态感知。

### 新增 Provider

1. 在 `internal/provider/` 实现 `LLMProvider` 接口：

```go
type LLMProvider interface {
    Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}
```

2. 加一个读环境变量的构造函数，在 `cmd/claw/main.go` 接线换上即可。当前默认 `provider.NewZhipuOpenAIProvider("glm-4.7-flashx")`；`NewDeepseekClaudeProvider` 走 Anthropic 兼容路线。

### 工具拦截（中间件）

```go
registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
    if call.Name == "bash" && strings.Contains(string(call.Arguments), "rm -rf") {
        return false, "禁止执行删除命令"
    }
    return true, ""
})
```

中间件在工具执行前运行，可以带理由拒绝调用，模型必须读该理由。

## ⚠️ 已知坑点

- **并发写 Session**：`Session` 由 `sync.RWMutex` 保护；并发工具执行时循环变量必须作为函数参数传入 goroutine，避免闭包捕获陷阱。
- **包名怪癖**：`internal/eval/benchmark.go` 位于 `internal/eval/` 目录却声明 `package observability`。导入路径是 `internal/eval`，但引用时写作 `observability.*`。
- **GLM 消息校验**：改动截断逻辑时必须保住 `ToolCall↔ToolResult` 配对，且首条非 system 消息必须是普通 user 消息。
- **引擎构造签名**：`NewAgentEngine(provider, registry, enableThinking, planMode)`——thinking 阶段每轮发两次 LLM 调用（先不带工具做规划，再带工具行动）。
- **飞书接线**：`main.go` 中飞书 HTTP 服务保持注释状态，要有意识地启用，不要顺手打开。

## MIT 许可

个人学习项目，仅供交流学习。
