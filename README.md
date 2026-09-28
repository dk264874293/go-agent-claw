# go-agent-claw 🐾

> 一个用 Go 从零打造的 ReAct 风格 AI 智能体引擎（Think → Act → Observe 循环），作为学习项目逐步复刻 Claude-Code 式的核心能力：会话管理、上下文压缩、子智能体、计划模式、链路追踪与成本统计。

## ✨ 核心特性

- **ReAct 主循环**：`Think → Act → Observe` 循环驱动，模型自主决定何时调用工具、何时宣告任务完成。
- **双阶段推理（Thinking 模式）**：开启后每轮发两次 LLM 调用——先剥夺工具强制规划，再带工具精准行动。
- **会话与工作记忆**：`Session` 在进程内维护历史，主循环只发送最近 20 条消息作为短期工作记忆；窗口滑动时自动保住 `ToolCall↔ToolResult` 配对与首条 user 消息的 API 校验。
- **单次运行上限**：每次 Run 返回 RunID、状态与终止原因；当前入口使用最多 20 轮、总超时 5 分钟（含排队），单工具默认 30 秒。自定义工具仍须响应 context。
- **上下文压缩器（Compactor）**：发给模型前对超长上下文做掩码/截断，保护最近消息不被远古日志吃光 token 预算。
- **会话调度**：同一 Session 的用户消息和整次运行按登记顺序执行，等待可取消；不同 Session 可并发。
- **输出与进程回收**：文件有界读取、Shell 有界缓冲保留前 8000 字节并标记截断；Linux/macOS 下取消及正常结束清理同组后台进程。主动脱离进程组仍需后续隔离治理。
- **并发工具执行**：连续且显式声明只读的工具默认最多 4 个并发；写入、Shell 和子任务按顺序执行。主子任务共用调度器，结果按原顺序回写。
- **子智能体（Subagent）**：主智能体可通过 `spawn_subagent` 派出探索型子任务，在最多 10 轮内搜集信息并返回摘要；当前子任务仍有通用 Bash，尚无可靠的只读隔离。
- **错误自愈（RecoveryManager）**：工具报错在送回模型前自动诊断并注入"救援提示"，引导模型自我修正。
- **死循环探测（ReminderInjector）**：对连续重复的工具调用做指纹识别，触发时注入严厉提醒打破局部执念。
- **可观测性**：`CostTracker` 记录 Token 与已配置模型的估算费用；`Span` 树形链路追踪导出到 `<workDir>/.claw/traces`。默认模型尚无价格配置。
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
internal/engine/            — AgentEngine：有上限的 ReAct 主循环 + 子智能体的 RunSub
internal/run/               — RunOptions、运行状态和终止原因
internal/engine/            — Reporter 接口 + TerminalReporter、ReminderInjector（防死循环）
internal/context/           — Session（历史 + 工作记忆）、Compactor、RecoveryManager、
                              PromptComposer（系统提示词、计划模式）、技能加载器
internal/tools/             — BaseTool 接口、支持中间件的 Registry、
                              read/write/edit_file/bash 工具、子智能体工具
internal/observability/     — CostTracker（包裹 provider，为 Session 计费）、Span 链路追踪，
                              导出到 <workDir>/.claw/traces
internal/feishu/            — 飞书/Lark 机器人 + 审批流（入口为独立的 cmd/agentops）
```

### 分层规则

- 依赖向内流动：`schema` 不依赖任何包；`engine` 编排 `provider`、`tools`、`context`、`observability`。
- 引擎对文件无状态：**WorkDir 挂在 Session 上**，注入各工具构造函数以约束文件操作范围。
- 工具结果以携带 `ToolCallID` 的 `RoleUser` 消息回传给模型（没有独立的 tool 角色）。
- 工具参数是 `json.RawMessage`；各工具惰性反序列化自己的参数。

## 🚀 快速开始

### 环境要求

- Go 1.25+
- CLI 需要 `OPENAI_API_KEY`；基准测试需要 `ZHIPU_API_KEY`，会真实调用 API
- 独立飞书服务需要 `ZHIPU_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`

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

> 不要使用 `go build ./...`：`workspace/` 是演示和运行产物目录，可能包含不能参与正式构建的草稿代码。`cmd/agentops` 是可编译的独立飞书服务。

### CLI 参数

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `-prompt` | 要交给 Agent 执行的任务描述；留空时进入 REPL | — |
| `-dir` | Agent 运行的工作区目录路径 | 当前目录 |
| `-session` | 当前进程内复用的会话 ID；重启后不恢复历史 | `cli_default_session` |

单任务运行结束后会打印 RunID、终止原因、总耗时与 Session 累计用量；失败时退出码非零。当前费用价格表未覆盖默认模型，费用显示不能作为准确账单。

### 运行测试

```bash
go test ./internal/...
go test ./cmd/claw
```

测试覆盖工作记忆、M1 运行边界、M2 排队与取消、工具调度、输出上限、Shell 进程回收、飞书提交/审批及账单快照；这些测试使用 Mock Provider，不调用真实 API。

## 🧠 工作原理

### 1. 装配流程（cmd/claw/main.go）

```
加载 .env → 校验 OPENAI_API_KEY
    ↓
Session（绑定 WorkDir） ← GlobalSessionMgr.GetOrCreate
    ↓
Provider (gpt-5.6-sol，数云兼容网关) → CostTracker 包裹 → trackedProvider
    ↓
Registry：read_file / write_file / bash / edit_file + spawn_subagent
readOnlyRegistry：read_file / bash（专供子智能体）
    ↓
AgentEngine(trackedProvider, registry, enableThinking, planMode)
    ↓
eng.Submit(ctx, sess, prompt, reporter, run.Options{MaxTurns: 20, Timeout: 5*time.Minute})
  → Session 排队 → 追加用户消息 → ReAct 主循环
```

### 2. ReAct 主循环（internal/engine/loop.go）

每一轮 Turn：

1. **上下文组装**：动态构建 System Prompt（PromptComposer）+ 最近 20 条工作记忆。
2. **压缩**：Compactor 检查字符总量，超标时对早期消息掩码化、超大日志掐头去尾。
3. **Thinking 阶段**（可选）：不带工具调用一次 LLM 强制规划，思考内容追加到内存 Session。
4. **Action 阶段**：带工具列表调用 LLM，拿到工具调用请求。
5. **工具调度**：连续只读调用限流并发，其他工具形成顺序屏障；错误经 RecoveryManager 注入救援提示，截断/超时/取消元数据保留在 Observation。
6. **Observation**：结果以携带 `ToolCallID` 的 user 消息回写 Session；ReminderInjector 探测死循环。
7. 模型不再请求工具时，Run 标为技术性完成；达到轮数或总超时时返回对应终止原因。技术性完成不代表任务经过验收。

### 3. 工作记忆的两档窗口策略

`GetWorkingMemory` 滑动窗口必须满足两条 API 硬校验：

- `ToolCall↔ToolResult` 配对完整（配对断裂 → LLM API 400 Bad Request）；
- 智谱 GLM 要求首条非 system 消息必须是普通 user 消息（否则 400 code=1214）。

因此窗口起点必须落在"普通 user 消息"块边界上，策略分两档：

- **方案 B（优先）**：回退补数 ≤3 条时向前补齐到 user 消息块边界，窗口略超限制；
- **方案 A（兜底）**：回退代价过大时保持窗口大小，剔除头部孤儿 tool 结果，并注入一条合成 user 前缀。

### 4. 子智能体（internal/tools/subagent.go + engine.RunSub）

主智能体调用 `spawn_subagent` 时，引擎拉起一个不依赖外部 Session 的一次性受限循环：

- 挂载 `readOnlyRegistry`（read_file + bash）；通用 Bash 仍可写入工作区外文件，当前并不真正只读；
- 最多 10 个 Turn，继承父任务 context、工具超时和并发配置；
- System Prompt 严厉警告必须用工具找答案、禁止凭空捏造；
- 不再调用工具即视为汇报完成，返回纯文本摘要给主智能体。

为打破 `tools ↔ engine` 包循环依赖，工具侧只依赖 `AgentRunner` 接口，由外部注入引擎实例。

## 📊 可观测性

- **CostTracker**：装饰器模式包裹 `LLMProvider`，将 Token 与已配置模型的估算费用累加到内存 Session；默认模型费用当前未配置，不应把显示的零费用当作真实账单。
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

## 💬 飞书机器人（独立入口）

`cmd/agentops` 是可编译的独立飞书 HTTP 服务，使用 `internal/feishu/` 的事件处理、Reporter 和审批管理。它与 CLI 分开启动；同会话运行已串行，审批等待受工具超时和 context 取消控制；全局队列容量、审批鉴权与跨会话隔离仍待实现，尚不适合多用户试点。

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

2. 在构造函数内集中读取环境变量，并在入口接线。当前 CLI 默认 `provider.NewOpenAIProvider("gpt-5.6-sol")`；`NewDeepseekClaudeProvider` 走 Anthropic 兼容路线。

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

- **并发写 Session**：通过 `Submit`/`Start` 提交任务，消息追加也必须在排队边界内；历史和账单使用锁保护，账单通过 `UsageSnapshot()` 读取。
- **包名怪癖**：`internal/eval/benchmark.go` 位于 `internal/eval/` 目录却声明 `package observability`。导入路径是 `internal/eval`，但引用时写作 `observability.*`。
- **GLM 消息校验**：改动截断逻辑时必须保住 `ToolCall↔ToolResult` 配对，且首条非 system 消息必须是普通 user 消息。
- **引擎构造签名**：`NewAgentEngine(provider, registry, enableThinking, planMode)`——thinking 阶段每轮发两次 LLM 调用（先不带工具做规划，再带工具行动）。
- **飞书入口**：`cmd/agentops` 是独立 HTTP 服务；CLI 不会启动它。审批等待已支持超时与 context 取消，审批身份校验仍待实现。

## MIT 许可

个人学习项目，仅供交流学习。
