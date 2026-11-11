# 机制保证边界

[文档导航](README.md) · [课程](learning/README.md) · [实验导航](experiments.md)

机制说明回答程序控制什么；确定性测试证明给定候选与故障下的行为；真实模型实验才涉及采纳、质量和消耗。以下是2026-11-11对当前实现的代码核对，不将历史实验视为当前所有任务的保证。

## 上下文与记忆

| 项目 | 当前契约 |
| --- | --- |
| 问题 | 长历史超过窗口或请求上限，已读取的关键来源可能丢失 |
| 确定性保证 | 工具调用和观察成组处理；当前普通策略2通过来源投影保留未处理正文，具备成功交付、有效笔记和处理声明等条件后才允许折叠；历史策略沿用原义 |
| 模型仍负责 | 决定哪些材料与结论相关、写有效笔记、识别反例及未解决条件；处理声明本身不证明理解或无遗漏 |
| 失败出口 | 必要材料超硬字节上限时 `BLOCKED`；当前 StopCode 为通用 `blocked`，需结合原因辨识上下文不足 |
| 源码与回归 | [Session](../internal/context/session.go)、[Compactor](../internal/context/compactor.go)、[读取记忆](../internal/engine/read_memory.go)、[来源投影](../internal/engine/analysis_context.go)、[read_memory_test.go](../internal/engine/read_memory_test.go) |
| 真实验证状态 | [读取实验](read-features-results-2026-11-07.md)的候选质量未通过；不能从启用笔记或完整读取推断质量提升 |

`Compactor.mask` 对旧观察做折叠或首尾截取，并非模型生成的语义摘要。有来源工作笔记另有资格、版本与投影规则。`-context-bytes` 限制内部请求材料的序列化字节，不是渠道 Token 计数或实际传输字节的精确替身；后者还受 SDK 编码影响。学习对照见[课程04](learning/04-context.md)。

## 计划与步骤完成

| 项目 | 当前契约 |
| --- | --- |
| 问题 | 列表顺序不能表达依赖，中断或重排可能重复已完成步骤 |
| 确定性保证 | 校验步骤 ID、重复、依赖和环；执行就绪步骤；重排由程序保留 Done 步骤，模型不能覆盖其 ID |
| 模型仍负责 | 拆解目标、给出合理依赖、判断每个步骤应交付什么及如何处理阻塞 |
| 失败出口 | 无效计划失败；受阻步骤至多使用现有一次重排机会，未知 Pending 不进入盲目重排 |
| 源码与回归 | [plan.go](../internal/engine/plan.go)、[runTask](../internal/engine/loop.go)、[loop_test.go](../internal/engine/loop_test.go)中的计划与补丁测试 |
| 真实验证状态 | 结构与恢复有离线回归；本轮未做真实规划质量比较 |

当前 `planStep.Done` 由步骤对话正常返回推进；这不等于独立验证了该步骤的业务语义。整个修改任务只有有效的用户验收证据才成为 `VERIFIED`。当前重排会替换未完成列表，尚未完整限制为“失败步骤及其下游”，也未逐项检查原有外部依赖保留。后续设计见[局部计划补丁](design/local-plan-patches.md)，本轮仅如实说明边界。

## 恢复与动作效果

| 项目 | 当前契约 |
| --- | --- |
| 问题 | 动作已发生但结果尚未落盘，恢复后可能重复产生效果 |
| 确定性保证 | 变更执行前登记 Pending，恢复时核对文件前后版本；确认预期效果后补记并复用回执；无法核实的命令保持阻塞；恢复保留已消费预算 |
| 模型仍负责 | 阅读拒绝和观察，提出符合当前任务的下一动作；不能自行宣布未知效果已消除 |
| 失败出口 | 关键存储失败为 `FAILED`；未知效果或文件版本无法核实为 `BLOCKED` |
| 源码与回归 | [execution.go](../internal/engine/execution.go)、[persistence.go](../internal/engine/persistence.go)、[TaskStore](../internal/taskstore/store.go)、[loop_test.go](../internal/engine/loop_test.go)恢复场景 |
| 真实验证状态 | 本轮课程通过临时历史夹具验证恢复行为；未做远端故障或真正进程强杀实验 |

[RecoveryManager](../internal/context/recovery.go)匹配已知错误并补充自然语言建议；它不拥有动作真值，也不证明可安全重试。TaskStore 和效果核对决定已发生什么、还能否继续。动作账、结果回执和检查点都不能单独为任意远端工具提供业务幂等；外部副作用还需要稳定业务身份和可查询结果。

## 协作与子任务

| 项目 | 当前契约 |
| --- | --- |
| 问题 | 大范围调查需要隔离上下文并交接来源，同时防止父子重复消耗或权限扩大 |
| 确定性保证 | 子任务只读，路径不超父范围，不再递归委派；局部与父子共享额度同时生效；报告核对来源版本及实际读取范围 |
| 模型仍负责 | 判断是否值得拆分、如何分配不重叠问题、理解交接发现与分歧；根 Agent 仍需组织最终判断 |
| 失败出口 | 子任务预算、取消和结构错误保留失败或部分交接，不能伪装完整结果；持久状态损坏失败关闭 |
| 源码与回归 | [subagent.go](../internal/engine/subagent.go)、[delegation.go](../internal/engine/delegation.go)、[handoff.go](../internal/engine/handoff.go)、[delegation_budget_test.go](../internal/engine/delegation_budget_test.go)与[handoff_test.go](../internal/engine/handoff_test.go) |
| 真实验证状态 | 已有[只读效率记录](readonly-efficiency-v2.md)明确门槛与停止条件；不能认为多 Agent 默认改善质量或成本 |

## 质量模式与普通交付前处理

| 项目 | 当前契约 |
| --- | --- |
| 问题 | 引用、算术或条件错误可能穿过普通交付；重要材料也可能仍未处置 |
| 确定性保证 | 显式质量模式限制草稿、核验与复核阶段，身份及结构无效时阻断；普通交付前处理按已有待办提供一次受预算约束的定向机会，保留未解决项 |
| 模型仍负责 | 找出重要命题与遗漏、正确绑定证据、解释条件、有效修订；模型复核不能自动成为语义真值 |
| 失败出口 | 质量草稿或发布参数不合法时阻断；普通处理保留未解决项及原终态，不改写业务 JSON 或升级为 `VERIFIED` |
| 源码与回归 | [quality_runtime.go](../internal/engine/quality_runtime.go)、[quality_failure_test.go](../internal/engine/quality_failure_test.go)、[delivery_review_runtime.go](../internal/engine/delivery_review_runtime.go)、[delivery_review_runtime_test.go](../internal/engine/delivery_review_runtime_test.go) |
| 真实验证状态 | [质量实验](quality-first-results-2026-11-07.md)候选未发布正式产物；[交付前处理实验](delivery-review-results-2026-11-08.md)因协议错误停止、候选未运行；均未证明质量提升 |

## 设计依据与升级条件

[第14讲](https://chatgpt.com/space/page_0bf5a9499b6081919f47fe8fa523189e#81949795-4d41-4411-9168-484304096f3a)支持将调度、叙事和机械状态分别解释；[第22讲](https://chatgpt.com/space/page_e0927ee650c881918da61a7a1f66384d#1b2cea5c-9c22-42c0-9052-c1d4b22fd40c)与[第23讲](https://chatgpt.com/space/page_60fb8aa86cbc8191b01dca99b71b7a5a#99ebeabe-cfde-4987-97af-15a2228cd664)支持分别核对执行准入、持久事实及未知副作用。本页的对应关系为项目应用判断。

当课程出现新的结构失败、扩展工具需要修改多个分类位置、计划需要严格限制局部重排，或有效真实实验显示现有机制不足时，再进入相应改造。未出现需求的长期记忆、多候选推理及协作课可以留空；不以增加模式数量衡量学习仓库完成度。
