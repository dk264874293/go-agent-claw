# 05：中断后凭什么继续

[课程目录](README.md) · [下一课：计划](06-plan.md)

## 问题与最小基线

本地已有动作开始记录，却没有动作结果。单凭这个检查点无法知道副作用是否已发生。本课在临时目录构造两种版本1历史状态，用真实恢复代码核对；它模拟的是崩溃后的状态，不执行进程强杀或远端调用。

```bash
go run ./cmd/learn -scenario recovery
```

## 机制与可观察证据

| 对照 | 当前证据 | 模型请求 / Registry 分发 | 结果 |
| --- | --- | --- | --- |
| 文件效果可核实 | 实际文件版本等于 Pending 的预期版本 | 2 / 0 | 补记 `CONFIRMED`，候选再次提出写入时复用回执，`NEEDS_VERIFICATION` |
| 命令效果未知 | Pending 没有可核对的文件前后版本 | 0 / 0 | `BLOCKED`，原因要求人工核实，候选命令从未执行 |

文件组恢复后的工具尝试为1，Registry 分发为0：引擎准入尝试后复用已核实回执，不进入工具。脚本必须读到匹配候选身份的“恢复时已核实文件效果”观察才交付。

源码：[课程](../../internal/learning/state.go)、[TaskStore](../../internal/taskstore/store.go)、[reconcile](../../internal/engine/persistence.go)、[回执复用](../../internal/engine/execution.go)。已有回归：`go test ./internal/engine -run 'TestResumeReconcilesConfirmedFileWriteWithoutReplay|TestResumeBlocksUnknownShellBeforeCallingModel'`。

## 成本与保证边界

恢复需要持久日志、状态身份、当前文件版本及读取核对。普通错误救援提示不能替代这些证据。该实现针对本地文件效果核对；支付、邮件或远端发布还需要外部幂等与可查询回执。允许人类决定下一步，不等于已经确认未知动作没有发生。

## 自查题

1. 为什么“没有成功结果记录”不能推出“可以重试”？
2. 文件内容相同与任意远端动作只执行一次之间，还缺哪些能力？
3. 为什么账本确认与用户验收要分别表示？
