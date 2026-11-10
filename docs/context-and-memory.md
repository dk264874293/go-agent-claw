[返回首页](../README.md) · [文档导航](README.md)

# 上下文、工作笔记与技能

长任务需要记住已经读过什么、哪些结论有来源，同时控制每次模型请求的体积。本项目将会话历史、请求投影、来源覆盖和工作笔记分别管理。

## Session 与上下文压缩

Session 保存同一交互会话中的消息和累计用量。请求模型时，引擎构造工作记忆并按字节预算压缩；不会把每轮全部历史直接复制给模型。

旧任务沿用最近消息窗口与 `Compactor.CompactWithin`。工具调用和对应结果必须成组保留，避免出现模型协议不接受的孤儿结果；部分渠道还要求首条非 system 消息为普通 user 输入。压缩请求材料不会清除原 Session 的完整历史。

普通新任务默认使用有来源的读取策略 2：未处理的正文不能先被消息窗口丢弃，只有完整成功交付、模型声明已处理且有有效笔记后，才能安全折叠。历史策略不自动升级；显式读取实验使用策略 4。分析、报告、质量模式使用各自受约束的请求投影。

```bash
# 较大的读取档位与精简输出；两者不会增加调用额度
./claw -mode readonly -read-page-bytes 16000 -output-style concise \
  -prompt "阅读 README.md 和 docs/architecture.md，整理模块职责"
```

`-context-bytes` 默认 300000，是请求材料字节硬上限。必要上下文超限时会停止请求，不会通过拆断工具消息配对强行继续。

## 工作笔记和读取覆盖

模型可在 `read_file`、`read_lines`、`write_file`、`edit_file` 顶层可选提交 `_work_notes` 短增量。引擎验证笔记并将元数据从实际执行参数中剥离；缺失或无效笔记软拒收，不因笔记问题取消原工具操作。

有笔记不代表已经覆盖整个文件。读取覆盖由同版本的实际源字节区间计算，发现清单只说明有哪些文件；文件版本变化、缺页和暂缓材料会影响可复用资格。`-read-scope` 只描述材料要求，`-require-full-read` 才显式增加完整阅读交付约束。

```bash
./claw -mode readonly -read-scope docs -read-name-glob '*.md' \
  -require-full-read -prompt "完整阅读声明范围，列出能力说明和仍待核实的事项"
```

该例覆盖范围较大，可能需要较多调用。笔记机制没有额外的摘要模型请求，但实际采纳、复读和 Token 变化取决于执行过程，不能仅凭机制启用就认定降耗成功。

进一步阅读：[工作笔记 v3](work-notes-efficiency-v3.md)、[笔记采纳与交付](notes-delivery-v1.md)、[范围与覆盖](coverage-efficiency-v2.md)、[来源视图与读取实验](read-features-v2.md)。

## 工作区技能

技能目录位于**任务工作区**的 `.claw/skills/<技能目录>/SKILL.md`。启动时加载技能索引，模型匹配到技能后使用读取工具打开正文，避免把所有说明一次性塞进上下文。

例如新建 `.claw/skills/doc-review/SKILL.md`：

```markdown
---
name: doc-review
description: 检查中文文档中的术语一致性和启动示例
---

# 中文文档检查

1. 先阅读目标文件，标出术语或步骤不一致的位置。
2. 给出原文位置、问题和修改建议。
3. 未授权修改时只输出建议。
```

技能说明不会扩大执行权限；使用效果取决于模型是否正确匹配和遵循。与外部 Agent 平台的技能发现规则也不必相同。

源码入口：[Session](../internal/context/session.go)、[Compactor](../internal/context/compactor.go)、[读取记忆](../internal/engine/read_memory.go)、[技能加载](../internal/context/skill.go)、[提示词组合](../internal/context/composer.go)。
