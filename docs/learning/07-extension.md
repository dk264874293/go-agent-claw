# 07：注册工具与执行契约

[课程目录](README.md) · [扩展清单](../extending-tools.md)

## 问题与最小基线

实现并注册 `BaseTool` 后，工具已进入注册表，但模型可见性、权限、效果分类与持久记录仍需完整设计。本课新增只计算输入的 `count_words`，比较 readonly 和 trusted。

```bash
go run ./cmd/learn -scenario extension
```

## 机制与可观察证据

| 仅注册后的模式 | 是否可见 | Handler 执行次数 | 回执 |
| --- | --- | --- | --- |
| readonly | 否 | 0 | `REJECTED/NONE` |
| trusted | 是 | 1 | `SUCCEEDED/UNKNOWN`，输出3 |

输入是“模型 候选 程序”，按空白计算3个字段。当前权限和副作用分类部分按工具名决定；未知名称没有自动成为只读工具，trusted 成功返回也没有自动成为已确认的文件动作。课程还检查未分类计算没有被伪装成持久变更账本回执。

源码：[可运行工具与课程](../../internal/learning/extension.go)、[名称权限分类](../../internal/tools/scope.go)、[Registry 效果分类](../../internal/tools/registry.go)、[引擎变更分类](../../internal/engine/execution.go)。课程参数校验覆盖缺失、null、未知字段、错误类型及多段 JSON；输入错误在计算前拒绝。

## 成本与保证边界

本例不访问文件、数据库或网络，trusted 仅用于观察现有默认分类。不能将这个例子直接改成写远端的工具然后认定账本和恢复已覆盖。Schema 描述本身不执行参数校验；自定义工具必须在自己的代码中拒绝非法参数。完整接入点见[工具扩展契约](../extending-tools.md)。

## 自查题

1. Schema 声明 required 后，工具为什么仍需校验参数？
2. `count_words` 为什么没有在 readonly 中自动开放？
3. 如果将它改成发邮件，除了注册还必须解决什么？
