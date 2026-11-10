[返回首页](../README.md) · [文档导航](README.md)

# CLI 自动配置选择 v1

本说明适用于支持 `-auto` 的 CLI 构建，可用 `./claw -help` 确认。若当前构建未提供该参数，可分别使用 `-local-checks on` 和 `-delivery-review on`，并遵守各自的组合约束。

`-auto off|on` 默认 `off`，仅用于 CLI 单次任务和 REPL 新任务；未开启时保留原有配置行为。开启后按已有任务模式补充未显式设置的能力，不增加分诊模型调用，也不根据任务关键词推断权限或严格模式。

| 任务条件 | 自动局部核对 | 自动交付前处理 |
| --- | --- | --- |
| 普通 `readonly + answer`、`workspace + change`，且无 `-plan`、`-thinking` | 开启 | 开启 |
| 其他合法普通组合，包括 `trusted`、规划、额外思考 | 开启 | 跳过 |
| `-analysis`、`-report` 或 `-quality` | 跳过 | 跳过 |

`-source-views`、`-claim-focus` 仍默认关闭；四个能力开关的显式 `on/off` 分别覆盖自动选择，显式不兼容组合继续报错。只读委派沿用现有策略。每次新任务显示实际配置、显式覆盖和自动跳过原因。

```bash
# 单次任务
./claw -auto on -mode readonly -prompt "阅读资料并总结"

# 交互模式：每次新任务独立选择
./claw -auto on -dir ./workspace

# 仅关闭局部核对，保留兼容任务的自动交付前处理
./claw -auto on -local-checks off -prompt "汇总资料"

# 仅关闭交付前处理，保留自动局部核对
./claw -auto on -delivery-review off -prompt "核对资料中的计算"
```

自动选择不修改模型、权限、预算、任务类型、规划、额外思考、严格报告模式、读取范围或完整阅读要求、输出风格、读取档位及验收命令。能力开放不保证模型实际使用或质量提升；实际核对和交付前处理继续消耗原共享预算。

命令行 `-resume` 不能同时显式传 `-auto`，包括 `off`。用 `-auto on` 启动的 REPL 内仍可使用 `/resume ID`，恢复只沿用历史配置，不重新选择、不重置预算或处理机会；`/clear` 不清除启动策略，`/verify` 保持原有下一次提交语义。Bench、飞书和观测台不新增自动入口。

实现入口：[CLI 参数与任务提交装配](../cmd/claw/main.go)，具体策略以支持该功能的构建为准。
