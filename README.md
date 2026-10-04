# go-agent-claw 🐾

> 一个用 Go 从零打造的 ReAct 风格 AI 智能体引擎（Think → Act → Observe 循环），作为学习项目逐步复刻 Claude-Code 式的核心能力：会话管理、上下文压缩、子智能体、计划模式、链路追踪与成本统计。

## ✨ 核心特性

- **ReAct 主循环**：`Think → Act → Observe` 循环驱动，模型自主决定何时调用工具、何时宣告任务完成。
- **双阶段推理（Thinking 模式）**：开启后每轮发两次 LLM 调用——先剥夺工具强制规划，再带工具精准行动。
- **会话与工作记忆**：`Session` 在进程内维护历史，主循环只发送最近 20 条消息作为短期工作记忆；窗口滑动时自动保住 `ToolCall↔ToolResult` 配对与首条 user 消息的 API 校验。
- **单次运行上限**：每次 Run 返回 RunID、状态与终止原因；默认最多 20 轮、总超时 5 分钟（含排队）、单工具 30 秒、Token 预算 100000，每次模型请求最多输出 4096 Token。自定义工具仍须响应 context。
- **上下文压缩器（Compactor）**：发给模型前对超长上下文做掩码/截断，保护最近消息不被远古日志吃光 token 预算。
- **会话调度**：同一 Session 的用户消息和整次运行按登记顺序执行，等待可取消；不同 Session 可并发。
- **输出与进程回收**：文件有界读取、Shell 有界缓冲保留前 8000 字节并标记截断；容器在取消及正常结束后回收，清理失败会报告。
- **并发工具执行**：连续且显式声明只读的工具默认最多 4 个并发；写入、Shell 和子任务按顺序执行。主子任务共用调度器，结果按原顺序回写。
- **子智能体（Subagent）**：主智能体可通过 `spawn_subagent` 派出探索型子任务，在最多 10 轮内搜集信息并返回摘要；仅提供 read_file、list_files 和 search_text，运行时拒绝写入、进程执行和递归委派。
- **错误自愈（RecoveryManager）**：工具报错在送回模型前自动诊断并注入"救援提示"，引导模型自我修正。
- **死循环探测（ReminderInjector）**：对连续重复的工具调用做指纹识别，触发时注入严厉提醒打破局部执念。
- **可观测性**：`CostTracker` 记录供应商返回的 Token Usage；配置匹配价目表时计算已知费用，缺失价格或 Usage 时标记未知。每次 Run 的 Trace 按 RunID 导出到 `<workDir>/.claw/traces`，导出错误会报告。
- **计划模式（PlanMode）**：PromptComposer 按 `planMode` 开关组装不同的系统提示词，默认关闭。
- **技能加载（SkillLoader）**：从工作区加载 Markdown 技能文件注入系统提示词。
- **双 Provider 支持**：OpenAI 兼容路线（智谱 GLM）与 Anthropic 兼容路线（DeepSeek），一个接口随时切换。
- **权限与审批**：工具必须声明能力，Registry 独立检查；写入和 Shell 需要本地终端逐次审批，无可信来源时拒绝。Shell 仅使用 Docker，不回退宿主机。
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
internal/config/            — CLI/环境配置解析与 Run Options 转换
internal/budget/            — 价目快照、Token/费用预留及结算
internal/policy/            — 能力校验与审批请求绑定
internal/approval/          — 可取消的本地终端审批
internal/workspace/         — os.Root 文件读写边界
internal/executor/          — Docker 容器执行与资源限制
internal/engine/            — Reporter 接口 + TerminalReporter、ReminderInjector（防死循环）
internal/context/           — Session（历史 + 工作记忆）、Compactor、RecoveryManager、
                              PromptComposer（系统提示词、计划模式）、技能加载器
internal/tools/             — BaseTool 接口、支持中间件的 Registry、
                              read/write/edit_file、目录浏览、文本搜索、容器 Shell、子智能体工具
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
- Shell 需要本机 Linux Docker Engine；macOS 使用 Docker Desktop。配置的镜像必须提前安装，例如 `docker pull bash:5.2`。默认镜像只含 Bash 和基础命令，运行 Go 测试需由操作者选择包含 Go 的可信镜像。
- CLI 需要 `OPENAI_API_KEY`；基准测试需要 `ZHIPU_API_KEY`，会真实调用 API
- 选择其他 Provider 时分别设置 `ZHIPU_API_KEY` 或 `DEEPSEEK_API_KEY`；密钥只从环境变量读取，可选 `.env` 只补齐未设置的变量
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
go run ./cmd/bench -approval local
```

> 不要使用 `go build ./...`：`workspace/` 是演示和运行产物目录，可能包含不能参与正式构建的草稿代码。`cmd/agentops` 是可编译的独立飞书服务。

### CLI 参数

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `-prompt` | 要交给 Agent 执行的任务描述；留空时进入 REPL | — |
| `-dir` | Agent 运行的工作区目录路径 | 当前目录 |
| `-session` | 当前进程内复用的会话 ID；重启后不恢复历史 | `cli_default_session` |
| `-provider`、`-model`、`-base-url` | Provider、模型及兼容 API 地址 | `openai`、`gpt-5.6-sol`、数云网关 |
| `-thinking`、`-plan-mode` | Thinking 和计划模式 | 均关闭 |
| `-max-turns`、`-timeout`、`-tool-timeout`、`-max-concurrent-tools` | 轮数、总时限、工具时限、只读并发 | `20`、`5m`、`30s`、`4` |
| `-max-total-tokens`、`-max-output-tokens` | 每 Run Token 预算、每请求输出上限 | `100000`、`4096` |
| `-max-cost`、`-cost-currency`、`-pricing-file` | 可选费用预算、币种、显式价目表 | 均未设置 |
| `-permissions` | `workspace` 或 `read-only` 能力集合 | `workspace` |
| `-approval` | `local` 本地终端逐次确认或 `deny` 拒绝副作用操作 | `local` |
| `-executor`、`-shell-image` | Shell 后端和已安装镜像 | `docker`、`bash:5.2` |
| `-shell-cpus`、`-shell-memory-mb`、`-shell-pids` | 容器 CPU、内存 MiB、进程配额 | `1`、`256`、`64` |
| `-allow-network` | 容器网络能力，启用后仍需逐次审批 | `false` |

同名 `CLAW_*` 环境变量可设置上述 Provider、模型、地址、推理、预算、权限和容器字段；命令行参数优先。支持的 Provider 值为 `openai`、`zhipu-openai`、`zhipu-anthropic`、`deepseek-openai`、`deepseek-anthropic`。单任务运行结束会打印 RunID、终止原因与 Session 累计用量；失败时退出码非零。Session 只存在于当前进程内。

读取和探索无需 Docker。写入及命令执行在 `/dev/tty` 展示参数摘要与完整参数 SHA-256，输入 `yes` 批准该次调用；审批等待计入单工具超时。没有控制终端或设置 `-approval deny` 时拒绝。审批与拒绝只表示一次工具调用结果，模型随后无工具返回仍可能是技术性完成，应检查实际产物。

容器只挂载指定工作区，默认禁用网络、使用非 root、只读根目录和资源限额。工作区中的所有文件仍可能被容器读取，因此请指定不含密钥的目录。原生文件工具禁止 `.env`、`.env.*`、`.git` 路径，并通过 `os.Root` 约束读写；硬链接、挂载点与拥有宿主权限的其他程序仍属于运行环境边界。这是学习原型，不能视为完整多租户沙箱。

费用预算必须同时设置币种及与 Provider、地址、模型匹配的价目表。价目表由操作者提供，不内置推测价格。例如：

```json
{
  "version": "示例版本",
  "rates": [{
    "provider": "openai",
    "base_url": "https://example.invalid/v1/",
    "model": "示例模型",
    "currency": "USD",
    "source": "操作者核对的价格来源",
    "input_per_million": "1.25",
    "output_per_million": "2.50"
  }]
}
```

价格为每百万 Token 的对应币种金额。金额使用整数微单位结算，每次请求向上舍入到微单位。未配置匹配价格时费用显示“未知”；不能把它当作免费。Token/费用上限控制本地后续请求准入，输入 Token 使用请求 JSON 字节数加协议开销估算；供应商实际 Usage 和最终账单可能高于本地估算或上限。

### 运行测试

完整步骤、覆盖缺口、真实模型与 Linux 门槛见 [综合验收方案](docs/acceptance/comprehensive-acceptance-plan.md)，结果使用 [验收报告模板](docs/acceptance/report-template.md) 记录。

2026-10-04 的 [本地综合验收报告](docs/acceptance/report-2026-10-04-macos.md) 已完成 47 个必测项；真实模型接入和原生 Linux 部署仍待验收。

```bash
go test ./cmd/... ./internal/...
go test -race ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
go build ./cmd/... ./internal/...
# 显式选择本机 Docker 验收，不调用 LLM；需提前安装 shell-image 默认镜像
CLAW_TEST_DOCKER=1 go test -race ./internal/executor -run '^TestDocker(Integration|Acceptance)$' -count=1 -timeout=180s -v
# 可选：Python 3 标准库伪终端交互验收，不占用用户终端、不调用真实 API
python3 scripts/test-local-approval.py
# 完整交互流程：需 Docker 可用；不调用真实 API，证据保存在仓库外新目录
python3 scripts/test-comprehensive-acceptance.py --evidence-dir /tmp/claw-comprehensive-new
```

测试覆盖 M1–M3 的运行边界、排队与取消、预算与 Trace，以及阶段 2 的权限、文件越界、只读子任务、审批绑定与取消。两类 Provider 使用本机伪服务验证多工具重放、实际 HTTP 取消和异常响应。完整交互脚本覆盖命令、多行/EOF、计划续接、子任务审批编辑、后端失败恢复和越权指令。容器验收覆盖非 root、CapEff/NoNewPrivs、挂载、受控网络对照、配额/OOM/PID 耗尽、后台与取消回收。普通 Go 测试不依赖 Docker；目标原生 Linux 主机验收仍待执行。

## 🧠 工作原理

### 1. 装配流程（cmd/claw/main.go）

```
读取 CLI/环境配置（可选 .env 只填缺省值）→ 校验对应 Provider 密钥
    ↓
Session（绑定 WorkDir） ← GlobalSessionMgr.GetOrCreate
    ↓
Provider（默认 gpt-5.6-sol，数云兼容网关）→ CostTracker 包裹 → trackedProvider
    ↓
Registry：read_file / list_files / search_text / write_file / edit_file / bash + spawn_subagent
readOnlyRegistry：read_file / list_files / search_text（专供子智能体）
    ↓
AgentEngine(trackedProvider, registry, enableThinking, planMode)
    ↓
eng.Submit(ctx, sess, prompt, reporter, cfg.RunOptions())
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

- 挂载 `readOnlyRegistry`（read_file + list_files + search_text）；同时限制执行 context 的能力，拒绝写入、Shell、网络和递归委派；
- 最多 10 个 Turn，继承父任务 context、工具超时和并发配置；
- System Prompt 严厉警告必须用工具找答案、禁止凭空捏造；
- 不再调用工具即视为汇报完成，返回纯文本摘要给主智能体。

为打破 `tools ↔ engine` 包循环依赖，工具侧只依赖 `AgentRunner` 接口，由外部注入引擎实例。

## 📊 可观测性

- **CostTracker**：装饰器模式包裹 `LLMProvider`；每次请求前原子预留估算输入和输出上限，返回 Usage 后结算。Thinking、Action 与子任务共享一次 Run 的预算。缺失 Usage 时保留预留并停止后续请求；Session 同时记录已知金额和未知请求数。估算规则与版本记录在运行账本及请求记录中。
- **Span 链路追踪**：`StartSpan` 通过 `context.Context` 级联父子关系，形成 `Agent.Run → Turn-N → LLM.Thinking / LLM.Action` 的树形结构。委派工具下的 `Agent.Subagent` 记录同一 RunID/SessionID 及子任务 `LLM.Action`，预算仍共用父 Run 账本。JSON 按 RunID 排他导出到 `<workDir>/.claw/traces/trace_<RunID>.json`。Trace 包含预算摘要、终止原因及审批 ID、参数摘要哈希、主体和期限，不写完整提示词、工具参数或工具输出；导出失败保留原运行状态并报告错误。

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

评测默认 `-approval deny`，有写入的用例需要显式 `-approval local` 并在终端逐次确认。Setup/Validate 脚本是操作者提供的宿主测试脚本，不能用于不可信输入。

内置两个用例：模糊替换准确性（edit_file）、代码阅读 + 单测生成综合能力。⚠️ 跑分会真实调用 LLM API，产生费用。

## 💬 飞书机器人（独立入口）

`cmd/agentops` 是可编译的独立飞书 HTTP 服务，使用 `internal/feishu/` 的事件处理、Reporter 和审批管理。它与 CLI 分开启动；同会话运行已串行。当前默认 `read-only`、`deny`；旧卡片审批没有完整主体鉴权，因此不能为新权限层授权。即使显式配置工作区能力，写入和进程执行仍因缺少可信审批来源而拒绝。全局队列容量、远程审批鉴权与跨会话隔离仍待实现。

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

2. 实现 `RequiredCapabilities() []policy.Capability`；未声明能力的工具默认拒绝。声明只读工具还需 `ReadOnly() bool`，且仅使用读取能力。

3. 在 `cmd/claw/main.go` 通过 `registry.Register(...)` 注册，引擎每轮动态感知。自行装配时用 `NewRegistryWithPolicy` 注入能力和可信审批来源；`NewRegistry` 没有审批来源，写入及进程执行默认拒绝。

### 新增 Provider

1. 在 `internal/provider/` 实现 `LLMProvider` 接口：

```go
type LLMProvider interface {
    Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}
```

2. 在构造函数内集中读取环境变量，并在入口接线。CLI 和飞书入口使用 `provider.NewConfigured`；缺密钥返回错误。新增适配器也应实现 `InputEstimator` 并接收每请求输出上限，以支持预算准入。

### 工具拦截（中间件）

```go
registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
    if call.Name == "bash" && strings.Contains(string(call.Arguments), "rm -rf") {
        return false, "禁止执行删除命令"
    }
    return true, ""
})
```

中间件在权限与审批检查后、工具执行前运行，可以带理由拒绝调用；它不能授予权限或代替审批。

## ⚠️ 已知坑点

- **并发写 Session**：通过 `Submit`/`Start` 提交任务，消息追加也必须在排队边界内；历史和账单使用锁保护，账单通过 `UsageSnapshot()` 读取。
- **包名怪癖**：`internal/eval/benchmark.go` 位于 `internal/eval/` 目录却声明 `package observability`。导入路径是 `internal/eval`，但引用时写作 `observability.*`。
- **GLM 消息校验**：改动截断逻辑时必须保住 `ToolCall↔ToolResult` 配对，且首条非 system 消息必须是普通 user 消息。
- **引擎构造签名**：`NewAgentEngine(provider, registry, enableThinking, planMode)`——thinking 阶段每轮发两次 LLM 调用（先不带工具做规划，再带工具行动）。
- **飞书入口**：`cmd/agentops` 是独立 HTTP 服务；CLI 不会启动它。当前入口默认只读；远程审批身份校验仍待实现。

## MIT 许可

个人学习项目，仅供交流学习。
