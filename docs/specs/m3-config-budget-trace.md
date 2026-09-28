# M3 开发 spec：统一配置、共享预算与可信追踪

状态：已实施，待完成目标平台真实网关兼容性验收。对应 [开发计划](../development-plan.md) 的 S1-07 至 S1-09；以 M1、M2 的提交与无网络验收为基线。

## 目标与边界

单次 Run 的模型请求应受可解释的 Token 与费用准入控制；主任务和子任务共用同一账本。CLI 可选择 Provider、模型、地址、Thinking、PlanMode 和运行上限。费用只有在匹配的价目表与 Usage 都存在时才显示确定金额。每个 Run 有唯一 Trace，导出错误可见。

M3 仍使用内存 Session，不实现重启恢复、沙箱、全局多用户配额或供应商侧绝对费用上限。Token 估算与供应商最终 Usage 可能不同；预算保证本地请求准入和后续停止，最终账单可能因在途请求超过设定值。文档与界面必须说明这一限制。

## 实施前代码依据

- `cmd/claw/main.go` 固定 `gpt-5.6-sol`、网关、20 轮、5 分钟与开启的 PlanMode，且 `godotenv.Overload` 会覆盖进程环境变量；`cmd/agentops` 与 benchmark 也各自固定模型和上限。
- `internal/provider/openai.go` 未传模型输出上限；`internal/provider/claude.go` 固定 `MaxTokens=4096`。Provider 只在成功响应中提取 Usage。
- `CostTracker` 仅按模型名查找两条演示价格；找不到价格时把费用作为零累加，并以人民币符号展示美元计算值。CLI 默认模型没有价格。
- `Session.UsageSnapshot()` 已能并发安全地读取累计值，但没有价格版本、币种、未知状态或 Run 维度账单。
- Engine 与 CLI 都导出 Trace；文件名使用 SessionID 和秒级时间戳，写入可能覆盖，目录错误与导出错误被忽略。现有 Span 可能记录原始提示词、工具参数和输出预览。

## 配置契约（S1-07）

建立 `internal/config`：纯解析与校验、Provider 构造、Run Options 转换分开实现。优先级为显式命令行参数 > 进程环境变量 > 可选 `.env` 的缺省值 > 代码默认值。不得打印密钥或把密钥写入配置文件；缺失密钥返回可测试的错误，不在构造流程中 `panic`/`log.Fatal`。

| 配置组 | 首批字段 | 规则 |
| --- | --- | --- |
| 连接 | `provider`、`model`、`base-url` | 保留当前 OpenAI 兼容网关为 CLI 默认连接；支持现有 OpenAI 兼容与 Anthropic 兼容适配器。URL 在 Provider 构造处集中处理，密钥只从对应环境变量读取。 |
| 推理 | `thinking`、`plan-mode`、`max-output-tokens` | PlanMode 默认关闭；单次模型请求都明确设置正数输出上限，建议默认 4096。适配器不支持时明确报错，不静默忽略。 |
| 运行 | `max-turns`、`timeout`、`tool-timeout`、`max-concurrent-tools` | 保留 M1/M2 的 20 轮、5 分钟、30 秒和 4 worker 缺省值；非法值在发请求前拒绝。 |
| 预算 | `max-total-tokens`、`max-cost`、`cost-currency`、`pricing-file` | Token 上限按 Run 生效，建议 CLI 默认 100000；费用上限为可选项，启用时必须有匹配的价目表及币种。金额以十进制字符串解析，不经浮点数。 |

用户选择的 Provider、模型、地址、预算和价格版本在一个 Run 内不可变。REPL 每条输入建立新的 Run 预算，Session 累计用量仍单独展示。`cmd/agentops` 与 benchmark 复用同一配置结构或转换函数；无人值守的付费入口必须配置有限 Token 上限，启用费用上限时缺价应在启动/评测前失败。benchmark 的真实调用仍需用户明确要求才能运行。

配置测试覆盖优先级、无效布尔/数值/时长、空模型、URL、Provider 与密钥映射、PlanMode 默认值、REPL 多轮配置不变和所有入口的选项传递。测试用临时环境与 Mock/本地 HTTP 服务，不读取项目 `.env`。

## 计量与价目表（S1-08）

价目表按 `Provider/网关标识 + Model` 匹配，记录币种、每百万输入/输出 Token 的价格、版本与来源说明。价格由操作者显式提供；不预置猜测值，不把不同币种自动相加或转换。金额内部使用整数微单位并定义统一舍入规则。价格配置加载后作为不可变快照绑定 Run，防止运行中更新改变账单解释。

每次模型调用记录请求序号、RunID、模型、输入/输出 Usage、币种、价格版本、已知金额或未知原因。`Usage=nil`、缺少价目、模型/网关不匹配和调用失败后可能已计费，都不能记为零费用。Session 汇总保留已知金额与未知请求数；不同币种分开汇总。CLI `/cost`、运行结果、benchmark 报表分别显示“已知金额”和“未知/待核对”状态，不再固定展示 `¥0`。

若配置费用上限而价格未知，在发起任何模型请求之前拒绝运行。若请求已发出但 Usage 未知，预算账本保留待核对的预留量，标记不确定状态并拒绝后续有预算请求；不得把预留量当作零释放。Provider 返回非空响应和错误时也先核对可用 Usage，再分类终止原因。

## 共享预算与输出上限（S1-09）

在 `internal/run` 或独立 `internal/budget` 中建立每个 Run 一个并发安全账本，以带类型的 context 值传给 Provider 计量包装器；`RunSub` 继承原 context，因此 Thinking、Action 和所有子任务调用同一账本。账本与 Session 累计统计分离。保留 M1 的主循环 `MaxTurns` 和子任务自身 10 轮限制；二者的 Token/费用余额共享。

一次模型请求的状态转换为：

1. **预检**：检查 context、配置、价目和剩余额度；在 Provider 适配层以实际序列化的消息及工具定义估算输入 Token，加入协议开销，再加请求的 `max-output-tokens`。估算值、方法和版本需可记录与测试；若适配器无法提供估算，则预算运行在发请求前明确失败。估算不是供应商最终账单。
2. **原子预留**：在同一把账本锁下检查并预留输入估算、输出上限及对应费用。额度不足时返回稳定的 `token_budget` 或 `cost_budget`，不调用底层 Provider。
3. **请求与结算**：底层 Provider 仅调用一次；有 Usage 时按实际输入/输出 Token 和固定价目结算、返还未用预留，超额则记录超支并阻止后续请求。无可信 Usage 时进入不确定状态，不能继续发起预算内模型请求。
4. **传播**：Engine 将预算耗尽映射为 `budget_exhausted` 和独立 `Reason`/sentinel；若子任务在工具调用中耗尽，先完成该轮 ToolCall/Observation 配对，再检查共享账本并终止主 Run，不能靠匹配中文错误文本。Thinking 与 Action 之间也检查余额。

输出上限分别传入 OpenAI 兼容和 Anthropic 兼容请求参数；用本地 HTTP 伪服务断言序列化结果，不触发真实 API。估算、预留与实际 Usage 的差异必须在结果中明确：本地上限限制后续请求，无法替代供应商账单上限。对未支持的响应字段或不提供 Usage 的网关，给出明确能力状态，不伪称预算精确。

## Run Trace 与错误可见性（S1-08）

由 Engine 负责一个 Run 的唯一 Trace；CLI 不再为同一运行重复导出 Session 级 Trace。文件名只用已校验的 RunID，使用排他创建防止覆盖；目录创建失败、序列化失败和写入失败均在 `run.Result` 的独立诊断字段及 CLI/飞书输出中可见，不能悄悄忽略。Trace 导出失败不覆盖模型任务原本的 Status/Reason。

Trace 至少包含 RunID、SessionID、Provider、模型、配置/价格版本、结束原因、模型请求数量、已知 Token、费用状态、预算预留与结算摘要。默认不写 API 密钥、完整提示词、工具参数或工具输出；已有 Span 属性也应清理或脱敏。写入测试使用临时目录，覆盖同秒两个 Run 不覆盖、无写权限/目录故障可见、REPL 多轮各有独立文件以及失败 Run 仍能导出。

## 实施切片与预计工作量

| 顺序 | 可独立评审的切片 | 主要文件 | 完成判据 | 参考时间 |
| --- | --- | --- | --- | --- |
| 1 | 配置解析、校验和入口装配 | `internal/config`、`cmd/claw`、`cmd/agentops`、`internal/eval`、`internal/provider` | CLI/环境优先级、PlanMode 默认关闭、无密钥错误可测试；入口都传递同一运行选项 | 1–1.5 日 |
| 2 | 价格快照、未知状态和展示 | `internal/observability`、`internal/context`、CLI/benchmark 输出 | 未配置价格不显示零；币种和版本可追溯；金额无浮点累加误差 | 1–1.5 日 |
| 3 | 共享账本与 Provider 预留/结算 | `internal/run` 或 `internal/budget`、`internal/observability` | 并发请求不会双花；Usage 缺失不释放为零；预算拒绝不触发网络调用 | 1.5–2 日 |
| 4 | Engine、子任务及两类 Provider 输出上限 | `internal/engine`、`internal/tools/subagent.go`、`internal/provider` | Thinking/Action/子任务共享余额；耗尽原因稳定；工具消息配对；请求带输出上限 | 1.5–2 日 |
| 5 | Trace 唯一导出与失败反馈 | `internal/observability`、`internal/engine`、CLI/飞书 | 每个 Run 一个文件，不覆盖，失败可见，不含原始敏感内容 | 0.5–1 日 |
| 6 | 贯穿回归与文档 | `internal/*_test.go`、`cmd/*_test.go`、README、AGENTS.md、开发计划 | 下表验收、race、vet、build 全部通过；真实调用未执行 | 0.5–1 日 |

参考总量为 **6–9 个工作日**，按单名熟悉 Go 的开发者估算；Provider 兼容网关是否接受输出上限参数是主要不确定项。若本地协议伪服务发现适配差异，先固定能力边界并重估，不以静默移除上限作为回退。

## M3 验收门槛

| 场景 | 必须观察到的结果 |
| --- | --- |
| 未给模型价格 | Token 仍可统计；费用为“未知”，`/cost` 和 benchmark 不显示虚假的零费用 |
| 配置费用上限但缺价/币种不符 | Run 启动前失败，模型调用数为零 |
| 预算刚好用尽 / 差 1 Token | 前者按规则结算后不发下一次请求；后者在预留阶段拒绝，不调用 Provider |
| Thinking + Action + 子任务并发争用余额 | 所有调用用同一账本，预留总量不超过余额，race 检查通过 |
| 子任务耗尽预算 | 已产生的 ToolCall 都有 Observation；主 Run 返回预算原因，无额外 Provider 请求 |
| Provider 失败或缺失 Usage | 已知 Usage 被记录；未知费用进入不确定状态，后续预算请求被拒绝 |
| OpenAI/Anthropic 兼容请求 | 本地协议伪服务收到所配置的输出 Token 上限与正确模型/地址 |
| Trace 同秒导出、目录故障 | RunID 文件不覆盖；失败在结果和入口输出可见，原 Status/Reason 保留 |
| REPL 与飞书入口 | 每次提交使用配置的预算；会话 FIFO、M1/M2 取消与消息配对测试继续通过 |

执行 `go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、`go build ./cmd/... ./internal/...`，并对 `internal/engine`、`internal/context`、`internal/observability`、`internal/tools`、CLI 与飞书相关路径运行 `go test -race`。不调用真实 LLM 或飞书 API；付费验证只有用户明确要求后才执行。

## 实施验收记录

M3 已通过上述全量单测、vet、构建及相关包的 race 检查。无网络测试覆盖配置覆盖优先级、无效配置、匹配/重复价目、预算临界点与并发预留、Thinking 和子任务共享余额、飞书入口预算传递、两类 Provider 的输出上限与 Usage、Trace 唯一性及失败反馈、工具原始内容不进入 Trace。未运行真实 LLM、飞书 API 或目标网关兼容性测试；本地预算只控制请求准入，不等同于供应商侧最终账单上限。
