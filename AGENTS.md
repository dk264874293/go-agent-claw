# AGENTS.md

## 项目定位

go-agent-claw 是一个使用 Go 编写的 ReAct 风格 AI 智能体引擎，通过
`Think -> Act -> Observe` 循环逐步复刻 Claude Code 类工具的核心能力，包括：

- CLI 单任务与交互式 REPL
- Session、工作记忆和上下文压缩
- 并发工具调用与错误恢复
- 探索型子智能体（当前尚无可靠的只读隔离）
- 计划模式和本地技能加载
- Provider 适配、Token/成本统计和链路追踪
- 飞书机器人与高风险操作审批
- 基于真实 LLM 的基准测试

这是一个学习型、功能型原型，不是具备完整沙箱和资源治理能力的生产执行器。
代码注释、日志和面向用户的字符串统一使用中文，请保持这一风格。

`CLAUDE.md` 只引用本文件，不需要单独维护。

## 当前运行配置

CLI 入口位于 `cmd/claw/main.go`，当前默认配置为：

- Provider：`provider.NewOpenAIProvider`
- 模型：`gpt-5.6-sol`
- API 地址：数云 OpenAI 兼容网关
- 环境变量：`OPENAI_API_KEY`
- Thinking：关闭
- PlanMode：开启
- 主工具：`read_file`、`write_file`、`edit_file`、`bash`、`spawn_subagent`
- 子智能体工具：`read_file`、`bash`

其他入口使用不同配置：

- `cmd/bench` 使用智谱 Provider，需要 `ZHIPU_API_KEY`，会真实调用 API 并产生费用。
- `cmd/agentops` 是可编译的飞书 HTTP 服务，不是空文件；它使用智谱 Provider，至少需要
  `ZHIPU_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`。
- Anthropic 兼容 Provider 还支持 DeepSeek 和智谱，切换时检查对应构造函数及环境变量。

不要读取、打印、提交或修改 `.env` 中的密钥。

## 常用命令

```bash
go build -o claw ./cmd/claw        # 构建 CLI
go run ./cmd/claw                  # 进入交互式 REPL
go run ./cmd/claw -prompt "任务"  # 执行单次任务
go build ./cmd/... ./internal/...  # 构建模块内的正式包
go test ./internal/...             # 运行现有测试
go test -race ./internal/context/  # 检查 Session 测试中的数据竞争
go vet ./cmd/... ./internal/...    # 静态检查
gofmt -w <修改过的.go文件>         # 格式化本次修改的 Go 文件
```

- 不要使用 `go build ./...`：模块根目录下的 `workspace/` 是演示和运行产物目录，可能包含不能参与正式构建的草稿代码。
- 不要在未得到明确要求时运行 `go run ./cmd/bench`，它会调用真实 LLM API 并产生费用。
- 当前自动化测试覆盖 `internal/context`、`internal/engine`、`internal/tools` 与 CLI 的 M1 路径；修改其他核心模块时必须补充相应测试。
- 仓库当前要求 Go 1.25 或更高版本。
- REPL 内置 `/help`、`/exit`、`/cost`、`/status`、`/clear`。独占一行的 `"""` 用于开始或结束多行输入。

## 架构与调用链

```text
cmd/claw
  -> Provider
  -> CostTracker
  -> Session
  -> AgentEngine.Run
       -> PromptComposer（核心提示词 + AGENTS.md + 本地 Skills）
       -> GetWorkingMemory(20)
       -> Compactor
       -> Provider.Generate
       -> Registry.Execute（同一轮工具并发执行）
       -> ToolResult 回写 Session
       -> 下一轮推理或结束
```

主要目录：

```text
cmd/claw/                   CLI 与 REPL 入口
cmd/bench/                  真实 LLM 基准测试入口
cmd/agentops/               飞书 AgentOps HTTP 服务入口
internal/schema/            Message、ToolCall、ToolResult 等共享类型
internal/provider/          OpenAI 与 Anthropic 兼容 Provider
internal/engine/            ReAct 主循环、Reporter、ReminderInjector
internal/run/               单次运行选项、状态与终止原因
internal/context/           Session、Compactor、Recovery、PromptComposer、SkillLoader
internal/tools/             Registry 与文件、Shell、子智能体工具
internal/observability/     CostTracker 与本地 JSON Trace
internal/feishu/            飞书事件处理、Reporter 和审批管理
internal/eval/              基准测试 Runner
workspace/                  演示、评测和运行产物，不属于正式模块
```

### 分层约束

- `schema` 是最内层共享类型，不应依赖其他业务包。
- `engine` 负责流程编排，依赖 `provider`、`context`、`tools` 和 `observability`。
- WorkDir 属于 Session，并在创建具体工具时注入；不要重新把全局工作目录放回 Engine。
- 工具参数使用 `json.RawMessage`，由具体工具负责反序列化。
- 工具结果使用带 `ToolCallID` 的 `RoleUser` 消息表示，没有独立的内部 tool role。
- `internal/eval/benchmark.go` 位于 `internal/eval`，但包名是 `observability`；导入路径和引用名并不一致。

## 关键不变量

### 工作记忆

`Session.GetWorkingMemory(20)` 必须同时保证：

1. ToolCall 与对应 ToolResult 不被窗口裁剪拆散。
2. 首条非 system 消息是没有 `ToolCallID` 的普通 user 消息，以兼容智谱消息校验。
3. 返回 Session 数据的副本，避免外部修改历史。

现有逻辑使用两档策略：回退不超过 3 条时补齐到普通 user 边界；回退过多时剔除头部孤儿工具结果，并按需注入合成 user 前缀。修改这段逻辑时必须保留并扩展 `session_test.go`。

### 并发工具执行

- 同一轮工具调用由 goroutine 并发执行。
- 每个 goroutine 只能写预分配结果切片中属于自己的索引。
- 循环索引和 `ToolCall` 必须作为 goroutine 参数传入，不要依赖闭包捕获。
- `Session` 的历史和账单由同一把 `sync.RWMutex` 保护；新增 Session 字段时应通过加锁方法访问。
- Registry 当前不支持运行期间并发注册工具或中间件，工具装配必须在 `Run` 前完成。

### Provider

- Provider 必须完整保存 assistant 的文本、ToolCalls、ToolCall ID 和 Usage。
- 重放历史时必须把 assistant ToolCalls 和 tool result 的关联关系原样转换回供应商协议。
- 添加 Provider 时实现 `LLMProvider.Generate(ctx, messages, tools)`，并确保 `context.Context` 能取消网络请求。
- 不要在构造函数之外散落供应商 URL、模型名或环境变量读取逻辑。

## 已知风险与缺陷

以下是当前事实，不要把对应注释或命名误认为已经实现了安全保证。

### P0：工具不是沙箱

- `read_file`、`write_file`、`edit_file` 目前只使用 `filepath.Join`，没有阻止 `..`、绝对路径或符号链接逃逸。
- `bash` 只是把 `cmd.Dir` 设置为 WorkDir，仍能访问和修改进程权限允许的任意位置。
- CLI 主 Registry 没有挂载审批或危险命令拦截中间件。
- 子智能体所谓的 `readOnlyRegistry` 包含任意 `bash`，因此并不真正只读。

涉及文件工具、安全边界或子智能体时，优先建立统一的路径解析函数和真正受限的只读命令能力。不要继续使用“锁定工作区”“物理隔离”等描述，除非实现和测试已经能够证明这些性质。

### P0：主 Agent 的资源限制尚未完整

`AgentEngine.Run` 已有 `MaxTurns` 和基于 context 的总超时，并由 Mock Provider 验证；CLI 和飞书入口目前使用固定的 20 轮、5 分钟。Token、费用、工具并发仍无硬性上限，Shell 后代进程及不响应 context 的工具仍可能超过时限。ReminderInjector 只是提示词干预，不能作为资源限制。

### P1：工具输出与取消链路尚不完整

- 主循环现将 RecoveryManager 增强后的错误写入 Observation；`bash` 非零退出和子任务失败会标记为错误；ReminderInjector 检查同轮全部工具结果。相关 Mock、Registry 和 BashTool 测试已补齐。
- `read_file` 使用 `io.ReadAll` 后才截断，`bash` 使用 `CombinedOutput` 后才截断；两者都不能真正防止超大输入导致内存占用。
- 飞书审批等待尚不响应 context，Shell 后代进程回收与不合作工具退出仍待 M2 实现。

修复这些行为时要以 `ToolResult.IsError` 的语义一致性为准，并添加 Registry、BashTool 和 Engine 级测试。

### P1：状态、成本和追踪尚不可靠

- Session 只存在于进程内存，`-session` 只能在当前进程内复用，不能实现重启后的历史恢复。
- 默认模型 `gpt-5.6-sol` 不在 `PricingModel` 中，因此当前费用总是零。
- 价格表注释使用美元，字段和终端展示使用 CNY/人民币符号，计费币种尚未统一。
- Trace 文件使用秒级时间戳，同一 Session 在同一秒导出时可能覆盖；目录创建错误也被忽略。

不要向用户宣称会话已经持久化或成本已经准确，除非相关实现和测试已补齐。

### P1：飞书并发与审批

- 同一 chat 的多条消息可以同时启动多个 Engine，并交叉写入同一个 Session；单次 Append 加锁不能保证整轮消息顺序。
- 审批等待没有超时和 context 取消，无人处理时会永久阻塞。
- 飞书 HTTP 服务位于 `cmd/agentops`，与 CLI 是两个独立入口；不要在 `cmd/claw` 中顺手启用它。

### P2：默认行为和仓库卫生

- CLI 默认开启 PlanMode，简单请求也可能创建或修改工作区根目录下的 `PLAN.md`、`TODO.md`。
- SkillLoader 会把 `.claw/skills` 下所有 `SKILL.md` 正文一次性注入系统提示词，没有按触发条件渐进加载。
- 多数旧 Go 文件尚未统一 gofmt；修改文件时只格式化本次涉及的 Go 文件，避免无关大范围变更。
- `.claw/`、`.zcode/`、`workspace/` 当前会显示为未跟踪目录。不要把运行生成的 trace、计划文件、评测工作区或二进制提交到仓库。
- README 中仍可能存在智谱默认 Provider、环境变量和 Session 持久化等旧描述，修改相关功能时同步更新 README 与本文件。

## 修改指南

### 新增工具

1. 在 `internal/tools/` 实现 `BaseTool`。
2. 明确参数校验、输出上限、取消行为和错误语义。
3. 在相应入口的 Registry 中注册。
4. 添加成功、非法参数、底层失败和越界访问测试。
5. 涉及写操作或 Shell 时，同时评估 CLI、子智能体和飞书审批路径。

### 新增或切换 Provider

1. 实现或复用 `LLMProvider`。
2. 在入口集中配置模型和构造函数。
3. 同步环境变量校验、价格表、README 和本文件。
4. 验证普通消息、并行 ToolCalls、ToolResult 重放、空响应、Usage 和 context 取消。
5. 不要为了验证而直接运行付费调用；优先使用 Mock Provider，真实调用需用户明确要求。

### 修改 Engine 或 Session

至少覆盖以下场景：

- 模型不调用工具时正常结束。
- 单个和并行工具调用按原顺序回写。
- 工具失败时增强错误真正到达下一轮模型上下文。
- 达到 Turn、时间、Token 或成本限制时确定性退出。
- 工作记忆裁剪后没有孤儿 ToolResult。
- 并发或取消时不会出现数据竞争和 goroutine 泄漏。

## 完成标准

提交修改前至少执行与改动范围相符的检查：

```bash
gofmt -w <修改过的.go文件>
go test ./internal/...
go vet ./cmd/... ./internal/...
go build ./cmd/... ./internal/...
```

若修改 Session、并发执行、飞书并发或 CostTracker，再运行相关 `-race` 测试。若因为需要外部密钥、真实费用或缺少环境而没有执行某项验证，必须在交付说明中明确写出。
