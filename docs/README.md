# 文档导航

[返回项目首页](../README.md)

文档以交流学习为主：先运行，再理解实现；需要研究具体机制时，再看版本化设计和实验记录。以下相对链接可直接在 GitHub 或本地 Markdown 阅读器中打开。

## 入门与日常使用

| 文档 | 解决的问题 |
| --- | --- |
| [无密钥工程课程](learning/README.md) | 先运行真实引擎回放，再逐课观察预算、权限、上下文、恢复、计划和扩展 |
| [上手指南](getting-started.md) | 准备环境、首次运行、首次写入、排查启动问题 |
| [模型与环境配置](configuration.md) | `env.export`、`.env` 优先级、渠道、端点、代理、构建默认值 |
| [CLI 与会话](cli.md) | 单次任务、REPL、多行输入、参数和斜杠命令 |
| [工具与执行权限](tools-and-permissions.md) | readonly / workspace / trusted 边界和工具用途 |
| [飞书接入](feishu.md) | 独立 Webhook 服务、群聊配置、去重和审批 |

## 能力与源码学习

| 文档 | 学习重点 |
| --- | --- |
| [架构与源码导读](architecture.md) | ReAct 流程、模块职责、接口与阅读顺序 |
| [上下文与记忆](context-and-memory.md) | Session、消息配对、压缩、来源覆盖、工作笔记、技能 |
| [计划与委派](planning-and-delegation.md) | 依赖计划、只读子任务、范围交接和共享预算 |
| [任务、恢复与验收](tasks-and-recovery.md) | 状态、退出码、检查点、动作效果和恢复约束 |
| [预算与观测](observability.md) | Token、费用、Trace、观测台和请求诊断 |
| [证据分析与报告](analysis-and-reports.md) | 普通答复、严格引用分析、报告文件与核验的区别 |
| [CLI 自动配置](cli-auto.md) | `-auto on` 的选择规则、显式覆盖和恢复边界 |
| [开发与测试](development.md) | 扩展工具 / Provider、离线回归、真实模型评测 |
| [机制保证边界](mechanism-boundaries.md) | 程序保证什么、模型仍负责什么、失败出口与验证状态 |
| [工具扩展契约](extending-tools.md) | 从注册到权限、效果、账本、恢复及子任务范围的完整接入点 |

建议路线：无密钥课程 → 架构 → 真实只读任务 → 工具与上下文 → 任务恢复 → 计划与委派 → 证据模式。也可以从一个工具调用开始，在观测台里跟踪一次完整任务。

实施范围和验证记录见[学习质量调整计划](learning-quality-plan.md)。工具能力元数据与严格局部补丁仍是后续设计，不能当成当前保证。

## 机制与实验记录

按问题先读[实验导航](experiments.md)，再进入下表的机制版本与历史记录。

机制说明描述实现契约，记录描述某次验证的结果和限制。两者结合阅读，避免将历史实验结果当作当前所有任务的保证。读取实验、交付前处理与质量模式均需显式启用；CLI 自动配置另见上表。

| 主题 | 机制说明 | 结果与限制 |
| --- | --- | --- |
| 只读执行与委派 | [只读效率 v2](readonly-efficiency-v2.md) | 首对 Token / 工具门槛及停止扩样原因也在该文档中 |
| 证据分析 | [分析模式](analysis-mode.md) | [早期质量评估](quality-first-evaluation.md) |
| 写入与报告 | [写入效率 v1](write-efficiency-v1.md) | [2026-11-06 结果](write-efficiency-results-2026-11-06.md) |
| 阅读范围与覆盖 | [覆盖效率 v2](coverage-efficiency-v2.md) | [2026-11-06 结果](coverage-efficiency-results-2026-11-06.md) |
| 工作笔记 | [笔记效率 v3](work-notes-efficiency-v3.md) | [2026-11-07 结果](work-notes-efficiency-results-2026-11-07.md) |
| 笔记采纳与交付 | [笔记交付 v1](notes-delivery-v1.md) | [2026-11-07 结果](notes-delivery-results-2026-11-07.md) |
| 历史来源复用策略 3 | [来源复用 v1](source-reuse-v1.md) | [2026-11-07 结果](source-reuse-results-2026-11-07.md) |
| 独立读取实验策略 4 | [读取实验 v2](read-features-v2.md) | [2026-11-07 结果](read-features-results-2026-11-07.md)：候选交付通过，语义质量未达标，实验默认关闭 |
| 普通交付前处理 | [交付前处理 v1](delivery-review-v1.md) | [2026-11-08 记录](delivery-review-results-2026-11-08.md)：包括真实实验按协议停止情况 |
| 显式质量核验 | [质量优先 v1](quality-first-v1.md) | [2026-11-07 结果](quality-first-results-2026-11-07.md) |
| 请求诊断 | [诊断 v1](request-diagnostics-v1.md) | [2026-11-07 结果](request-diagnostics-results-2026-11-07.md) |
| 本地观测台 | [观测台说明](local-monitor.md) | [实施记录](local-monitor-results-2026-11-05.md) · [界面记录](local-monitor-ui-results-2026-11-06.md) |
| 真实任务修复 | [修复 Spec](real-task-repair-spec.md) | [2026-11-05 对照](real-task-repair-results-2026-11-05.md) |

## 历史测试与问题复盘

- [功能测试用例](functional-test-cases.md)、[功能测试记录](functional-test-results-2026-11-04.md)、[自动化记录](functional-test-results-2026-11-04-automated.md)、[真实模型与飞书联调](functional-test-results-2026-11-04-live.md)。
- [任务分析链路修复](task-analysis-repair.md)：子报告交接、证据复用、预算收尾与部分分析。
- [文件发现链路修复](discovery-repair.md)：发现、搜索与读取范围相关问题。
- 评测运行方法和各实验脚本入口见[开发与测试](development.md)。

实验数据用于讨论工程取舍。引用有效、读取完整、局部关系成立、模型复核通过、业务结论正确和运行验收是不同证据，交流时请说明所指层次。
