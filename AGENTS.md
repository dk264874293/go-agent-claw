# AGENTS.md

## 项目定位

go-agent-claw 是一个使用 Go 编写的 ReAct 风格 AI 智能体引擎，通过
`Think -> Act -> Observe` 循环逐步复刻 Claude Code 类工具的核心能力，包括：

- CLI 单任务与交互式 REPL
- Session、工作记忆和上下文压缩
- 并发工具调用与错误恢复
- 探索型子智能体（读取能力限制与受限工具集合）
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
- PlanMode：默认关闭，可通过 CLI/`CLAW_PLAN_MODE` 开启
- 默认运行上限：20 轮、5 分钟、单工具 30 秒、只读并发 4、每 Run 100000 Token、每请求最多输出 4096 Token
- 可选价目表：由操作者提供，按 Provider、网关地址和模型匹配；缺价时费用为未知
- 主工具：`read_file`、`list_files`、`search_text`、`write_file`、`edit_file`、`bash`、`spawn_subagent`
- 权限：`workspace`；写入与 Shell 逐次本地终端审批，无可信来源时拒绝
- Shell：本机 Linux Docker Engine（macOS 用 Docker Desktop），已安装 `bash:5.2`；默认网络关闭、1 CPU、256 MiB、64 PID；后端不可用时不回退宿主机
- 子智能体工具：`read_file`、`list_files`、`search_text`；子任务 context 只能缩小能力

其他入口使用不同配置：

- `cmd/bench` 使用智谱 Provider，需要 `ZHIPU_API_KEY`，会真实调用 API 并产生费用。默认审批 deny；写入用例需显式 local 并逐次确认。Setup/Validate 是操作者提供的宿主脚本，不能接受不可信输入。
- `cmd/agentops` 是可编译的飞书 HTTP 服务，不是空文件；它使用智谱 Provider，至少需要
  `ZHIPU_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`。默认 read-only/deny，旧飞书卡片不能为新权限层授权。
- Anthropic 兼容 Provider 还支持 DeepSeek 和智谱，切换时检查对应构造函数及环境变量。

不要读取、打印、提交或修改 `.env` 中的密钥。

## 常用命令

```bash
go build -o claw ./cmd/claw        # 构建 CLI
go run ./cmd/claw                  # 进入交互式 REPL
go run ./cmd/claw -prompt "任务"  # 执行单次任务
go build ./cmd/... ./internal/...  # 构建模块内的正式包
go test ./cmd/... ./internal/...   # 运行现有测试与 CLI 综合回归
go test -race ./internal/context/  # 检查 Session 测试中的数据竞争
go vet ./cmd/... ./internal/...    # 静态检查
gofmt -w <修改过的.go文件>         # 格式化本次修改的 Go 文件
```

- 不要使用 `go build ./...`：模块根目录下的 `workspace/` 是演示和运行产物目录，可能包含不能参与正式构建的草稿代码。
- 不要在未得到明确要求时运行 `go run ./cmd/bench`，它会调用真实 LLM API 并产生费用。
- 当前自动化测试覆盖 `internal/context`、`internal/engine`、`internal/tools`、`internal/observability`、`internal/feishu` 、权限、根目录文件操作、审批和容器执行，以及 CLI 的 M1–M3 综合路径；修改其他核心模块时必须补充相应测试。
- 仓库当前要求 Go 1.25 或更高版本。
- REPL 内置 `/help`、`/exit`、`/cost`、`/status`、`/clear`。独占一行的 `"""` 用于开始或结束多行输入。

## 架构与调用链

```text
cmd/claw
  -> Provider
  -> CostTracker
  -> Session
  -> AgentEngine.Submit（会话排队、追加消息、运行）
       -> PromptComposer（核心提示词 + AGENTS.md + 本地 Skills）
       -> GetWorkingMemory(20)
       -> Compactor
       -> Provider.Generate
       -> ExecuteBatch -> Registry.Execute（只读限流并发，其他工具顺序执行）
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
internal/config/            入口配置解析、校验和 Run Options 转换
internal/budget/            价目快照、请求预留与共享预算账本
internal/policy/            能力与审批绑定
internal/approval/          本地终端审批适配
internal/workspace/         根目录约束文件访问
internal/executor/          Docker 隔离后端
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

- 只有显式声明 `ReadOnly()` 且仅需读取能力的连续工具调用可并发，默认最多 4 个 worker；写入、Shell、子任务和未知工具构成顺序屏障。主任务和子任务共用调度器。
- 每个 worker 只能写预分配结果切片中属于自己的索引，循环边界通过 goroutine 参数传入。
- 所有入口通过 `Submit`/`Start` 把追加消息和整次运行纳入 Session FIFO 队列；等待可取消，排队时间计入总超时。不要在提交前自行追加消息。
- `Session` 的历史、运行票据和账单由同一把 `sync.RWMutex` 保护；账单通过 `UsageSnapshot()` 读取，历史返回嵌套数据的深拷贝。新增字段通过加锁方法访问。
- Registry 当前不支持运行期间并发注册工具或中间件，工具装配必须在 `Run` 前完成。

### Provider

- Provider 必须完整保存 assistant 的文本、ToolCalls、ToolCall ID 和 Usage。
- 重放历史时必须把 assistant ToolCalls 和 tool result 的关联关系原样转换回供应商协议。
- 添加 Provider 时实现 `LLMProvider.Generate(ctx, messages, tools)`，并确保 `context.Context` 能取消网络请求。
- 不要在构造函数之外散落供应商 URL、模型名或环境变量读取逻辑。

## 已知风险与缺陷

以下是当前事实，不要把对应注释或命名误认为已经实现了安全保证。

### P0：隔离边界与平台限制

- 文件读写统一通过 `internal/workspace` 的 `os.Root`；禁止绝对路径、`..`、`.env` 和 `.git` 路径，读取只接受常规文件，写入使用原子替换。它不是进程沙箱，硬链接和挂载点仍须治理。
- `bash` 仅通过 Docker Executor 执行：非 root、只读根目录、资源配额、默认网络关闭，只挂载指定工作区。容器可读取工作区内文件，操作者必须选择不含密钥的工作区。
- CLI Registry 在中间件之前独立检查能力和本地审批。授权绑定请求 ID、RunID、SessionID、工具、参数哈希、主体与期限。未声明能力或缺少可信审批来源时拒绝。
- 子智能体只有受限读取工具，同时由 context 限制能力，拒绝写入、Shell、网络和递归委派。模型文本不能授予权限。
- macOS Docker Desktop Linux VM 已验证；目标原生 Linux 主机运行验收尚待执行。隔离后端不可用时必须明确失败，不得自动回退宿主 Shell。

涉及文件工具、安全边界或子智能体时复用统一权限和路径层。不要使用“物理隔离”等绝对保证；完整多租户防护仍不属于此原型能力。

### P0：主 Agent 的资源限制尚未完整

`AgentEngine.Run` 有 `MaxTurns` 和基于 context 的总超时；CLI、飞书和 benchmark 通过统一配置传入上限。默认 20 轮、5 分钟、每 Run 100000 Token；单工具 30 秒（包含审批等待），只读批次最多 4 个 worker。每请求明确设置输出 Token 上限，默认 4096。`CostTracker` 请求前估算并原子预留输入、输出及可选费用；Thinking、Action 与子任务共享 Run 账本。输入估算使用序列化请求 JSON 的 UTF-8 字节数加协议开销，供应商实际 Usage/账单可能超出本地上限；缺失 Usage 时保留预留并停止后续请求。费用预算只有显式匹配价格时才可启用。不响应 context 的自定义工具、阻塞的文件系统 I/O 仍需后续治理；Shell 正常结束和取消后回收整个容器，清理错误必须可见。ReminderInjector 只是提示词干预，不能作为资源限制。

### 工具输出与取消约定

- 主循环现将 RecoveryManager 增强后的错误写入 Observation；`bash` 非零退出和子任务失败会标记为错误；ReminderInjector 检查同轮全部工具结果。相关 Mock、Registry 和 BashTool 测试已补齐。
- `read_file` 最多读取 8001 字节以检测截断；`bash` 持续排空输出，仅保留前 8000 字节。Registry 限制最终工具结果长度，但自定义工具内部仍须自行限制分配。
- `ToolResult` 和 Observation 保留 `Truncated`、`TimedOut`、`Cancelled`、`Denied`、审批不可用/拒绝与资源耗尽状态；超时和取消必须标记 `IsError`。
- Shell 在 Linux/macOS 使用本机 Docker Linux Engine，取消和正常结束都回收本次容器；旧宿主进程组路径只用于 M2 测试，不得作为运行时回退。
- 审批等待响应工具/运行 context，取消时清理登记；飞书 Reporter 请求使用传入 context，额外限制为 3 秒。

修复这些行为时要以 `ToolResult.IsError` 的语义一致性为准，并添加 Registry、BashTool 和 Engine 级测试。

### P1：状态、成本和追踪尚不可靠

- Session 只存在于进程内存，`-session` 只能在当前进程内复用，不能实现重启后的历史恢复。
- 不预置默认模型或网关的价格；费用只有在显式价目表匹配且供应商返回 Usage 时才显示确定金额，否则显示未知。Session 将不同币种分开汇总。
- Trace 文件按随机 RunID 排他创建，目录/写入错误在运行结果中可见；它不含完整提示词、工具参数和输出，但仍是本地执行元数据，应按后续保留策略治理。

不要向用户宣称会话已经持久化或成本已经准确，除非相关实现和测试已补齐。

### P1：飞书并发与审批

- 同一 chat 已按 Session 票据登记顺序执行，不同 chat 可并发；全局任务队列容量、事件去重和服务关闭治理尚待实现。
- 旧飞书审批支持取消、超时和重复处理不阻塞，但主体鉴权、跨会话隔离和持久化尚未实现，因此不作为新权限层审批来源。当前入口默认只读。
- 飞书 HTTP 服务位于 `cmd/agentops`，与 CLI 是两个独立入口；不要在 `cmd/claw` 中顺手启用它。

### P2：默认行为和仓库卫生

- CLI 默认关闭 PlanMode；显式开启后，模型可能创建或修改工作区根目录下的 `PLAN.md`、`TODO.md`。
- SkillLoader 对文件、遍历深度、数量及总注入大小设有上限，并标记外部来源；仍一次性加载符合限制的正文，没有按触发条件渐进加载。
- 多数旧 Go 文件尚未统一 gofmt；修改文件时只格式化本次涉及的 Go 文件，避免无关大范围变更。
- `.claw/`、`.zcode/`、`workspace/` 当前会显示为未跟踪目录。不要把运行生成的 trace、计划文件、评测工作区或二进制提交到仓库。
- README 中仍可能存在智谱默认 Provider、环境变量和 Session 持久化等旧描述，修改相关功能时同步更新 README 与本文件。

## 修改指南

### 新增工具

1. 在 `internal/tools/` 实现 `BaseTool` 和 `RequiredCapabilities()`；未声明能力默认拒绝。
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
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
go build ./cmd/... ./internal/...
```

若修改 Session、并发执行、飞书并发或 CostTracker，再运行相关 `-race` 测试。若因为需要外部密钥、真实费用或缺少环境而没有执行某项验证，必须在交付说明中明确写出。

容器验收需明确设置 `CLAW_TEST_DOCKER=1 go test -race ./internal/executor -run TestDockerIntegration -count=1 -v`，运行前准备已安装镜像。普通测试不依赖 daemon。
