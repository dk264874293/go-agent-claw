# 从一次循环理解 Agent 工程

[项目首页](../../README.md) · [文档导航](../README.md) · [机制保证边界](../mechanism-boundaries.md)

先用脚本候选运行真实引擎，再观察程序为什么放行、拒绝、继续或停止。需要 Go 1.25 或更新版本。以下命令从仓库根目录运行，不读取 `.env`、不需要模型密钥，也不会请求远端模型；首次下载 Go 依赖可能访问模块代理。

```bash
go run ./cmd/learn -scenario react
go run ./cmd/learn -list
```

每次课程都创建并删除独立的临时工作区，文件工具、权限、预算、消息投影和 TaskStore 使用正式实现。Provider 使用固定脚本，检查实际观察后才提出下一步；意外轨迹或不满足预期会令课程失败。中断与历史策略对照通过临时的版本1状态夹具构造，不增加普通 CLI 的保护关闭开关。

## 学习顺序

| 课程 | 命令中的 scenario | 先回答的问题 |
| --- | --- | --- |
| [01 最小 ReAct](01-react.md) | `react` | 模型候选怎样变成下一轮可见的工具观察？ |
| [02 预算与停止](02-budget.md) | `budget` | 模型持续想调用工具时，谁让它停下来？ |
| [03 权限与副作用](03-permission.md) | `permission` | 工具已注册、模型已选择，为什么仍不能执行？ |
| [04 上下文与证据](04-context.md) | `context` | 工具消息合法，为什么仍可能遗漏已读证据？ |
| [05 中断恢复](05-recovery.md) | `recovery` | 文件已写出、结果未记录，恢复时依据什么决定？ |
| [06 计划与依赖](06-plan.md) | `plan` | 列表顺序、依赖顺序和已完成事实有什么区别？ |
| [07 工具扩展](07-extension.md) | `extension` | 新增接口实现后，还缺哪些执行契约？ |

首遍只追踪 [cmd/learn](../../cmd/learn/main.go) → [课程装配](../../internal/learning/learning.go) → [converse / generateRequest](../../internal/engine/loop.go) → [工具分发](../../internal/tools/registry.go) → 下一次 `Generate`。再按课程进入上下文、恢复或计划细节。报告、质量、读取实验和历史兼容分支可以在主线理解后按需阅读。

## 如何读输出

- `[模型请求]`：实际到达脚本 Provider 的请求序号、消息数和可见工具数；模型预算在调用前由引擎准入。
- `[候选]`：脚本模拟模型提出的意图。它不证明 Handler 已执行。
- `[分发回执]`：真实 Registry 返回的状态和副作用分类。`REJECTED/NONE` 表示未产生效果；`SUCCEEDED/UNKNOWN` 不能被解释为已确认副作用。
- `[任务结果]`：真实 `RunResult` 的状态、StopCode、预算用量及课程观察。`registry_dispatches` 是 Registry 分发次数，不是 Handler 次数，也不是计费工具尝试数；恢复复用回执时可能出现工具尝试1、分发0。
- `[课程通过]`：课程预期已通过断言。预算课预期为 `BUDGET_EXHAUSTED`，非法计划预期为 `FAILED`；课程通过不把这些任务改成成功。

`usage.elapsed` 是 Go `time.Duration` 的纳秒值，随机器和运行变化；身份、耗时和并发读取输出次序不属于固定答案。Registry 自身可能在 stderr 输出挂载日志。脚本只验证可检查的消息、动作和结果，不展示模型私密思维链。

## 从回放走向真实任务

读懂回放后，按[配置指南](../configuration.md)准备模型，再运行只读任务：

```bash
go run ./cmd/claw -mode readonly -delegation off \
  -prompt "阅读 README.md，说明模型、工具和引擎的职责"
```

这条命令会请求真实模型并产生费用。进一步读[架构导读](../architecture.md)、[机制保证边界](../mechanism-boundaries.md)和[实验导航](../experiments.md)，分别区分结构、保证与效果。课程目前没有子 Agent；委派机制可从[计划与子智能体](../planning-and-delegation.md)和对应回归进入，待有独立范围及合并问题时再扩展课程。
