# go-agent-claw 本地自动化测试复测记录

2026 年 11 月 4 日，Asia/Shanghai。按 [功能测试用例](functional-test-cases.md) 复测，**47 条本地自动用例全部通过**，包含 39 条 P0 和 8 条 P1。产品失败 0 条，另有 14 条待联调。

环境为 macOS arm64、Go 1.26.5，分支 `feture/03`，HEAD `926b56a`。测试包括当前工作区实现，测试范围指纹 `0c9fd26cbd737abbdba866bc04a7b5857bcedfac851c0da948b05925ccb9968d` 与 [上一轮 P0 记录](functional-test-results-2026-11-04.md) 一致。

## 本轮覆盖

| 组 | 用例 | 条数 | 结果 |
| --- | --- | --- | --- |
| CLI | CLI-01 至 CLI-05 | 5 | 通过 |
| 工具 | TOOL-01 至 TOOL-09 | 9 | 通过 |
| 主循环、预算、上下文 | RUN-01 至 RUN-06、RUN-09、RUN-10 | 8 | 通过 |
| 验收 | ACC-01 至 ACC-04 | 4 | 通过 |
| 计划与恢复 | STATE-01 至 STATE-08 | 8 | 通过 |
| 子智能体 | SUB-01 至 SUB-04 | 4 | 通过 |
| 观测 | OBS-01 至 OBS-03 | 3 | 通过 |
| 飞书离线 | FS-01 至 FS-06 | 6 | 通过 |

构建与全量 Go 竞态测试均退出 `0`。CLI 用构建后的二进制在临时目录复跑，CLI-01、CLI-04 退出 `0`，CLI-02 六组非法参数均退出 `2`。RUN-05 返回 `BLOCKED/2`，本轮追踪 `model_calls=0`、`tool_calls=0`。

```bash
GOPROXY=off GOSUMDB=off go build ./cmd/... ./internal/...
GOPROXY=off GOSUMDB=off go test -race -count=1 -json ./cmd/... ./internal/...
```

P0 每项的命名测试、输入和期望详见上一轮记录。本轮复跑了对应 Go 测试、CLI 冒烟和关键补证。TOOL-04 的 8 次受保护路径读写被拒绝，2 个原文件不变。RUN-03 的 4 个工具请求全部配对，其中 2 个成功、2 个因预算被拒绝。验收退出码、失败观察回传、飞书队列顺序、5 分钟审批上限、审批前无执行及独立台账目录启动均复验通过。

## 首次执行的 8 条本地 P1

| 用例 | 执行与实际观察 | 判定 |
| --- | --- | --- |
| CLI-03 | 无密钥时提示设置 `OPENAI_API_KEY`，退出 `1`。用占位密钥指向不存在的目录时，返回 `FAILED` 和「无法定位工作区」，退出 `1`。 | 通过 |
| CLI-05 | 空多行输入被忽略，未闭合多行输入在 EOF 取消。两次退出 `0`，新任务记录均为 `0`。 | 通过 |
| TOOL-06 | `TestReadFileLineRangeReflectsUTF8Boundary`。`offset=4, limit=4` 返回完整「乙」、行号 `[2, 2]`、下一段偏移 `7`，文件末尾返回空片段和 `[0, 0]`。 | 通过 |
| RUN-06 | `TestPromptComposerIndexesSkillsWithoutLoadingBody` 与工具读取夹具。系统提示有技能索引和路径，未包含测试正文标记，按路径读取可获得正文。 | 通过 |
| RUN-09 | `TestQARun09LongOutputEvidence`。Bash 产生 `13,000` 字节，观察含摘要与材料 ID。分段读回 `4,000 / 4,000 / 4,000 / 1,000` 字节，拼接等于原文。请求 `4,001` 字节被拒绝，新任务无法读取原任务的材料。 | 通过 |
| RUN-10 | `TestAutoRuntimeRUN10`。500 ms 时限返回 `BUDGET_EXHAUSTED/3`、`deadline`，主动取消返回 `CANCELLED/130`、`cancelled`，一轮耗尽返回 `BUDGET_EXHAUSTED/3`、`turn_limit`。三组均只调用一次假 Provider。 | 通过 |
| SUB-03 | 三条委派峰值并发 `2`，取消传到两条已启动任务，第三条未启动。父任务 `CANCELLED/130`，三条观察均配对。 | 通过 |
| SUB-04 | 子任务两轮后停止，父模型收到具体阻塞原因。即使预先注册 Bash 和递归委派，子任务直接请求这两项仍为 `REJECTED/NONE`，未产生 Bash 标记，递归调用 `0` 次。 | 通过 |

SUB-04 的临时夹具首轮失败。夹具在可信模式沿用了默认修改任务类型，却期待问答任务的 `ANSWERED`。实际返回 `NEEDS_VERIFICATION` 符合默认行为。夹具显式使用问答任务后复测通过。

## 临时夹具复跑

以下命令在仓库根目录运行。临时目录内保留 Go 测试源及 overlay 配置。

```bash
GOPROXY=off GOSUMDB=off go test -race -count=1 -v -overlay /tmp/claw-run09-p6olj3k5/overlay.json ./internal/engine -run '^TestQARun09LongOutputEvidence$'
GOPROXY=off GOSUMDB=off go test -race -count=1 -v -overlay /tmp/claw-auto-runtime-oloyfy2p/overlay.json ./internal/engine -run '^TestAutoRuntimeRUN(03Pairing|10)$'
GOPROXY=off GOSUMDB=off go test -race -count=1 -v -overlay /var/folders/jv/0js0mg814j3f2lxwvqtkjm4c0000gn/T/claw-sub-local-yr1mn_34/overlay.json ./internal/engine -run '^TestLocalSUB0(3|4)'
GOPROXY=off GOSUMDB=off go test -race -count=1 -v -overlay /private/var/folders/jv/0js0mg814j3f2lxwvqtkjm4c0000gn/T/claw-auto-p0-58p4kwqo/overlay.json ./internal/tools ./internal/engine ./internal/feishu -run '^TestAutoP0'
GOPROXY=off GOSUMDB=off go test -race -count=1 -v -overlay /tmp/claw-auto-feishu-vz88x4bf/overlay.json ./internal/feishu -run '^TestAutoFeishuFS(01QueuedFIFO|06CreatesSeparateLedger)$'
```

## 待联调

| 环境 | 用例 | 条数 |
| --- | --- | --- |
| CLI 真实模型及其恢复场景 | CLI-06、CLI-07、CLI-08、RUN-07、RUN-08、ACC-05、ACC-06、STATE-09 | 8 |
| 飞书测试环境 | FS-07、FS-08、FS-09 | 3 |
| 真实模型 Bench | BENCH-01、BENCH-02、BENCH-03 | 3 |

待联调的 P0 为 CLI-07、ACC-05、FS-07、FS-08、FS-09。各项环境和操作见 [待联调清单](functional-test-results-2026-11-04.md#待真实模型或飞书联调)。
