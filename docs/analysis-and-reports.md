[返回首页](../README.md) · [文档导航](README.md)

# 证据分析与报告入门

普通问答适合自由解释；证据模式进一步要求结论绑定本次读取的原文，帮助区分材料事实、分析推断、建议和待核实项。

## 选择入口

| 场景 | 参数 | 交付 |
| --- | --- | --- |
| 普通阅读问答 | `-mode readonly` | 自由答复 |
| 带严格引用的控制台分析 | `-mode readonly -analysis` | 结构化分析结果 |
| 新建证据报告文件 | `-mode workspace -report 报告.md` | 引擎渲染的 Markdown 文件 |
| 在分析或报告上增加核验 | 在对应命令上加 `-quality` | 带核验票据的受限交付流程 |

这些模式须显式选择，不根据任务文字中的“分析”“报告”自动开启。

## 控制台分析

```bash
./claw -mode readonly -analysis \
  -prompt "分析 README.md 和 docs/architecture.md，列出有原文依据的能力、设计取舍和未确认事项"
```

引擎检查引用版本、实际读取范围、行号与摘录。部分引用无效时相关结论降为待核实；全部核心结论缺乏有效依据或格式无效时返回 `BLOCKED/analysis_report_invalid`，不会无条件再请求模型修复格式。

`ANSWERED` 表示分析已经答复，引用校验通过只证明引用关系符合规则，不能保证推断正确。分析不能配置运行验收命令。详细契约见[分析模式](analysis-mode.md)。

## 生成报告文件

```bash
# 在仓库根目录新建报告，重复执行需更换文件名
./claw -mode workspace -report 学习报告.md \
  -read-scope README.md -require-full-read \
  -prompt "基于 README.md 总结项目能力、使用边界和推荐学习路线"
```

目标必须是工作区内尚不存在的 `.md` 文件，父目录须已存在，仅支持 `workspace + change`。报告任务只允许读取和 `write_report`，不能修改原资料。模型提交结构化内容，引擎检查引用后统一渲染、写入和交付，不再请求模型复述整份报告。

成功发布仍是 `NEEDS_VERIFICATION`，因为引用检查不能代替语义或运行验收。报告引用和读写效率设计见[证据报告 v1](write-efficiency-v1.md)。

## 质量核验与普通局部核对

```bash
./claw -mode readonly -analysis -quality \
  -prompt "分析 README.md，列出有证据的重要发现和待核实边界"
```

质量模式登记唯一草稿后关闭探索，执行受限算术 / 明确 JSON 核验，按风险最多一次模型复核，再由作者最多修订一次；全部消耗原共享预算。不能组合计划、额外思考、验收命令或子任务。详见[质量模式](quality-first-v1.md)。

普通任务若只需要对已有来源做局部数值或 JSON 核对，可选择 `-local-checks on`；若需要交付前的一次定向处理，可选 `-delivery-review on`，或使用 [`-auto on`](cli-auto.md) 按兼容规则开放。普通实验开关不能与 analysis / report / quality 混用，它们也不构成独立语义验收。详见[读取实验](read-features-v2.md)与[交付前处理](delivery-review-v1.md)。

源码入口：[分析](../internal/engine/analysis_runtime.go)、[报告](../internal/engine/report.go)、[质量核验](../internal/engine/quality.go)。
