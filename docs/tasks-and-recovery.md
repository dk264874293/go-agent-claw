[返回首页](../README.md) · [文档导航](README.md)

# 任务状态、恢复与验收

一次任务不只是一段模型答复。引擎还记录工具是否执行、文件是否确认写入、是否运行验收，以及遇到中断时哪些效果仍然未知。

## 终态与退出码

| 状态 | 含义 | 退出码 |
| --- | --- | --- |
| `ANSWERED` | 回答任务已交付，不代表独立语义验收 | 0 |
| `VERIFIED` | 用户验收命令通过，且工作区版本符合记录要求 | 0 |
| `NEEDS_VERIFICATION` | 已交付修改或报告，仍缺验收 | 2 |
| `BLOCKED` | 当前约束或未知效果阻止继续 | 2 |
| `BUDGET_EXHAUSTED` | 次数或累计时间额度耗尽 | 3 |
| `CANCELLED` | 用户取消或上下文取消 | 130 |
| `FAILED` | 执行、模型或关键存储失败 | 1 |

启动参数无效也可能返回 2。判断任务结果时同时阅读状态、原因、交付回执和验收缺口。

## 任务记录

记录位于 `<工作区>/.claw/tasks/<任务 ID>/`，包含事件、检查点和工具材料；计划模式额外生成 `PLAN.md` 与 `TODO.md`。Trace 按运行 ID 写入 `<工作区>/.claw/traces/`。观测台可以展示这些任务的过程与交付摘要。

`Delivery` 回执确认的是动作发生时的效果。文件后来被改过，回执不能代替当前文件检查；旧任务缺少完整账本时会显示历史缺口。

## 恢复中断任务

用终端输出中的实际 ID 替换示例：

```bash
./claw -dir ./workspace -mode workspace -resume 任务ID
# 原任务若使用 trusted，恢复时也明确指定对应模式
./claw -dir ./workspace -mode trusted -resume 任务ID -timeout 15m
```

也可在 REPL 中输入 `/resume 任务ID`。恢复沿用原任务目标、会话、验收命令、读取与实验设置；不会重置已消费的预算。显式调高额度按恢复规则处理，本次权限与原任务权限共同约束执行。

不要同时提供新 `-prompt`、`-verify`、`-plan`、`-task-kind` 或 `-report`。`-auto`、`-delivery-review` 和新的读取实验开关也不能在命令行恢复时显式重设，包括 `off`；其他设置见各能力文档。

文件动作中断后会核对版本，已确认效果保留；通用命令无法确认是否产生效果时，任务可能保持阻塞，不能靠重新运行一次掩盖未知结果。同一工作区还受任务锁保护。

## 用户验收

```bash
./claw -mode trusted -prompt "检查并修正 README 中的空白格式问题" \
  -verify 'git diff --check'
```

验收命令由用户指定，仅在 trusted 模式执行。这个示例只检查 diff 空白问题；检查业务行为应提供相应测试命令。命令成功且工作区版本一致时才得到 `VERIFIED`，其证明范围取决于命令本身。

工具额度耗尽后，根 Agent 若仍有轮次、模型和时间，可进行一次禁工具收尾。部分分析通过 `PartialReport` 单独展示，原 `BUDGET_EXHAUSTED` 和退出码 3 保留；恢复不会在同一额度边界重复消耗收尾机会。

源码入口：[状态和结果](../internal/engine/types.go)、[主循环与恢复](../internal/engine/loop.go)、[任务存储](../internal/taskstore/store.go)。详细交接和部分分析约定见[任务分析链路](task-analysis-repair.md)。
