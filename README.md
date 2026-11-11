# go-agent-claw

一个用 Go 编写的 ReAct 智能体学习项目：从 **Think → Act → Observe** 循环出发，逐步实现会话、工具调用、上下文管理、任务恢复、子智能体和验收机制，探索 Claude Code 一类编码助手的实现方式。

适合想动手理解 Agent 工程的同学交流学习。可以用它阅读代码、整理资料、修改文件，也可以沿着源码观察模型如何调用工具、预算如何扣减、任务为何停止。代码注释、日志和用户提示均使用中文。

[快速开始](#快速开始) · [无密钥课程](docs/learning/README.md) · [能力说明](#能力说明) · [学习路线](#学习路线) · [完整文档](docs/README.md)

## 快速开始

需要 **Go 1.25 或更新版本**，推荐 macOS / Linux。可以先运行无需密钥的教学回放；真实任务需要模型 API，默认渠道为 DeepSeek，项目默认模型名为 `deepseek-v4.1-flash-1017`。

### 1. 获取项目

```bash
git clone https://github.com/dk264874293/go-agent-claw.git
cd go-agent-claw
```

### 2. 先观察一次无密钥循环

```bash
go run ./cmd/learn -scenario react
go run ./cmd/learn -list
```

回放使用脚本 Provider 和真实引擎，实际读取临时文件，显示模型候选、工具准入回执、下一轮观察与任务终态；不读取 `.env`、不调用远端模型。首次下载 Go 依赖可能需要联网。继续按[七个课程](docs/learning/README.md)学习预算、权限、上下文、恢复、计划和工具扩展。

### 3. 配置模型并运行真实任务

```bash
# 首次使用：复制模板；已有 .env 时跳过，保留本地配置
cp -n env.export .env
```

编辑 `.env`，填写 `DEEPSEEK_API_KEY` 即可体验默认配置，其余配置按需取消注释。[env.export](env.export) 同时支持 `export` 语法和 `.env` 加载；本地密钥填写在已被 Git 忽略的 `.env` 中。

使用其他模型或兼容网关时，参见[模型、端点与代理配置](docs/configuration.md)。CLI 从**启动目录**自动读取 `.env`；`-dir` 指定的是任务工作区。

在项目根目录执行，只读体验无需准备额外文件，此命令会调用真实模型并产生费用：

```bash
go run ./cmd/claw -mode readonly -prompt "阅读 README.md，用中文概括这个项目的用途和主要能力"
```

不带 `-prompt` 即进入交互对话：

```bash
go run ./cmd/claw -mode readonly
```

输入 `/help` 查看命令，`/exit` 退出，`/cost` 查看用量。默认不传 `-mode` 时为 `workspace`，允许修改工作区文件；初次体验建议明确使用 `readonly`。

### 4. 构建后使用

```bash
go build -o claw ./cmd/claw
./claw -help
./claw -mode readonly -prompt "阅读 cmd/claw/main.go，说明程序如何启动"
```

更多操作见[CLI 与交互会话](docs/cli.md)，启动失败时查看[常见问题](docs/getting-started.md#常见问题)。

## 常用场景

以下命令在项目根目录执行，已完成上述构建和配置：

```bash
# 阅读代码、问答：只允许工作区内读取
./claw -mode readonly -prompt "说明 internal/provider 中模型渠道的选择逻辑"

# 修改文件：先在演示目录体验
mkdir -p workspace
./claw -dir ./workspace -prompt "创建 hello.md，写一段中文的 Go 学习笔记"

# 带原文引用的分析
./claw -mode readonly -analysis -prompt "分析 README.md 和 docs/architecture.md，区分事实、推断和建议"

# 生成证据报告：新文件，父目录须已存在
./claw -report 学习报告.md -read-scope README.md -require-full-read \
  -prompt "根据 README.md 生成项目能力学习报告"

# 可信本机模式：允许 Bash，并运行用户指定的验收命令
./claw -mode trusted -plan -prompt "修复 README 中的错别字，保持文意" \
  -verify 'git diff --check'
```

`workspace` 任务完成写入后，缺少运行验收通常返回 `NEEDS_VERIFICATION`；这表示仍需核实修改，并非程序启动失败。`trusted` 中的命令以当前用户权限运行。各模式边界见[工具与执行权限](docs/tools-and-permissions.md)。

## 能力说明

按需阅读，每份使用指南都提供说明、示例或源码入口；已有机制设计和实验记录也在文档中保留。

| 能力 | 可以学到什么 | 说明文档 |
| --- | --- | --- |
| 无密钥工程课程 | 用真实引擎回放正常流程与故障，核对状态与副作用 | [课程目录](docs/learning/README.md) |
| ReAct 主循环与多模型 | 模型适配、工具调用、结果回传、引擎分层 | [架构与源码导读](docs/architecture.md) |
| CLI 与会话 | 单次任务、REPL、多行输入、常用参数 | [CLI 使用](docs/cli.md) |
| 模型与环境配置 | `env.export`、`.env`、渠道选择、端点、代理、构建默认值 | [配置指南](docs/configuration.md) |
| 文件工具与权限 | 发现、搜索、分页阅读、版本化写入、Bash 与中间件 | [工具与权限](docs/tools-and-permissions.md) |
| 上下文、工作笔记与技能 | 消息窗口、压缩、来源覆盖、增量笔记、`SKILL.md` | [上下文与记忆](docs/context-and-memory.md) |
| 计划与子智能体 | 依赖步骤、只读委派、局部与共享预算、报告交接 | [计划与委派](docs/planning-and-delegation.md) |
| 持久任务与验收 | 检查点、状态、恢复、效果账本、退出码 | [任务与恢复](docs/tasks-and-recovery.md) |
| 成本与链路观测 | Token、费用、预算、Trace、本地任务页面 | [预算与观测](docs/observability.md) |
| 证据分析与报告 | 区分事实和推断、校验原文引用、生成 Markdown 报告 | [分析与报告入门](docs/analysis-and-reports.md) |
| 显式质量核验 | 证据草稿、受限算术 / JSON 核验、风险复核 | [质量模式](docs/quality-first-v1.md) |
| 自动配置（需对应构建） | `-auto on` 按兼容规则开放局部核对与交付前处理，以 `-help` 为准 | [CLI 自动配置](docs/cli-auto.md) |
| 读取实验与交付前处理 | 来源视图、局部核对、结论提示、一次定向处理 | [读取实验](docs/read-features-v2.md) · [交付前处理](docs/delivery-review-v1.md) |
| 飞书群聊入口 | 独立服务、群聊队列、去重、动作审批 | [飞书接入](docs/feishu.md) |
| 扩展与评测 | 新工具 / Provider、离线回归、真实模型对照 | [开发与测试](docs/development.md) |

`-auto`、读取实验、交付前处理和质量模式均默认关闭。能力开放不保证模型实际采纳或质量提升，引用校验也不等于结论正确。实验条件、历史结果和已知限制见[文档导航中的机制与实验记录](docs/README.md#机制与实验记录)。

## 学习路线

1. **先看见循环**：[无密钥课程](docs/learning/README.md)，执行 `go run ./cmd/learn -scenario react`，再做预算、权限等故障对照。
2. **理解真实 Agent**：[架构](docs/architecture.md)，从教学装配跟到 `internal/engine/loop.go`，再看 Provider 和工具 Registry；之后按[上手指南](docs/getting-started.md)、[配置](docs/configuration.md)运行真实只读任务。
3. **理解工程约束**：[权限](docs/tools-and-permissions.md) → [上下文](docs/context-and-memory.md) → [预算与观测](docs/observability.md)。
4. **尝试复杂任务**：[计划与委派](docs/planning-and-delegation.md) → [恢复与验收](docs/tasks-and-recovery.md) → [分析与报告](docs/analysis-and-reports.md)。
5. **动手扩展与评价**：[工具扩展契约](docs/extending-tools.md) → [开发指南](docs/development.md) → [机制保证边界](docs/mechanism-boundaries.md) → [实验导航](docs/experiments.md)。

## 构建与测试

```bash
go build ./cmd/... ./internal/...
go test ./cmd/... ./internal/...
# 修改共享预算、账本、存储或审批时
go test -race ./internal/...
```

`workspace/` 是演示工作区，可能有空的 Go 草稿文件，构建和测试请使用上述包范围。`go run ./cmd/bench` 会真实调用模型并产生费用；评测方法见[开发与测试](docs/development.md)。

欢迎通过 Issue 交流问题、使用体验和设计取舍，也欢迎提交小范围、易复现的改进。分享运行记录时请去掉密钥、个人路径和私有资料；提出效果或降耗结论时附上任务条件、模型、预算与实际结果，方便大家复现讨论。

个人学习项目，采用 MIT 许可。
