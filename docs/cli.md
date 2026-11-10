[返回首页](../README.md) · [文档导航](README.md)

# CLI 与交互会话

以下示例假设已在项目根目录完成配置，并执行 `go build -o claw ./cmd/claw`。

## 单次任务和 REPL

```bash
# 单次任务，结束后退出
./claw -mode readonly -prompt "阅读 README.md，说明项目用途"
# 连续对话，不带 -prompt
./claw -mode readonly
# 在指定的已有工作区中连续对话，默认允许工作区内写入
mkdir -p workspace
./claw -dir ./workspace
```

REPL 中的连续提交使用同一个内存 Session，后续问题可引用前文；每次提交创建一个任务。`-session` 设置会话 ID，跨进程继续持久任务使用 `-resume`。

| 命令 | 用途 |
| --- | --- |
| `/help` | 查看内置命令和多行输入方式 |
| `/exit` | 退出会话 |
| `/cost` | 查看当前 Session 的 Token 与费用 |
| `/status` | 查看会话 ID、模型、模式、历史消息数与用量 |
| `/clear` | 清空对话历史，保留已发生的累计费用 |
| `/verify <命令>` | trusted 模式设置下一次提交的验收命令 |
| `/resume <任务 ID>` | 按持久配置恢复任务 |

输入独占一行的三个双引号开始多行输入，再输入一行相同符号提交：

```text
"""
请阅读 README.md：
1. 总结项目用途
2. 给出推荐的学习顺序
"""
```

## 常用参数

| 参数 | 默认值 / 说明 |
| --- | --- |
| `-dir` | `.`，任务工作区，须已存在 |
| `-mode` | `workspace`；也可选 `readonly`、`trusted` |
| `-provider` / `-model` | 渠道默认 `auto`，模型默认 DeepSeek；见[配置](configuration.md) |
| `-task-kind` | 按模式推断：readonly 为 `answer`，workspace / trusted 为 `change` |
| `-plan` | 默认关闭，启用依赖步骤计划 |
| `-thinking` | 默认关闭，每轮额外执行一次无工具推理，消耗共享预算 |
| `-delegation` | 仅只读回答，默认 `auto`，可选 `off` / `on` |
| `-verify` | 仅 trusted，指定用户验收命令 |
| `-resume` | 恢复任务 ID，沿用原任务目标和配置 |
| `-read-page-bytes` | `8000`，可选 `16000`，不增加调用次数额度 |
| `-context-bytes` | `300000`，请求材料字节硬上限，并非 Token 上限 |
| `-output-style` | `concise`，可选 `detailed` |
| `-read-scope` | 可重复的工作区相对文件 / 目录；只描述资料要求 |
| `-read-name-glob` | 默认 `*`，配合读取范围过滤基本文件名 |
| `-require-full-read` | 显式要求声明范围完整读取后再交付 |
| `-max-turns` / `-max-model-calls` / `-max-tool-calls` | 默认 `30` / `60` / `120` |
| `-timeout` | 默认 `10m`，主任务与子任务共享累计时间 |

```bash
# 要求完整阅读 docs 中发现的 Markdown 文件，并详细回答
./claw -mode readonly -read-scope docs -read-name-glob '*.md' \
  -require-full-read -output-style detailed -prompt "总结各项能力的适用场景"
```

读取范围不会扩大权限。普通片段阅读可以回答局部问题，只有显式 `-require-full-read` 才增加完整阅读交付要求。完整读取也不能证明结论正确。

## 按需开启的能力

| 入口 | 说明 |
| --- | --- |
| `-analysis` | [只读证据分析](analysis-and-reports.md)，须 `-mode readonly` |
| `-report 报告.md` | [新建证据报告](analysis-and-reports.md)，须 workspace + change |
| `-quality` | [严格质量核验](quality-first-v1.md)，只与 analysis / report 组合 |
| `-auto on` | [自动配置](cli-auto.md)，需对应构建支持，按兼容规则补充未显式设置的能力 |
| `-source-views on` / `-local-checks on` / `-claim-focus on` | [三个独立读取实验](read-features-v2.md) |
| `-delivery-review on` | [普通任务交付前处理](delivery-review-v1.md) |
| `-request-diagnostics on_error` | [本地请求诊断](request-diagnostics-v1.md) |

这些能力有组合约束，恢复时不能用新开关重配原任务；具体限制以各能力说明为准。查看全部参数用 `./claw -help`。

源码入口：[CLI 装配与 REPL](../cmd/claw/main.go)、[任务类型与退出码](../internal/engine/types.go)。
