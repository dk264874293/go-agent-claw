# 工具扩展的完整契约

[开发指南](development.md) · [可运行示例](learning/07-extension.md) · [后续能力元数据设计](design/tool-capabilities.md)

新增 `BaseTool` 并注册后，Registry 能识别该工具；能否被模型看见、能否执行、如何归类副作用及恢复，仍由以下接入点共同决定。当前名称规则适合现有小型工具集，扩展时必须核对。

## 接入清单

| 检查项 | 当前入口 | 新工具需要回答的问题 |
| --- | --- | --- |
| 名称与定义 | [BaseTool / Register](../internal/tools/registry.go) | 名称是否唯一？Schema与实现是否一致？同名注册会替换原项，应避免无意覆盖 |
| 模型可见性 | [VisibleTools](../internal/tools/registry.go)、[引擎工具前沿](../internal/engine/read_reuse.go) | 当前任务、阶段和模式是否应暴露？注册不意味着所有模式可见 |
| 身份与执行范围 | [ExecutionScope](../internal/tools/scope.go)、[工作区根句柄](../internal/tools/files.go) | 是否遵守 WorkDir、AllowedPaths、模式和文件边界？自定义工具不会自动获得路径保护 |
| 参数与运行前校验 | [Registry](../internal/tools/registry.go)、工具自己的 Execute | Schema仅描述，不是通用运行时验证器；必填、类型、未知字段和边界由工具校验。包内可用 preflight；跨包示例在 Execute 中校验 |
| 附加准入与审批 | [Middleware / 执行钩子](../internal/tools/registry.go)、[飞书审批](../internal/feishu/approval.go) | 需要哪些限制？拒绝是否发生在真实副作用前？审批是否绑定当前动作和参数？ |
| 只读分类 | [IsReadOnly](../internal/tools/scope.go) | 新名称默认不属于只读；是否需要新增明确分类？仅在描述里写“只读”无效 |
| 变更分类 | [execute 的 mutation 判定](../internal/engine/execution.go) | 当前只识别 write_report、write_file、edit_file、bash；新增写工具不自动进入 Pending 和变更恢复 |
| 回执与副作用 | [Registry 的结果分类](../internal/tools/registry.go)、[ToolResult](../internal/schema/message.go) | 拒绝、失败、取消、成功分别对应 NONE、CONFIRMED还是UNKNOWN？未知名称正常返回默认UNKNOWN |
| 持久动作和效果 | [execution.go](../internal/engine/execution.go)、[delivery.go](../internal/engine/delivery.go) | 执行前记什么、请求身份是什么、结果是否可靠落盘？成功打印文本不能替代动作账 |
| 恢复 | [reconcile](../internal/engine/persistence.go) | 用什么核对效果？无法核实时怎样阻塞？远端系统是否支持幂等和结果查询？ |
| 预算与并发 | [runState.reserve](../internal/engine/run_state.go)、[工具批次调度](../internal/engine/loop.go) | 拒绝是否仍扣尝试？是否可以与其他只读操作并发？写入是否仍是顺序屏障？ |
| 子任务装配 | [子任务工具](../internal/tools/subagent.go)、[RunSub](../internal/engine/subagent.go) | 是否真正只读、是否受父路径范围约束？不能通过子任务绕过权限 |
| 观测与资料 | [observe 白名单](../internal/observe/types.go)、[来源捕获](../internal/engine/analysis_evidence.go) | 是否只记录允许的元数据？普通字符串输出不能自动提升为可信来源 |

## 最小可运行例子

[count_words](../internal/learning/extension.go) 实现 Name、Definition、Execute，按空白统计输入字段，并在实际计算前拒绝缺失、null、错误类型、未知字段和多段 JSON。注册方式是：

```go
registry.Register(tool) // 只完成注册；权限和效果仍由正式分发流程决定。
```

运行 `go run ./cmd/learn -scenario extension`：readonly 看不到也不能执行该新名称，返回 REJECTED/NONE；trusted 执行一次，输出3，但未分类的效果仍为 UNKNOWN，不自动增加持久变更回执。这个例子故意保留现行分类以展示接入缺口，不修改主引擎语义。

给真实读工具补分类时，还需验证路径范围、只读并发、子任务准入以及结果是否有真实读取回执。给远端写工具补接线时，需要先设计持久动作与外部结果核对；不能复制本例后直接以 trusted 放行来代替这些契约。

## 验收选择

优先测试具体不变量：无权限时 Handler 未运行、非法参数无副作用、相同动作恢复不重放、未知结果不盲目重试、取消与存储失败不能转成功。根据改变的真实范围运行相关包测试；共享预算、账本、存储和审批变更额外执行内部 race。能力元数据改造的触发与候选比较见[后续设计](design/tool-capabilities.md)。
