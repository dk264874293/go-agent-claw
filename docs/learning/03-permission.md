# 03：候选、准入与副作用

[课程目录](README.md) · [下一课：上下文](04-context.md)

## 问题与最小基线

同一个 `write_file` 候选分别进入 readonly 和 workspace。脚本在 readonly 中故意提出一个不在工具前沿中的候选，观察真实分发是否仍会拒绝，然后继续合法读取。

```bash
go run ./cmd/learn -scenario permission
```

## 机制与可观察证据

| 对照 | 写入工具是否可见 | 写入回执 | 实际文件 | 任务终态 |
| --- | --- | --- | --- | --- |
| readonly | 否 | `REJECTED/NONE` | 不存在 | `ANSWERED` |
| workspace | 是 | `SUCCEEDED/CONFIRMED` | 内容为“受控写入” | `NEEDS_VERIFICATION` |

两组均为3次模型请求和2次工具尝试 / Registry 分发。第二次请求核对写入状态，第三次请求核对合法读取的正文。只读组拒绝后可以继续调查，并不会因为出现非法候选就自动放宽权限。

源码：[工具可见性与模式](../../internal/tools/scope.go)、[Handler 前准入](../../internal/tools/registry.go)、[效果记录](../../internal/engine/execution.go)、[课程](../../internal/learning/basic.go)。已有真实工具回归：[registry_test.go](../../internal/tools/registry_test.go) 中的隐藏工具、路径越界、外部符号链接与过时写入版本场景。

## 成本与保证边界

准入拒绝也占用工具尝试预算。每一层检查都会增加代码与状态成本，但可见 Schema 和提示词本身不能代替执行边界。`CONFIRMED` 证明动作当时的文件效果，修改任务仍需用户指定的有效验收才能成为 `VERIFIED`。

## 自查题

1. 为什么隐藏工具以后，还要在 Registry 执行时再次检查？
2. `OnToolCall` 或 `[候选]` 可以证明 Handler 已运行吗？
3. 文件写入成功，为什么仍是 `NEEDS_VERIFICATION`？
