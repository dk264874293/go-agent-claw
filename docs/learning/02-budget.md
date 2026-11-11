# 02：预算与停止

[课程目录](README.md) · [下一课：权限](03-permission.md)

## 问题与最小基线

脚本持续提出新的读取意图，每次都使用不同文件，避免重复读取策略干扰。比较模型上限1和3，任务输入与候选生成规则相同。

```bash
go run ./cmd/learn -scenario budget
```

## 机制与可观察证据

| 对照 | 模型请求 | 工具尝试 / 分发 | 终态及 StopCode |
| --- | --- | --- | --- |
| 模型上限1 | 1 | 1 / 1 | `BUDGET_EXHAUSTED / model_limit` |
| 模型上限3 | 3 | 3 / 3 | `BUDGET_EXHAUSTED / model_limit` |
| 工具上限1、模型上限2 | 2 | 1 / 1 | `BUDGET_EXHAUSTED / tool_limit` |

第三组在工具额度耗尽后，使用原预算内的一次禁工具收尾。第二次请求必须没有工具定义，产出位于 `PartialReport`；终态和预算耗尽原因保持，不将部分答复作为任务完成。

预算由 [runState.reserve](../../internal/engine/run_state.go) 准入，收尾由 [closing.go](../../internal/engine/closing.go) 原子预扣轮次与模型额度；[课程](../../internal/learning/basic.go)只提出候选。已有回归还覆盖恢复时消费计数保留及收尾不重复：`go test ./internal/engine -run 'TestResumePreservesConsumedModelBudget|TestClosing'`，具体完整用例见 [closing_test.go](../../internal/engine/closing_test.go)。

## 成本与保证边界

增加额度允许更多请求，也增加真实模型场景的潜在成本；本课只观察调用数，不测量 Token 或费用。共享调用次数与时间预算不等于金额硬上限。有限预算保证任务能停止，不能保证停止前得到正确结论。

## 自查题

1. 收尾为什么不能在模型额度之外“免费”增加请求？
2. 工具被拒绝是否仍可能消耗一次工具尝试额度？
3. 恢复时清零用量，会破坏什么约束？
