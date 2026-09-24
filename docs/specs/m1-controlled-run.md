# M1 开发 spec：可观测、可终止的单次运行

状态：已实现并通过 Mock 验收（2026-09-24）。对应 [开发计划](../development-plan.md) 的 S1-01 至 S1-04。

## 目标与交付边界

一次 `AgentEngine.Run` 应产生唯一 RunID、明确的运行结果和终止原因。Mock Provider 持续请求工具时，运行能在配置的轮数或总超时处停止。工具失败须以 `IsError=true` 进入下一轮模型上下文，原始输出和恢复建议都可见。

M1 使用内存中的 Run 记录和当前 Session。Token/费用预算、Shell 后代进程回收、有界工具输出、同一 Session 整轮串行和运行持久化分别按开发计划后续任务交付。M1 的时间限制依赖 Provider 与工具响应 `context.Context`；此阶段不宣称能在超时后强制回收不合作的子进程。

## 对外契约

建议在 `internal/run` 定义不依赖 `engine`、`context`、`tools` 的类型，并让 Engine 返回不可变的结果快照。接口形态固定为：

```go
type Options struct {
    MaxTurns int
    Timeout  time.Duration
}

type Result struct {
    RunID       string
    SessionID   string
    Status      Status
    Reason      Reason
    StartedAt   time.Time
    EndedAt     time.Time
    Turns       int
}

func (e *AgentEngine) Run(
    ctx context.Context,
    session *ctxpkg.Session,
    reporter Reporter,
    opts run.Options,
) (run.Result, error)
```

`MaxTurns` 和 `Timeout` 必须为正值；非法选项在模型或工具调用前返回参数错误。CLI 与飞书入口在 M1 显式传入默认值：`MaxTurns=20`、`Timeout=5m`。M3 再开放统一配置。RunID 使用系统随机源生成至少 128 位标识；生成失败时不启动运行。运行时间使用 `time.Time` 记录，序列化时按 UTC 表达。

`Result` 在运行开始后始终返回，错误路径也要填入 RunID、起止时间和终止原因。`error == nil` 只表示循环正常结束，不表示用户任务经过业务验收。除非法选项或 RunID 生成失败外，返回的 `Result.EndedAt` 必须有值。

| Status | Reason | 触发条件 | `error` |
| --- | --- | --- | --- |
| `succeeded` | `completed` | Action 响应没有 ToolCalls | `nil` |
| `budget_exhausted` | `max_turns` | 已完成允许的 Action 轮数，准备发起下一轮时被拦截 | 非 `nil` |
| `budget_exhausted` | `time_limit` | Run 自身的总超时到期 | 非 `nil` |
| `cancelled` | `caller_cancelled` | 调用方取消或其上层截止时间到期 | 非 `nil` |
| `failed` | `provider_error` | Provider 返回错误，且运行上下文仍有效 | 非 `nil` |
| `failed` | `invalid_response` | Provider 返回 `nil` 响应且无错误，或返回无法处理的响应 | 非 `nil` |

`succeeded` 只表示模型停止调用工具，不能当作阶段 5 的验收通过。普通工具失败是 Observation，不直接终止 Run；模型可在下一轮修正。错误类型应支持 `errors.Is` 或等价的稳定判别方式，CLI 和测试不得通过匹配中文错误文案判断终止原因。

单任务 CLI 在 `Status != succeeded` 时打印 RunID、Reason 和错误并返回非零退出码；成功时返回零。REPL 打印相同的失败信息，但保留 Session 并继续接受输入。飞书入口应报告终止原因；M1 不改变其消息队列和审批机制。

## 轮数、超时和上下文语义

一轮从开始本次推理起，到 Action 响应的工具结果按原顺序写入 Session 后结束。`Turns` 统计已开始的推理轮数；无工具的最终回答也计一轮。开启 Thinking 时，Thinking 请求属于同一轮，最多产生两次 Provider 请求；Thinking 失败也消耗已开始的一轮。`MaxTurns=N` 时最多开始 N 轮，不保证模型请求总数不超过 N。

每次开始新一轮前先检查运行上下文，再检查 `MaxTurns`。Thinking 与 Action 请求前、Provider 返回后、工具执行前和工具聚合后都检查上下文。Run 用 `context.WithTimeout` 派生运行上下文，并将它传给 Provider、Registry 和子任务。调用方已取消或其截止时间先到时，记为 `caller_cancelled`；Run 自身期限先到时，记为 `time_limit`。上下文终止优先于同时观察到的 Provider 错误或轮数上限。

当第 N 轮 Action 返回 ToolCalls 时，先执行并记录该轮的工具结果，再在下一轮入口以 `max_turns` 结束。超时或取消发生在 ToolCalls 已写入 Session 之后时，必须为每个 ToolCall 写入对应的 Observation；响应 context 的假工具应快速返回取消错误。后代进程回收及不响应 context 的工具退出时限由 M2 负责。

RunID、计数器和终止状态属于每次 Run；Session 保留历史和工作目录。M1 不把工作目录放回 Engine，不把 Run 当成持久化 Session，也不在并发运行期间修改 Registry 注册表。

## 工具失败与 Observation

沿用 `schema.ToolResult{ToolCallID, Output, IsError}` 和带 `ToolCallID` 的 `RoleUser` Observation。失败语义按下列规则统一：

1. `BashTool` 的非零退出、超时和取消返回非 `nil` 错误，同时保留已取得的 stdout/stderr；`SubagentTool` 的子任务失败返回非 `nil` 错误。工具返回 `(output, err)` 时，Registry 生成 `IsError=true`，`Output` 同时包含原始输出和错误原因，不丢弃任何一方。
2. 未知工具或中间件拒绝仍生成 `IsError=true` 的结果。成功结果为 `IsError=false`。`ToolCallID` 与请求中的 ID 一致。
3. Engine 仅对 `IsError=true` 的结果调用 `RecoveryManager.AnalyzeAndInject`。增强后的文本写入 Session 的 Observation，下一次 Provider 输入必须能看到原始错误与恢复建议；Reporter 接收同一结果的简短预览及真实错误标记。
4. 同一轮多工具调用可并发执行，但 Observation 按 ToolCalls 原顺序追加。一个工具失败不丢弃其他工具结果。`ReminderInjector` 检查本轮全部结果，不能只检查索引 0；提醒应在所有 Observation 之后追加。

工具结果转 Observation 时仍须满足 `Session.GetWorkingMemory` 的 ToolCall/ToolResult 配对约束。M1 的 Registry 改动只修正错误保真，不改变工具权限模型。

## 实现顺序与测试

1. **S1-01 测试夹具**：顺序脚本式 Mock Provider 记录每次输入和调用次数；假工具能返回成功、`(stdout/stderr, error)`、阻塞直到 context 取消。使用 `t.TempDir()`，不访问真实 API 和项目运行目录。
2. **S1-02 错误反馈**：先写 Registry、BashTool、SubagentTool 的失败测试，再修复 Engine Observation；并行结果测试确认顺序和 Reminder 对非首项失败的处理。
3. **S1-03 Run 结果**：加入结果类型、唯一 ID、状态与终止原因；更新 CLI 和飞书调用点，单任务 CLI 的非零退出码用进程级测试或可注入入口验证。
4. **S1-04 硬性退出**：用持续请求工具的 Mock 验证 `MaxTurns`；用等待 `ctx.Done()` 的 Mock 验证总超时和调用方取消。Thinking 开启时验证轮数按 Action 轮计、Provider 请求次数可为两倍。

至少覆盖以下验收用例：

| 用例 | 必须观察到的结果 |
| --- | --- |
| 首轮无工具回答 | `succeeded/completed`，`Turns=1`，RunID 非空 |
| 单个失败工具后模型修正 | `IsError=true`；下一次 Provider 输入包含原始输出、错误原因和恢复建议 |
| 同轮多个工具，第二个失败 | Observation 顺序与 ToolCalls 一致；第二个错误进入上下文和 Reminder |
| Mock 无限请求工具，`MaxTurns=2` | 恰好开始两轮 Action；无第三轮 Provider 请求；`budget_exhausted/max_turns` |
| Thinking 开启，`MaxTurns=2` | 最多两轮 Thinking 与两轮 Action；`Turns` 不按 API 请求数翻倍 |
| Provider 等待 context 到期 | `budget_exhausted/time_limit`，后续模型与工具调用数为零 |
| 调用方主动取消 | `cancelled/caller_cancelled`，后续模型与工具调用数为零 |
| Provider 返回错误或空响应 | 分别为 `failed/provider_error`、`failed/invalid_response` |
| 非法运行选项 | 调用次数为零，返回参数错误 |
| 单任务 CLI 失败 | 退出码非零，输出包含 RunID 和 Reason；REPL 能继续提交下一条输入 |

验收命令：`go test ./internal/...`、`go vet ./cmd/... ./internal/...`、`go build ./cmd/... ./internal/...`；修改并发路径时追加相关 `go test -race`。真实 LLM 调用不属于 M1 验收。

## 完成定义

S1-01 至 S1-04 的代码、针对性测试和接口说明均完成；所有上述验收用例通过；演示一个正常工具任务和一个被 `MaxTurns` 终止的模拟任务，并展示各自的 RunID、Observation、状态和终止原因。将实际工时与未解决风险写回开发计划，再重估 M2、M3。
