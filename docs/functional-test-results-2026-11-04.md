# go-agent-claw 本地 P0 测试记录

2026 年 11 月 4 日，Asia/Shanghai。按 [功能测试用例](functional-test-cases.md) 逐条执行，**39 条本地 P0 全部通过，产品失败 0 条**。另有 **14 条待联调，其中 5 条为 P0**。联调用例的状态与本地结果分别记录。

| 项目 | 本次执行 |
| --- | --- |
| 环境 | macOS arm64，`go1.26.5 darwin/arm64`。 |
| 代码基线 | `feture/03`，HEAD `926b56a`，包含当前工作区未提交的实现和测试。测试范围指纹为 `0c9fd26cbd737abbdba866bc04a7b5857bcedfac851c0da948b05925ccb9968d`，涵盖 `cmd`、`internal`、Go 模块文件及用例文档，共 52 个文件。 |
| 构建 | `GOPROXY=off GOSUMDB=off go build ./cmd/... ./internal/...`，退出 `0`。 |
| 运行方式 | CLI 使用临时目录和构建出的二进制。Go 用例按表中的函数或子测试逐条选择，使用 `-race -count=1`，每条都匹配到实际测试。测试使用假 Provider、模拟飞书事件或本地文件。 |
| 退出码口径 | 表内 CLI 退出码为二进制实测。其他各条 Go 测试进程均退出 `0`，业务终态和工具退出码另记在实际结果里。 |

复跑 Go 用例时，在仓库根目录执行 `go test -race -count=1 -v ./internal/<包> -run '<测试名或子测试表达式>'`。多个函数分别执行，或使用带锚点的正则。CLI 的夹具准备与 REPL 输入见用例文档。

## CLI 和请求边界

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| CLI-01 | 临时二进制 `-help`，无密钥 | 退出 `0`。核对了模式、任务类型、验收、恢复、计划、Thinking、四项运行额度和上下文字节参数。 | 通过 |
| CLI-02 | 非法模式、非法任务类型、工具额度 `-1`、上下文上限 `0`、只读验收、恢复同时提交目标 | 六组均输出「运行参数无效」并退出 `2`。缺少密钥的环境下仍先返回参数错误。 | 通过 |
| CLI-04 | 以占位密钥输入 `/help`、`/status`、`/cost`、`/clear`、`/unknown`、`/exit` | 退出 `0`，显示 `readonly`、历史 `0` 条、token `0/0`，有清空确认和未知命令提示。新增任务 `0` 个，`status.txt` 保持 `draft`。 | 通过 |
| RUN-05 | `-context-bytes 64 -prompt '测试上下文限制'`，占位密钥 | 退出 `2`，终态 `BLOCKED`，原因是必要上下文和工具定义超过 64 字节。追踪显示 `model_calls=0`、`tool_calls=0`。 | 通过 |

RUN-05 的追踪摘录如下。

```json
{
  "run_id": "46ea7cede457ddc9727d50f74ebbc1c2",
  "status": "BLOCKED",
  "stop_code": "blocked",
  "model_calls": 0,
  "tool_calls": 0
}
```

## 工具权限和执行结果

以下测试位于 `./internal/tools`。

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| TOOL-01 | `TestRegistryRejectsHiddenToolsAndInvalidArgsBeforeMiddleware` | 夹具只暴露已注册的 `read_file`。写请求 `REJECTED/NONE`，文件未生成，中间件调用 `0` 次。 | 通过 |
| TOOL-02 | `TestRegistryRejectsHiddenToolsAndInvalidArgsBeforeMiddleware` | 空命令、错误类型、`null`、多余字段四种输入均在执行前拒绝，类别为「参数错误」，中间件调用 `0` 次。 | 通过 |
| TOOL-03 | `TestRootedToolsBlockTraversalAndExternalSymlinks` | 绝对路径、`../`、外部软链接读请求均被拒绝，越界写入和目录枚举同样被拒绝。外部标记内容未变，外部目标文件未创建。 | 通过 |
| TOOL-04 | `TestRootedToolsBlockTraversalAndExternalSymlinks`，临时夹具 `TestP0ReportProtectsTraceAndTaskFiles` | 对任务日志和追踪文件直接及通过别名执行的 `8` 次读写均为 `REJECTED/NONE`，原文件未变。技能文件和合法内部链接可读。 | 通过 |
| TOOL-05 | `TestWritePreviewAndVersionRejectConcurrentStaleUpdate` | 使用同一版本竞争的两个编辑中，一个 `SUCCEEDED/CONFIRMED`，一个因版本冲突 `REJECTED`。最终 SHA 与成功编辑的预览一致。 | 通过 |
| TOOL-07 | `TestSearchAndListingStayWithinSubtaskScope` | 列目录和搜索结果只来自 `visible`，通过链接访问 `private` 被拒绝。 | 通过 |
| TOOL-08 | `TestBashReportsNonZeroExitAndKeepsOutput` | Bash 结果为 `FAILED/UNKNOWN`，工具退出码 `7`，stdout 和 stderr 均保留。 | 通过 |
| TOOL-09 | `TestBashCancellationStopsDescendants` | 取消后工具在 `1` 秒内返回 `CANCELLED/UNKNOWN`，继续等待 `350` 毫秒，未出现子进程的逃逸标记。 | 通过 |

## 主循环 预算和上下文

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| RUN-01 | `./internal/engine` 的 `TestRunForwardsRecoveryHintToModel` | 第二次假模型输入收到调用 ID `missing` 的配对观察，内容含救援指南。任务最终为 `ANSWERED`。 | 通过 |
| RUN-02 | `./internal/engine` 的 `TestRunStopsAtModelAndToolBudgets/模型额度` | 假模型调用 `3` 次，工具尝试 `3` 次，终态 `BUDGET_EXHAUSTED`。 | 通过 |
| RUN-03 | `./internal/engine` 的 `TestRunStopsAtModelAndToolBudgets/并行工具额度`，临时夹具 `TestP0ReportBudgetKeepsAllObservations` | 假模型调用 `1` 次，`4` 个工具请求中只有 `2` 个进入工具执行。实际观察 `4/4` 配对，其中 `2` 条 `SUCCEEDED`，`2` 条 `REJECTED` 并标明未执行。任务 `BUDGET_EXHAUSTED`，退出码映射 `3`。 | 通过 |
| RUN-04 | `./internal/engine` 的 `TestRunRetainsGoalAfterWorkingMemorySlides`，`./internal/context` 的 `TestCompactWithinDropsWholeToolBatchAndKeepsGoal`、`TestCompactorPreservesUTF8` | 第 `13` 轮原目标仍在系统上下文。普通用户消息开场，工具批次配对完整。压缩按完整批次裁剪，保留内容是合法 UTF-8。 | 通过 |

RUN-05 已在 CLI 和请求边界表记录。

## 修改任务和验收

以下测试位于 `./internal/engine`。任务退出码和验收失败观察另以临时夹具 `TestP0AcceptanceObservableOutputs` 核对。

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| ACC-01 | `TestChangeCompletionRequiresCurrentVerification/模型声称完成` | 模型给出完成文本，任务为 `NEEDS_VERIFICATION`，退出码映射 `2`，没有验收证据。 | 通过 |
| ACC-02 | `TestChangeCompletionRequiresCurrentVerification` 的「实际通过」与「非零退出」 | 成功命令给出 `VERIFIED/0`。失败命令给出 `NEEDS_VERIFICATION/2`，验收输出保留 `FAILED`、退出码 `7` 和夹具输出，下一轮模型收到失败观察。 | 通过 |
| ACC-03 | `TestChangeCompletionRequiresCurrentVerification/验收期间修改代码`、`TestResumeCannotReuseStaleVerification` | 两次均为 `NEEDS_VERIFICATION/2`。原因分别为验收前后工作区版本不同、工作区变化后原验收失效。恢复的新增模型调用为 `0`。 | 通过 |
| ACC-04 | `TestReadOnlyModeRejectsVerificationOnRunAndResume` | 只读直接验收、可信任务降权恢复均为 `BLOCKED`，模型和工具调用各 `0` 次，`proof.txt` 未生成。 | 通过 |

## 计划 持久化和恢复

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| STATE-01 | `./internal/engine` 的 `TestPlanRunsReadyStepsAndWritesTaskViews` | 逆序计划实际按 `create`、`check` 执行，假模型调用 `5` 次。任务视图与快照一致，工作区根目录已有计划文件不变。 | 通过 |
| STATE-02 | `./internal/engine` 的 `TestPlanRejectsInvalidDependenciesBeforeExecution`、`TestPlanPatchPreservesCompletedSteps` | 循环和未知依赖均在工具调用 `0` 次时被拒绝。替换待办成功，改写完成步骤被拒绝，原计划保留。 | 通过 |
| STATE-03 | `./internal/engine` 的 `TestResumeReconcilesConfirmedFileWriteWithoutReplay` | 恢复核实目标文件已生效，模型收到核实回执，文件工具 Handler 重放 `0` 次。 | 通过 |
| STATE-04 | `./internal/engine` 的 `TestResumeDoesNotReplayRecordedShellEffect` | 夹具模拟 Bash 成功后模型失败。恢复再次提出原命令时使用已有结果，`proof.txt` 仍只有一个 `x`。 | 通过 |
| STATE-05 | `./internal/engine` 的 `TestResumeBlocksUnknownShellBeforeCallingModel` | 恢复因命令效果未知返回 `BLOCKED`，新增模型和工具调用均为 `0`。 | 通过 |
| STATE-06 | `./internal/engine` 的 `TestResumePreservesConsumedModelBudget` | 首次累计模型调用 `1` 次，原额度恢复后仍是 `1` 次。显式调高上限后累计 `2` 次，终态 `ANSWERED`。 | 通过 |
| STATE-07 | `./internal/taskstore` 的 `TestRecoverInterruptedAppend`、`TestCorruptStateBlocksRecovery` | 撕裂尾部修复后成功追加序号 `3`，旧检查点序号 `1` 保留。完整坏记录、未知版本、跳号、超前检查点均被拒绝。 | 通过 |
| STATE-08 | `./internal/taskstore` 的 `TestWorkspaceLockAcrossProcesses`、`TestArtifactsAreScopedAndImmutable`、`TestWorkspacePathsRejectSymlinks` | 持锁时第二进程得到占用，释放后可打开。材料按范围读到 `123`，覆盖、跨任务读取及越界 ID 均失败，软链接越界没有写入外部目录。 | 通过 |

## 子智能体

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| SUB-01 | `./internal/engine` 的 `TestSubagentReturnsOnlyReadEvidence` | 子任务只看到只读工具。报告为 `PARTIAL`，保留第 `2` 行证据，第 `100` 行归入未核实项。 | 通过 |
| SUB-02 | 单独复跑 `./internal/engine` 的 `TestSubagentReturnsOnlyReadEvidence` | 主子共用计数，累计 `4` 次假模型调用、`2` 次工具尝试，主任务为 `ANSWERED`。 | 通过 |

## 用量和追踪

以下测试位于 `./internal/observability`。

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| OBS-01 | `TestConcurrentUsageWithUnpricedCalls` | 十次并发累计输入 `1000`、输出 `2000` token，费用约 `¥0.008`。USD 样本的费用未知，缺 Usage 样本的 token 和费用未知。竞态检测通过。 | 通过 |
| OBS-02 | `TestTraceExportsUniqueRedactedRecords` | 同 ID 两次导出得到两份 JSON，保留 `FAILED`，测试提示、参数和输出的敏感标记均未出现。 | 通过 |
| OBS-03 | `TestTraceCannotFollowWorkspaceEscape` | 外链情况下导出报错，外部目录为空。 | 通过 |

## 飞书离线回归

以下测试位于 `./internal/feishu`。通过模拟 bot 和审批验证，真实 Webhook 收发待 FS-07 至 FS-09。

| 编号 | 执行 | 实际结果 | 判定 |
| --- | --- | --- | --- |
| FS-01 | `TestQueueSerializesSharedWorkspaceAndPersistsDedup`，临时夹具 `TestLocalP0QueuedFIFOAndUnauthorizedRejection` | 共享工作区最大并发 `1`。首任务运行时队列只有 `a2`、`a3`，释放后按序启动。陌生人和重复事件未入队，重启模拟后同一事件 ID 未重入。 | 通过 |
| FS-02 | `TestApprovalWaitBindsChatAndActorAndCancels` | 跨群、非审批人、重复批准均无效。合法批准消费一次，取消后返回 `context.Canceled`，旧批准无效。 | 通过 |
| FS-03 | `TestApprovalWaitExpiresAtTaskDeadline`，临时夹具 `TestLocalP0ApprovalMaximumWindow` | 任务余时 `120 ms` 时审批随任务到期，返回 `DeadlineExceeded`，过期批准无效。任务余时 `10` 分钟及无截止时间时，审批窗口均为 `5m0s`。 | 通过 |
| FS-04 | `TestFeishuRejectsApprovalAfterFileChanges` | 审批等待时修改文件，旧批准被拒绝。模型观察含对象版本变化原因，文件保持「其他版本」。 | 通过 |
| FS-05 | `TestFeishuVerificationUsesCommandApproval`，临时夹具 `TestLocalP0VerifierHasNoEffectBeforeApproval` | 审批消息包含实际验收命令。批准前等待 `100 ms`，未出现任务终态或验收标记。批准后标记写入 `verified`，终态 `VERIFIED`。 | 通过 |
| FS-06 | `TestEventLedgerAndWorkspaceCannotOverlap`，临时夹具 `TestLocalP0SeparateLedgerStartsFakeBot` | 直接重叠、双向嵌套、软链接别名的配置被拒绝。并列路径配置通过并能启动模拟 bot，创建台账目录。 | 通过 |

## 失败和补证说明

本轮未发现产品失败。FS-05 的临时补证首次退出 `1`，原因是夹具把审批消息 JSON 中的 `\u003e` 当成字面 `>` 做匹配。改为解析 JSON 后，夹具复测退出 `0`，并直接核对了批准前后的标记文件。

TOOL-04、RUN-03、ACC-01 至 ACC-03、FS-01、FS-03、FS-05 和 FS-06 的补证使用临时 Go overlay。记录的是额外断言的实际结果，仓库内的常规回归继续用表中已有测试函数执行。

## 待真实模型或飞书联调

| 编号 | 优先级 | 所需环境 | 下一步 |
| --- | --- | --- | --- |
| CLI-06 | P1 | CLI 真实模型，`OPENAI_API_KEY` | 验证多行正文里的 `/help`、`exit` 只作为文本提交。 |
| CLI-07 | P0 | CLI 真实模型，`OPENAI_API_KEY` | 读取夹具 `status.txt`，核对 `ANSWERED/0` 与文件不变。 |
| CLI-08 | P1 | CLI 真实模型，`OPENAI_API_KEY` | 核对同一会话的单轮用量、累计用量和 `/clear`。 |
| RUN-07 | P1 | CLI 真实模型，`OPENAI_API_KEY` | 让 Thinking 耗尽单次模型预算，核对 Action 被阻止。 |
| RUN-08 | P1 | CLI 真实模型，`OPENAI_API_KEY` | 在真实任务中验证 Ctrl+C 和任务总时限退出。 |
| ACC-05 | P0 | CLI 真实模型，`OPENAI_API_KEY` | 跑工作区模式和可信模式的相同修改，分别核对待验收和已验收。 |
| ACC-06 | P1 | CLI 真实模型，`OPENAI_API_KEY` | 验证 REPL 的 `/verify` 只绑定下一次任务。 |
| STATE-09 | P1 | ACC-05 产生的已验收任务与原会话 | 外部改动文件后用 CLI 恢复，核对历史验收失效。 |
| FS-07 | P0 | 飞书测试应用与群聊、智谱 Provider、测试工作区 | 验证真实 Webhook、只读回复及未授权用户行为。 |
| FS-08 | P0 | 可信飞书测试群、授权审批人、智谱 Provider | 分别批准写入和验收，核对文件变化与任务终态。 |
| FS-09 | P0 | 测试群和跨群对照、审批人与非审批人 | 验证拒绝、跨群、非审批人、重复批准和取消。 |
| BENCH-01 | P1 | 智谱 Provider，`ZHIPU_API_KEY` | 跑版本编辑用例并核对目标内容和额外文件。 |
| BENCH-02 | P1 | 智谱 Provider，`ZHIPU_API_KEY` | 跑生成测试用例并核对 `TestMultiply` 确实执行。 |
| BENCH-03 | P1 | 同一轮真实 Bench 结果 | 核对逐条摘要、总通过数和程序原始退出码。 |

其余本地 P1 未排入本轮，共 8 条，编号为 CLI-03、CLI-05、TOOL-06、RUN-06、RUN-09、RUN-10、SUB-03、SUB-04。
