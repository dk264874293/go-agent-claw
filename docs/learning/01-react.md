# 01：观察一次完整 ReAct 循环

[课程目录](README.md) · [下一课：预算](02-budget.md)

## 问题与最小基线

仅调用一次模型，无法让回答依赖执行过程中才取得的文件内容。本课固定一个读取任务，用真实文件工具取得观察后再生成终答。

```bash
go run ./cmd/learn -scenario react
```

## 机制与状态

1. 入口创建临时 `lesson.md`，装配 Session、脚本 Provider、Registry 和 Engine。
2. 第一次模型请求提出 `read_file`，ToolCallID 为 `read-lesson`。
3. 引擎扣减工具预算，Registry 检查准入，文件工具返回来源版本及实际内容。
4. 工具观察以带相同 ToolCallID 的 `RoleUser` 消息返回。第二次请求的脚本必须核对成功状态与正文，才交付答复。
5. Engine 返回真实任务终态。

## 可观察证据

预期：2次模型请求、1次工具尝试、1次 Registry 分发，读取回执 `SUCCEEDED/NONE`，任务 `ANSWERED`。第二次请求有4条消息，包含最初请求、助手候选和对应观察。

源码：[入口](../../cmd/learn/main.go)、[课程脚本](../../internal/learning/basic.go)、[真实循环](../../internal/engine/loop.go)、[Registry](../../internal/tools/registry.go)。回归：`go test ./internal/learning -run 'TestLessonsExerciseActualEngineBoundaries/react'`。

## 成本与保证边界

此处模型请求是本地脚本调用，费用为零；真实 LLM 使用同一结构时仍按实际请求计费。课程证明文件实际读取且观察成功回传，不证明任意模型能够正确理解原文。`ANSWERED` 只表示答复交付。

## 自查题

1. 把工具观察的 ToolCallID 换成另一个值，会破坏什么协议？
2. 模型提出读取之后，权限在哪里再次检查？
3. 第二次脚本不检查观察、直接返回固定文字，会失去哪项验证？
