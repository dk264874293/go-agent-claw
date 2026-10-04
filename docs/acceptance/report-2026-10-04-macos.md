# M1–M3 与阶段 2 本地综合验收报告

日期：2026-10-04。依据：[综合验收方案](comprehensive-acceptance-plan.md)。

## 1. 结论

| 范围 | 结论 | 说明 |
| --- | --- | --- |
| 本地运行 A/B/P/C/F | **47 / 47 通过** | 全量回归、真实 HTTP 协议伪服务、实际 Docker 和真实 CLI/伪终端均有本次证据 |
| 真实模型接入 R | **未执行：5 项** | 本次未授权付费调用；没有调用模型网关、benchmark 或飞书 API |
| 原生 Linux 部署 | **未执行** | 没有提供目标主机；macOS 上的 Linux 容器运行不代表原生 Linux CLI 部署验收 |
| 阶段 3 原型准入 | **允许开始** | 限于当前 macOS 环境内的持久化/崩溃恢复原型；不构成生产部署或真实网关接入结论 |

本次范围内未解决 P0/P1 为零。保留一项 P2 观察：并行运行多组构建/测试时曾出现一次 CLI 进程启动超时，具体平台原因尚未确定；夹具已增加启动余量和失败诊断，任务运行时限仍有单独约束，最终逐组检查通过。

## 2. 批次、代码与环境

| 字段 | 实际值 |
| --- | --- |
| 批次 | `2026-10-04-qQLJIA`，Codex 执行 |
| 原始基线 | `ba71d887c1d1c79cbc3e4cc3a0e2fb6f179d7a88` |
| 实现修复/新增验收 | `f3a6004c0856fe92d17b7d7f5ad190c8efce17c7` |
| 最终代码/夹具提交 | `ac70dbe9513fbba946adbda2d1330bcee097f01f`，分支 `codex/stage2-permissions-isolation` |
| 代码差异证据 | 仓库外 `verified-code.patch`、`code-commit.txt`、`verified-revision.txt` |
| 系统/工具链 | Darwin arm64，Go 1.26.5，CGO=1，Apple clang 21.0.0 |
| UID/GID | 宿主 501/20；实际容器进程非 root，详见 D 的断言与证据索引 |
| Docker | Engine 29.4.3，linux，cgroup v2；`unix:///Users/wangpeiliang/.docker/run/docker.sock` |
| 固定镜像 | `bash:5.2`，`sha256:c6ca50f2256aec4a9ef3b459f939971dec4adddb70d705b956e2d4019a61475d` |
| 容器执行 | 非 root；1 CPU、256 MiB、64 PID；根目录只读、全部 capability 移除、no-new-privileges；仅工作区 bind 和 16 MiB 受限 tmpfs |
| 执行窗口 | 首次检查日志创建于 18:38:12 +08:00；最终四项门槛检查于 19:23:45 +08:00 结束；19:26:55 +08:00 完成证据备份 |
| 工作区 | 所有 CLI/文件操作使用仓库外临时副本；入口执行目录无 `.env` |
| CLI 场景配置 | 伪终端：mock 模型、每 Run 300000 Token、输出 64 Token、单工具 5 秒、Thinking 关闭；PlanMode 按场景开启，F05 为 read-only；普通 CLI 集成夹具显式限制 Run 为 5 秒 |
| 凭证与费用 | 伪服务只使用假密钥；真实模型调用数 0，本次无 API 费用 |

Docker、完整交互和协议补测针对的运行实现与 `f3a6004` 一致。完整交互 CLI 构建时工作树尚未提交，其 VCS 元数据保留原基线及 modified 标记，详见 `compiled-cli-build-info.txt`。`d4b1102` 只修改 CLI 集成测试的进程时限、显式任务时限和失败诊断，没有修改运行实现、容器或交互脚本。`ac70dbe` 进一步补上实际根挂载 ro 和工作区回读断言；最终全量检查直接记录了 `ac70dbe` 的完整 SHA。报告及使用说明是之后的文档变更。

## 3. 证据索引与检查结果

原始执行目录：`/private/tmp/claw-acceptance-20261004.qQLJIA`。

**已备份到仓库外的持久目录**：`/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA`。以下简称 **E**。不依赖临时目录继续存在；未将二进制、Trace 或评测工作区加入 Git。

[证据索引](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/evidence-index.json) 包含每个交互 Run 的 ID、起止时间、用量和 Trace 路径。[哈希清单](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/SHA256SUMS.json) 覆盖 264 个原始证据文件。普通检查的命令、提交 SHA、起止时间和退出码见相应 `accepted-*.json`；逐函数结果/耗时在 U 日志，逐 Run 起止时间在交互 Trace。

| 索引 | 命令/入口 | 退出码 | 结果与文件 |
| --- | --- | --- | --- |
| U | `CLAW_TEST_DOCKER=0 go test -count=1 -v ./cmd/... ./internal/...` | 0 | [accepted-unit.log](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/accepted-unit.log) |
| RACE | `CLAW_TEST_DOCKER=0 go test -race -count=1 ./cmd/... ./internal/...` | 0 | [accepted-race.log](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/accepted-race.log) |
| V | `go vet ./cmd/... ./internal/...` | 0 | `E/accepted-vet.log` |
| BUILD | `go build ./cmd/... ./internal/...` | 0 | `E/accepted-build.log` |
| P | `go test -race -count=1 -v ./internal/provider` | 0 | `E/final-provider.log`；也已纳入最终 U/RACE |
| D | `CLAW_TEST_DOCKER=1 go test -race -count=1 -v -timeout=180s ./internal/executor -run '^TestDocker(Integration\|Acceptance)$'` | 0 | [accepted-docker.log](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/accepted-docker.log)，两个容器测试实际执行，无 SKIP |
| T | `python3 scripts/test-local-approval.py` | 0 | `E/terminal.log`：yes、审批超时、恢复与退出 |
| I | `python3 scripts/test-comprehensive-acceptance.py --evidence-dir /tmp/claw-acceptance-20261004.qQLJIA/interactive` | 0 | [interactive.log](/Users/wangpeiliang/Desktop/AI-project/go-agent-claw-acceptance/2026-10-04-qQLJIA/interactive.log)，9 个独立交互场景 |

U/RACE 中两个可选 Docker 测试的 SKIP 不用于证明 C 类；C 类结论来自 D。`cmd/bench` 和无测试文件的正式包只有构建/静态检查证据，没有借此宣称真实评测已运行。

## 4. 逐项结果

证据列中的 U/P/D/I/T 对应上表文件；函数名和子场景可以在日志中定位。一行“通过”表示方案该行所有本地断言已有证据，不表示真实模型会自行遵循脚本行为。

### A：核心回归（16 项）

| ID | 状态 | 实际断言/主要证据 |
| --- | --- | --- |
| A-01 | 通过 | U：`TestRunCompletesWithoutTools`，一轮 completed、RunID 和结束时间有效 |
| A-02 | 通过 | U：`TestRunParallelFailureReachesNextModelAndReminder`，原 stderr、Recovery、同轮成功结果及顺序均保留，Reminder 实际触发 |
| A-03 | 通过 | U：`TestRunMaxTurnsStopsFurtherModelRequests`，Thinking 开/关均在轮数边界停止 |
| A-04 | 通过 | U：`TestRunTimeLimitAndCallerCancellation`、`TestRunTimeoutPreservesToolCallPair`、`TestRunProviderErrorsAndInvalidOptions`；P 验证供应商空响应失败 |
| A-05 | 通过 | U：全部 `TestGetWorkingMemory_*`、`TestSessionCopiesNestedMessageData`，普通 user 前缀、完整配对、深拷贝 |
| A-06 | 通过 | U/RACE：Session FIFO、取消排队不追加/不调用模型、不同 Session 并发；飞书提交顺序测试 |
| A-07 | 通过 | U/RACE：`TestBatchConcurrencyAndWriteBarrier`、`TestBatchTimeoutAndUnstartedCancellation` |
| A-08 | 通过 | U：有界输出、读取、合法 UTF-8 与下一轮超时元数据；实际容器输出/取消另见 D |
| A-09 | 通过 | U：`TestParsePrecedenceAndPlanDefault`、非法配置与权限环境优先级测试 |
| A-10 | 通过 | U：`TestLedgerExactBoundaryAndOneTokenShort`，正好预留通过、差 1 Token 拒绝 |
| A-11 | 通过 | U/RACE：并发预留不双花、Thinking/Action 共用预算、子任务耗尽后停止；I/F03 5 次主子请求共计 35 Token |
| A-12 | 通过 | U：Ledger/Tracker 的未知用量、错误和负数断言；P：缺字段、空/null/部分/负数/小数 Usage 均不伪报已知零，完整零计数仍已知 |
| A-13 | 通过 | U：价格精确匹配、Tracker 版本/币种、缺价与币种错误启动拒绝；`TestAcceptancePerRequestRounding` 向上舍入；`TestAcceptanceMulticurrencyAndClear` 分别展示 CNY/USD |
| A-14 | 通过 | U：唯一 RunID 排他导出、导出故障可见、原始参数/输出不落 Trace；I 的独立 Run 和假密钥排除断言 |
| A-15 | 通过 | U：`TestCLIIntegration`，真实进程的工具重放、费用未知、预算零调用拒绝、权限与无审批拒绝 |
| A-16 | 通过 | U：单任务退出码与 REPL 失败后继续；I/F01：全部命令、多行、EOF、clear 后保留账单；T/I：退出与审批取消 |

### B：权限、文件与审批（12 项）

| ID | 状态 | 实际断言/主要证据 |
| --- | --- | --- |
| B-01 | 通过 | U：未声明、未知工具、权限不足在中间件前拒绝；`TestAcceptanceApprovalAuditAndAuthority` 证明再次 Restrict 不能扩权 |
| B-02 | 通过 | U：`TestExternalInstructionCannotAuthorizeChildWrite`，宽 Registry 下仍拒绝写入、Shell、递归；Policy/网络拒绝测试证明能力不能扩大 |
| B-03 | 通过 | U：`TestRootBoundariesAndAtomicWrite`，绝对/..、反斜杠/盘符、合成 .env/.env.* 和 .git 路径拒绝 |
| B-04 | 通过 | U/RACE：父/末级外部链接及链接替换竞争，不泄露/修改外部标记 |
| B-05 | 通过 | U：FIFO/目录拒绝、硬链接原子替换不修改外部原值、失败/成功无临时文件残留 |
| B-06 | 通过 | U：`TestAcceptanceEditLevelsAndAtomicFailure` 四级编辑、精确/模糊歧义、空/不存在片段与扩展超过 1 MiB，失败原文件不变；原 0750 权限保留 |
| B-07 | 通过 | U：`TestReadOnlyExploration`、`TestAcceptanceExplorationLimits`，200 条目、100 匹配、1000 扫描、16 层、128 KiB 文件及总输出/UTF-8/取消 |
| B-08 | 通过 | U：`TestApprovalBinding`，逐字段变更、期限延长、重复请求重放均拒绝 |
| B-09 | 通过 | U：无控制终端/Approver、过期、参数变化、取消；I/F02 无副作用，Registry 元数据完整 |
| B-10 | 通过 | T：yes/超时/恢复；U：审批排队取消；I/F02：no 拒绝；I/B10：实际 Ctrl+C 在等待时退出且不写文件 |
| B-11 | 通过 | U：审批 ID、完整参数哈希、主体、期限与无原始内容审计；技能每文件/总量/数量/深度/扫描上限，指南与技能 UTF-8 截断；I/F03/F05 外部来源标记 |
| B-12 | 通过 | U：`TestServiceAssemblyRejectsUntrustedApproval`，默认 read-only/deny、local 配置拒绝、旧回调不能给新 Registry 授权；飞书预算、FIFO 与取消测试 |

### P：Provider 协议（5 项）

| ID | 状态 | 实际断言/主要证据 |
| --- | --- | --- |
| P-01 | 通过 | U/P：OpenAI/Anthropic 请求模型与明确输出字段，文本和 Usage 正确 |
| P-02 | 通过 | U/P：`TestAcceptanceProviderReplay`，同时文本+两个调用，ID/名称/参数及第二轮结果关联；Anthropic 参数按 JSON 语义比对 |
| P-03 | 通过 | U/P：`TestAcceptanceProviderCancellation`，两类真实 HTTP 请求均已进入服务端，context 取消后两秒内返回 context.Canceled，没有后续请求 |
| P-04 | 通过 | U/P：HTTP 400、损坏 JSON、空 choices/content、缺/不完整/非法 Usage、空对象及空/重复工具标识；错误时合法 Usage 保留 |
| P-05 | 通过 | U/P：五种支持值的默认 URL 映射、缺密钥/空模型错误、假密钥头、显式 URL 与 max_tokens/max_completion_tokens 字段；未知值拒绝 |

### C：真实容器（9 项）

| ID | 状态 | 实际断言/主要证据 |
| --- | --- | --- |
| C-01 | 通过 | D/权限与挂载：非 root、CapEff=0、NoNewPrivs=1、实际根挂载 ro；daemon inspect 核对唯一工作区 bind、16 MiB nosuid/nodev/noexec tmpfs |
| C-02 | 通过 | D：宿主合成文件与假环境秘密不可见，工作区写入并在容器中回读成功，Docker socket 不存在且不在挂载中 |
| C-03 | 通过 | D/受控网络对照：同 daemon 的 TCP 回显服务在 bridge 返回固定标记、none 连接失败；服务无宿主发布端口 |
| C-04 | 通过 | D：实际 cgroup CPU=100000/100000、memory=268435456、pids=64；OOM 标为 ResourceExhausted；实际 fork 返回 Resource temporarily unavailable 后回收 |
| C-05 | 通过 | D：成功/exit 7 stderr 保真，100000 字节输出截断且排空，没有堵塞 |
| C-06 | 通过 | D/正常结束回收后台进程：后台循环实际写过文件，返回后 350 毫秒内容不再变化，容器被删除 |
| C-07 | 通过 | D：超时及主动取消元数据正确，取消至回收返回约 39.143 毫秒，PID 超时场景总计约 4.24 秒，后续任务成功；均达到取消后 5 秒目标 |
| C-08 | 通过 | U：不可用/远程/缺镜像、create/start/inspect/rm 和双重失败注入，清理错误可见，无 pull/宿主回退；I/F04 实际缺镜像错误与外部哨兵不变 |
| C-09 | 通过 | U：网络开启仍需审批，模型未知 network/image/mounts/memory 参数不能更改固定配置；同后端第二次执行不重解析镜像；D 核对每次实际 image ID |

### F：完整交互（5 项）

| ID | 状态 | 实际断言/主要证据 |
| --- | --- | --- |
| F-01 | 通过 | I/F01 两场景：help/status/cost/clear/exit、两任务、原样多行、EOF 取消；请求仅 2 次、每 Run 7 Token，clear 后累计 5/2 保留、最终累计 10/4 |
| F-02 | 通过 | I/F01 默认不开计划且不写 PLAN/TODO；I/F02 三场景：审批新建/已有计划局部更新，原计划和已完成内容保留；write 拒绝后 Bash 仍须独立审批，拒绝后无写入 |
| F-03 | 通过 | I/F03：委派→子任务三个只读工具→摘要→主任务审批 edit→独立 go test；请求次序/结果次序/35 Token 共享预算/父子跨度；源码改为乘法，测试与 go.mod 原样保留 |
| F-04 | 通过 | I/F04：实际本地 daemon 缺镜像→工具错误→只读恢复→下一 REPL 任务；外部哨兵不变且未创建宿主回退标记，两个独立 Run |
| F-05 | 通过 | I/F05：大型假指南与 40 个假技能同时存在，系统提示 <105000 字节并带来源；强制发出的 write/bash/spawn 均权限拒绝，无文件副作用 |

### R 与部署

| 项目 | 状态 | 原因 |
| --- | --- | --- |
| R-01～R-05 | 全部未执行（5 项） | 缺少明确付费授权、批次预算与已核对的接入配置 |
| 原生 Linux | 未执行 | 目标主机未提供；没有将容器、编译或历史记录算作主机验收 |

## 5. 关键产物与 Run 证据

| 场景 | RunID / 独立断言 | 证据 |
| --- | --- | --- |
| F03 主子任务与修复 | `4b45f2d28fe5368e040b42d1b721b6ab`，主子 5 请求/35 Token，`Tool.Execute(spawn_subagent) → Agent.Subagent → LLM.Action` | `E/interactive/F03-child-edit/.claw/traces/trace_4b45f2d28fe5368e040b42d1b721b6ab.json` |
| Multiply 验收 | 初始三个输入均暴露加法错误；修复后 `(2,3)=6`、`(-2,3)=-6`、`(0,3)=0` 通过 | `E/interactive/F03-child-edit/before-test.log`、`after-test.log`、`artifact-hashes.json`；测试文件前后 SHA256 一致，go.mod 原文一致 |
| 计划续接 | `df107b64984c932ea9edb2e26780508e`，4 请求/28 Token；PLAN 原文不变，仅 TODO 一个 checkbox 更新 | `E/interactive/F02-plan-resume/` 下请求、Trace、文件、终端日志 |
| 缺镜像与恢复 | `1a8d9ee9a18d97e8f433fbc1c8a79101` 后 `419f2c8c3268f086cd329023dba35ed5`；外部哨兵仍为固定原值 | `E/interactive/F04-missing-image/`、`E/interactive/outside-sentinel` |
| Ctrl+C 审批取消 | `ae9a2ada1e24fa1e15e0636e6be01002`，caller_cancelled，未创建 cancelled 文件 | `E/interactive/B10-ctrl-c/` |
| 容器回收 | 本批容器名/服务 ID 在 D；每个执行容器均 inspect 不存在，服务 defer 回收，最终 managed 容器数 0 | `E/accepted-docker.log`、空的 `containers-after.txt` |
| 原始数据排除 | Trace 不含假密钥、源码路径、子报告/原始工具参数；审计仅含 ID/摘要哈希/主体/期限 | U：`TestTraceOmitsToolArgumentsAndOutput`、`TestAcceptanceApprovalAuditAndAuthority`；I 中的 Trace 断言 |

CLI 伪服务计费数据均为合成 Usage，没有价目时显示未知费用；35 Token 是用于验证共享账本的测试值，不是实际模型成本。

## 6. 缺陷、首次失败与复验

| ID | 等级 | 发现与修复 | 复验 |
| --- | --- | --- | --- |
| D01 | P1 | 两类 SDK 的空/部分 Usage 被整数零值补齐；OpenAI 空 choices 丢失合法 Usage。改为检查原始计数字段的完整性/合法性，错误返回保留已知 Usage | 首次 `provider-before-fix.log`；U/P 全部通过 |
| D02 | P1 | OpenAI/Anthropic 空 assistant 响应可能被当成完成。统一非空内容/工具标识校验，非法响应返回错误 | 同上；U/P 的空内容、空对象、工具标识子用例通过 |
| D03 | P2 | 指南/技能按字节截断损坏中文 UTF-8。加载前清理不完整编码，再解析与注入 | 首次 `context-before-fix.log`；U 中单文件/总量/指南 UTF-8 断言通过 |
| D04 | P2 | 子任务 Trace 缺少模型调用跨度。增加继承父身份的 Agent.Subagent 与每轮 LLM.Action | 首次 `interactive-second.log` 和其 F03 Trace；最终 I/F03 通过 |
| O01 | P2 观察 | 并行多组检查时 CLI 15 秒进程时限触发且无输出。单独复测连续三次通过；改为进程 45 秒、实际 Run 5 秒，追加请求数诊断；具体平台原因未确定 | 保留 `verified-unit.log`（退出 1）、`cli-timeout-investigation.log`、`cli-fixture-fixed.log`；最终 U/RACE/V/BUILD 为 0 |

实现修复为 `f3a6004`；O01 的夹具调整为 `d4b1102`。最初四项基线检查全部通过；后续补测暴露问题并修复，而非用基线通过代替验收。

夹具修正也保留失败记录：

- `provider-before-fix.log` 同时包含一次 Anthropic ToolResult 内容数组断言写成字符串的错误；已改为按协议实际文本块比对。
- `boundaries-before-fix.log` 包含 Session 测试误用不存在的 GetHistory 方法；已使用 MessageCount。
- `docker-first.log` 的网络对照失败源于固定镜像没有 httpd applet；诊断后改为镜像自带的 TCP 回显服务，最终 bridge/none 对照均实际执行。
- `interactive-first.log` 使用了错误的 `-plan` 夹具参数；已改为 `-plan-mode`。
- `provider-additional.log` 的小数 Usage 用例最初要求 Provider 必须直接报错；最终按“有效文本可返回，但 Usage 未知，Tracker 保留预留并停止后续请求”的既有预算约定验收。

没有覆盖或删除首次失败。O01 后最终门槛逐组运行，避免多套 Go 构建/race 竞争影响进程启动；后续若在独立 CI 运行仍复现，再收集启动进程状态定位。

## 7. 范围限制与下一步

- 本地：A16+B12+P5+C9+F5=47，通过 47，失败/阻塞/未执行/不适用均为 0。
- 真实模型：5 项未执行；没有验证网关实际工具行为、供应商 Usage/最终账单或模型是否自行遵循 PlanMode。
- 原生 Linux：未执行；Windows 等平台不在本次支持范围。
- Session 尚未持久化，重启恢复和工具执行的不确定状态属于阶段 3。
- 本次是受信任开发者的单机原型验收，不代表完整多租户沙箱、供应商绝对费用上限或飞书身份鉴权已完成；阻塞文件系统 I/O、Trace 保留策略与服务治理仍按既有开发计划推进。
- 取消回收的 39 毫秒是本机正常 daemon 下的本次观测，不是所有环境的时延保证。

**下一步：制定阶段 3 持久化与故障恢复 spec，然后做 SQLite 事务/崩溃恢复原型。** 真实模型接入需另行明确付费批次与配置；Linux 验收需提供目标主机，这两项结论继续单独维护。
