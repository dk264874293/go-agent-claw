# 04：消息窗口与未处理证据

[课程目录](README.md) · [下一课：恢复](05-recovery.md)

## 问题与最小基线

两个批次各读取12份短文档，共24份。比较历史策略0的20条消息窗口与当前策略2的有来源请求投影；输入文件、候选批次和预算相同。

```bash
go run ./cmd/learn -scenario context
```

## 机制与可观察证据

历史策略0通过版本1临时任务夹具恢复。新任务中的0会规范化为2，课程不会给普通 CLI 增加降级开关。

| 对照 | 成功读取 | 最后请求可见原文 | 模型 / 分发 | 终态 |
| --- | --- | --- | --- | --- |
| 历史策略0 | 24份 | 12份 | 3 / 24 | `ANSWERED`，脚本明确不能综合全部原文 |
| 当前策略2 | 24份 | 24份 | 3 / 24 | `ANSWERED`，原文仍待语义分析 |
| 必要上下文上限64字节 | 0份 | 未调用 Provider | 0 / 0 | `BLOCKED / blocked`，原因包含上下文超限 |

所有到达脚本的请求都检查工具调用与观察配对。前两组都可能满足协议，但可见原文覆盖不同；第三组证明必要上下文放不下时明确停止，不破坏消息配对强行请求。

源码：[课程及历史夹具](../../internal/learning/state.go)、[Session 窗口](../../internal/context/session.go)、[Compactor](../../internal/context/compactor.go)、[当前读取记忆](../../internal/engine/read_memory.go)、[来源投影](../../internal/engine/analysis_context.go)。既有回归：`go test ./internal/engine -run TestWorkspaceUnprocessedSourcesSurviveTwentyMessageWindow`。

## 成本与保证边界

保留未处理正文增加请求体积。有效笔记与成功交付条件满足后才有折叠资格；本课不提交处理声明，不能用脚本中缓存的正文代替最后请求可见的证据。字节预算不等于模型 Token 上限；机械消息合法、原文保留完整都不保证结论正确。

## 自查题

1. 已读24份，为什么模型本次可能只看到12份？
2. 固定窗口和有来源记忆分别在什么位置生效？
3. 凭什么允许折叠一份原文？模型说“处理过”是否单独足够？
