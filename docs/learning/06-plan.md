# 06：依赖、完成事实与非法计划

[课程目录](README.md) · [下一课：扩展](07-extension.md)

## 问题与最小基线

仅按列表顺序执行，会先核对尚未创建的文件。本课将 check 放在 create 前面，但声明 check 依赖 create，观察执行器计算就绪步骤；随后恢复带已完成 create 的状态，最后提交非法依赖。

```bash
go run ./cmd/learn -scenario plan
```

## 机制与可观察证据

| 对照 | 模型 / 分发 | 关键事实 | 任务终态 |
| --- | --- | --- | --- |
| 反序列表按依赖执行 | 5 / 2 | 实际先写后读，保存的两步骤均 Done | `NEEDS_VERIFICATION` |
| 恢复保留已完成步骤 | 2 / 1 | 只读现有产物，不再运行 create，两步骤均 Done | `NEEDS_VERIFICATION` |
| 非法依赖 missing | 1 / 0 | 只生成计划，校验失败，未执行工具 | `FAILED` |

脚本检查每次系统提示中的实际步骤目标，读取组还核对真实文件观察。课程结束前从 TaskStore 读取快照，确认稳定步骤身份和完成状态；无法仅靠打印一份计划获得通过。

源码：[课程](../../internal/learning/plan.go)、[计划提议、校验和就绪计算](../../internal/engine/plan.go)、[执行步骤和置 Done](../../internal/engine/loop.go)。已有回归：[loop_test.go](../../internal/engine/loop_test.go) 的 `TestPlanRunsReadyStepsAndWritesTaskViews`、`TestPlanPatchPreservesCompletedSteps`。

## 成本与保证边界

新任务多一次计划模型调用，步骤执行继续消耗原预算。当前计划重排冻结已完成步骤，但未实现完整的失败子图补丁约束，详见[后续设计](../design/local-plan-patches.md)。对话正常返回会将步骤置 Done；整个修改任务的业务验收仍由有效 Verification 决定。计划不是准确性的证明。

## 自查题

1. 为什么列表首项不是第一个执行的步骤？
2. 已完成步骤为什么需要稳定 ID？
3. 当前重排保留 DONE，与只允许修改失败子图之间有什么差距？
