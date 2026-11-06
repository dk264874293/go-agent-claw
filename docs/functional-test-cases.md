# go-agent-claw 功能测试用例

适用于当前 CLI、工具注册表、ReAct 引擎、任务存储、子智能体、观测和飞书 AgentOps。先跑构建与离线用例，再按需要验证真实模型和飞书链路。离线用例使用假 Provider、模拟事件或临时文件，结果不受模型是否愿意调用工具影响。

## 执行准备

macOS 或 Linux，Go 1.25。在仓库根目录执行以下命令。`P0` 是本轮必测，`P1` 是扩展回归。标注「离线」的表格列出仓库内已有的测试函数，可用 `-run` 按名执行。标注「真实模型」或「飞书联调」的用例单独记录结果。

```bash
go version
go build ./cmd/... ./internal/...
go test -count=1 ./cmd/... ./internal/...
go test -race -count=1 ./cmd/... ./internal/...
```

可用以下命令准备 CLI 沙盒夹具。单次任务的退出码从构建出的二进制读取。`go run` 会包装程序退出状态。

```bash
CLAW_QA_REPO="$PWD"
CLAW_QA_DIR="$(mktemp -d /tmp/claw-qa.XXXXXXXX)"
CLAW_QA_BIN="$CLAW_QA_DIR/claw"
go build -o "$CLAW_QA_BIN" ./cmd/claw

new_claw_case() {
  CLAW_QA_CASE="$(mktemp -d "$CLAW_QA_DIR/case.XXXXXXXX")"
  printf 'draft\n' > "$CLAW_QA_CASE/status.txt"
  printf '甲\n乙丙\n丁' > "$CLAW_QA_CASE/lines.txt"
}
new_claw_case
cd "$CLAW_QA_DIR"
```

CLI 用例在没有 `.env` 的临时目录启动，以隔离仓库中的配置。纯本地交互和请求发送前的检查可以用 `OPENAI_API_KEY=qa-placeholder`。真实模型用例要求在环境中设置可用的 `OPENAI_API_KEY`，实际请求会计费。每个需要修改文件的用例先执行 `new_claw_case`。回到仓库执行 `go test` 时使用 `cd "$CLAW_QA_REPO"`。

## CLI 参数和交互

| 编号 | 优先级与方式 | 前置和步骤 | 预期结果 |
| --- | --- | --- | --- |
| CLI-01 | P0 · 本地 | 执行 `"$CLAW_QA_BIN" -help`。 | 退出码 `0`。列出模式、验收、恢复、规划、Thinking、调用上限、超时及上下文限制。 |
| CLI-02 | P0 · 本地 | 分别传入 `-mode invalid`、`-task-kind invalid`、`-max-tool-calls -1`、`-context-bytes 0`、默认模式下的 `-verify true`，以及组合 `-resume invalid -prompt 测试`。 | 每组提示运行参数无效，退出码 `2`。请求在启动 Provider 前被拒绝。 |
| CLI-03 | P1 · 本地 | 临时目录下执行 `OPENAI_API_KEY= "$CLAW_QA_BIN" -dir "$CLAW_QA_CASE" -prompt 测试`。另用占位密钥提交同一任务，将目录换成 `"$CLAW_QA_DIR/not-created"`。 | 缺密钥时提示设置 `OPENAI_API_KEY`。目录不存在时任务为 `FAILED`。两次退出码均为 `1`。 |
| CLI-04 | P0 · 本地 | 用占位密钥进入 REPL，依次输入 `/help`、`/status`、`/cost`、`/clear`、`/unknown`、`/exit`。 | 显示命令列表、`workspace` 模式、零条历史和用量信息。清空后给出确认，未知命令给出提示，退出码 `0`。工作区中没有新增任务记录。 |
| CLI-05 | P1 · 本地 | 用占位密钥进入 REPL。输入两行独立的 `"""`。另一次输入开头的 `"""` 与一行正文，随后结束 stdin。 | 空内容被忽略。未闭合的多行输入在 EOF 时取消。两次均没有任务记录。 |
| CLI-06 | P1 · 真实模型 | 用 `-mode readonly` 进入 REPL。输入 `"""`、`请原样返回接下来两行文字`、`/help`、`exit`、`"""`。看到回复后单独输入 `/exit`。 | 多行内容只提交一次。正文中的 `/help` 和 `exit` 不触发本地命令。单独的 `/exit` 正常退出。 |
| CLI-07 | P0 · 真实模型 | 在新夹具执行 `"$CLAW_QA_BIN" -dir "$CLAW_QA_CASE" -mode readonly -prompt '请读取 status.txt 并返回文件内容'`。 | 回复与 `draft` 一致，任务为 `ANSWERED`，退出码 `0`。`status.txt` 字节不变。 |
| CLI-08 | P1 · 真实模型 | 进入 REPL，提交与 CLI-07 相同的读取请求，再执行 `/cost`、`/status`、`/clear`、`/status`。 | 提交后显示会话和上一轮用量。清空后消息数为零，累计用量与已有任务记录保留。金额口径见 OBS-01。 |

CLI-04 和 CLI-05 可直接复制以下输入。单次 CLI 命令结束后立即运行 `printf '退出码 %s\n' "$?"` 读取退出码。

```bash
printf '/help\n/status\n/cost\n/clear\n/unknown\n/exit\n' |
  OPENAI_API_KEY=qa-placeholder "$CLAW_QA_BIN" -dir "$CLAW_QA_CASE"

printf '"""\n"""\n/exit\n' |
  OPENAI_API_KEY=qa-placeholder "$CLAW_QA_BIN" -dir "$CLAW_QA_CASE"

printf '"""\n未提交内容\n' |
  OPENAI_API_KEY=qa-placeholder "$CLAW_QA_BIN" -dir "$CLAW_QA_CASE"
```

## 工具权限和执行结果

```bash
cd "$CLAW_QA_REPO"
go test -race -count=1 -v ./internal/tools
```

| 编号 | 优先级 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| TOOL-01 | P0 | 注册读写和 Bash。用默认只读范围直接提交写请求。 | 工具列表仅包含已注册的只读工具。写请求为 `REJECTED`，效果 `NONE`，目标文件不存在，中间件未被调用。 | `TestRegistryRejectsHiddenToolsAndInvalidArgsBeforeMiddleware` |
| TOOL-02 | P0 | 用可信范围向 Bash 分别传空命令、错误类型、`null` 和多余字段。 | 均在执行前拒绝，类别为参数错误，中间件和命令均未运行。 | `TestRegistryRejectsHiddenToolsAndInvalidArgsBeforeMiddleware` |
| TOOL-03 | P0 | 准备工作区外的标记文件，分别尝试绝对路径、`../` 和工作区内指向外部的软链接。 | 文件工具拒绝越界。外部标记内容保持原值。 | `TestRootedToolsBlockTraversalAndExternalSymlinks` |
| TOOL-04 | P0 | 使用普通文件工具访问 `.claw` 运行数据及指向它的别名。再访问 `.claw/skills` 中的技能文件。 | 运行状态和追踪材料不会被普通文件工具读取或写入。技能正文仍可按需读取。 | `TestRootedToolsBlockTraversalAndExternalSymlinks` |
| TOOL-05 | P0 | 新建文件并读取版本。对同一文件并发提交两个携带同一 `expected_version` 的编辑。 | 一个 `SUCCEEDED/CONFIRMED`，另一个 `REJECTED` 并提示版本冲突。文件摘要与成功编辑预期的摘要相同。 | `TestWritePreviewAndVersionRejectConcurrentStaleUpdate` |
| TOOL-06 | P1 | 文件内容为 `甲\n乙丙\n丁`。按 `offset=4, limit=4` 读取，另从文件结束位置读取。 | 返回完整的「乙」、行号范围 `[2, 2]`、下一段偏移量 `7`。结束位置返回空片段，行号范围 `[0, 0]`。 | `TestReadFileLineRangeReflectsUTF8Boundary` |
| TOOL-07 | P0 | 在 `visible` 与 `private` 中放相同搜索词，仅授予 `visible` 范围。列目录、搜索，再读 `visible` 中指向 `private` 的链接。 | 结果只来自授权范围，链接访问被拒绝。 | `TestSearchAndListingStayWithinSubtaskScope` |
| TOOL-08 | P0 | 在可信模式执行同时写 stdout、stderr 并以 `7` 退出的夹具命令。 | 工具结果为 `FAILED/UNKNOWN`，保留两路输出和退出码 `7`。 | `TestBashReportsNonZeroExitAndKeepsOutput` |
| TOOL-09 | P0 | 启动创建了子进程的 Bash 夹具。看到启动标记后取消任务上下文。 | 工具结果为 `CANCELLED/UNKNOWN`，同进程组子进程无法在等待后写出逃逸标记。 | `TestBashCancellationStopsDescendants` |

在 `workspace` 模式测试 Bash 拒绝时，优先看注册表返回的结构化结果。真实模型因看不到 Bash 而自行放弃调用，只能证明本轮未执行命令。`trusted` 使用当前用户权限，路径越界用例应通过文件工具触发。

## 主循环 预算和上下文

```bash
go test -count=1 -v ./internal/context ./internal/engine \
  -run '^(TestRunForwardsRecoveryHintToModel|TestRunStopsAtModelAndToolBudgets|TestRunRetainsGoalAfterWorkingMemorySlides|TestCompactWithinDropsWholeToolBatchAndKeepsGoal|TestCompactorPreservesUTF8|TestPromptComposerIndexesSkillsWithoutLoadingBody|TestGetWorkingMemory.*|TestSession_ResetHistoryAndMessageCount)$'
```

| 编号 | 优先级与方式 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| RUN-01 | P0 · 离线 | 假模型请求读取一个不存在的文件，检查下一次模型输入。 | 观察消息携带原调用 ID 和救援提示，模型可据此解释文件缺失。 | `TestRunForwardsRecoveryHintToModel` |
| RUN-02 | P0 · 离线 | 模型上限设为 `3`，假模型持续请求工具。 | 实际模型调用数为 `3`，返回 `BUDGET_EXHAUSTED`，耗尽后无新调用。 | `TestRunStopsAtModelAndToolBudgets` |
| RUN-03 | P0 · 离线 | 工具上限设为 `2`，模型同轮提出 `4` 次并行读取。 | 只有 `2` 次工具尝试进入额度，返回 `BUDGET_EXHAUSTED`。未执行的调用获得配对结果。 | `TestRunStopsAtModelAndToolBudgets` 检查预算。配对可另查 Session 中的观察消息。 |
| RUN-04 | P0 · 离线 | 驱动引擎超过最近 `20` 条消息的窗口，再用大工具结果触发压缩。 | 原始任务目标仍可见。首条非系统消息是普通用户消息。工具调用与结果成组保留或移除，中文片段仍是合法 UTF-8。 | `TestRunRetainsGoalAfterWorkingMemorySlides`、`TestCompactWithinDropsWholeToolBatchAndKeepsGoal`、`TestCompactorPreservesUTF8` |
| RUN-05 | P0 · 本地 | 在空白夹具用占位密钥执行 `"$CLAW_QA_BIN" -dir "$CLAW_QA_CASE" -context-bytes 64 -prompt '测试上下文限制'`。 | 任务为 `BLOCKED`，退出码 `2`。追踪中 `model_calls` 为 `0`。 | — |
| RUN-06 | P1 · 离线 | 夹具中放置一个包含独特正文标记的技能文件，构建系统提示。 | 提示包含技能索引和读取位置，正文标记只在读取技能文件时出现。 | `TestPromptComposerIndexesSkillsWithoutLoadingBody`、TOOL-04 |
| RUN-07 | P1 · 真实模型 | 在新夹具用 `-thinking -max-model-calls 1` 提交任务。第一阶段模型返回后检查任务摘要与追踪。 | Thinking 用掉唯一模型调用，后续 Action 被预算阻止，返回 `BUDGET_EXHAUSTED`，退出码 `3`。 | — |
| RUN-08 | P1 · 真实模型 | 用可信模式和验收命令 `sleep 10` 提交一个小任务。确认验收命令已开始后按 Ctrl+C。另一次用短任务时限触发截止。 | Ctrl+C 后单次任务为 `CANCELLED`，退出码 `130`。任务总时限先到时为 `BUDGET_EXHAUSTED`，退出码 `3`。效果尚不明确的已启动动作进入恢复核实。 | — |
| RUN-09 | P1 · 定向补测 | 用可信临时夹具让 Bash 执行 `awk 'BEGIN { for (i=0; i<13000; i++) printf "x" }'`，返回 13000 字节的输出。取返回的材料 ID，用 `read_evidence` 读取多段，再在另一个任务中使用相同 ID。 | 首次观察包含摘要和材料 ID。原任务可按偏移回读，每段不超过 `4000` 字节，另一个任务无法读取该材料。 | `TestArtifactsAreScopedAndImmutable` 覆盖存储隔离，长输出链路单独补测。 |
| RUN-10 | P1 · 定向补测 | 假 Provider 等待 `ctx.Done()`。分别触发短任务时限、主动取消，再用返回工具调用的假 Provider 耗尽 `1` 轮主循环额度。 | 前两次分别为 `BUDGET_EXHAUSTED/3` 与 `CANCELLED/130`。轮数耗尽后模型调用不再增加。 | — |

默认上限为主任务 `30` 轮、`60` 次模型调用、`120` 次工具尝试和 `10` 分钟。检查运行记录时区分单个 Bash 的超时和任务总时限。单个 Bash 达到约 `30` 秒后，如果效果未知，任务会阻塞以等待核实。

## 修改任务和验收

```bash
go test -count=1 -v ./internal/engine \
  -run '^(TestChangeCompletionRequiresCurrentVerification|TestResumeCannotReuseStaleVerification|TestReadOnlyModeRejectsVerificationOnRunAndResume)$'
```

| 编号 | 优先级与方式 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| ACC-01 | P0 · 离线 | 假模型直接返回完成文本。任务类型为 `change`，没有验收命令。 | `NEEDS_VERIFICATION`，退出码 `2`。 | `TestChangeCompletionRequiresCurrentVerification` |
| ACC-02 | P0 · 离线 | 可信模式使用检查标记文件存在的命令。另一次改用返回 `7` 的命令。 | 命令成功且版本一致时为 `VERIFIED/0`。命令失败时保留失败观察，未修复时为 `NEEDS_VERIFICATION/2`。 | `TestChangeCompletionRequiresCurrentVerification` |
| ACC-03 | P0 · 离线 | 验收命令自身改动源文件后以 `0` 退出。另一个已验证任务在外部修改文件后恢复。 | 运行中的版本变化和恢复时的历史证据失效均无法得到 `VERIFIED`，版本变化原因可查。 | `TestChangeCompletionRequiresCurrentVerification`、`TestResumeCannotReuseStaleVerification` |
| ACC-04 | P0 · 离线 | 只读新任务尝试验收。另一个原本为可信模式的任务用只读权限恢复，原验收命令会写测试标记。 | 两次均在调用模型和运行验收前阻塞，标记未创建。 | `TestReadOnlyModeRejectsVerificationOnRunAndResume` |
| ACC-05 | P0 · 真实模型 | 分别用 `workspace` 和 `trusted` 对两个新夹具提交同一修改目标。可信模式附加用户验收命令。 | 两个夹具的文件内容均符合要求。前者为 `NEEDS_VERIFICATION/2`，后者为 `VERIFIED/0`。 | ACC-01、ACC-02 验证判定规则 |
| ACC-06 | P1 · 真实模型 | 可信 REPL 启动时指定 `-task-kind change`，先用 `/verify test -f status.txt` 设置验收，连续提交两次修改任务，中间不再设置。 | 第一次使用该命令且成功时为 `VERIFIED`。第二次为 `NEEDS_VERIFICATION`，任务记录中没有第二次验收调用。 | — |

ACC-05 可用以下命令。每次运行后核对 `cat "$CLAW_QA_CASE/status.txt"` 与退出码。

```bash
new_claw_case
"$CLAW_QA_BIN" -dir "$CLAW_QA_CASE" -mode workspace \
  -prompt '用文件工具把 status.txt 内容改为 ready，保留结尾换行，只修改这个文件'
printf '退出码 %s\n' "$?"

new_claw_case
"$CLAW_QA_BIN" -dir "$CLAW_QA_CASE" -mode trusted \
  -prompt '用文件工具把 status.txt 内容改为 ready，保留结尾换行，只修改这个文件' \
  -verify 'test "$(cat status.txt)" = ready'
printf '退出码 %s\n' "$?"
```

## 计划 持久化和恢复

```bash
go test -race -count=1 -v ./internal/taskstore
go test -race -count=1 -v ./internal/engine -run '^(TestPlan.*|TestResume.*)$'
```

| 编号 | 优先级 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| STATE-01 | P0 | 假模型逆序返回带依赖的两个步骤。工作区根目录预先放置自己的 PLAN/TODO。 | 引擎先执行就绪步骤。任务目录的 PLAN/TODO 与快照一致，根目录原有文件保持原样。 | `TestPlanRunsReadyStepsAndWritesTaskViews` |
| STATE-02 | P0 | 假模型分别提交循环依赖和未知依赖的计划。再让后续步骤阻塞并提出补丁，分别保留和改写已完成步骤。 | 无效依赖在执行工具前被拒绝。有效补丁保留完成步骤，改写完成步骤的补丁被拒绝。 | `TestPlanRejectsInvalidDependenciesBeforeExecution`、`TestPlanPatchPreservesCompletedSteps` |
| STATE-03 | P0 | 构造只有开始记录的文件写入，实际文件已是预期内容。恢复后假模型再次提出同一动作。 | 通过文件版本核实已有结果，动作 Handler 不再次执行。 | `TestResumeReconcilesConfirmedFileWriteWithoutReplay` |
| STATE-04 | P0 | Bash 已成功并记录动作结果，下一次模型调用失败。恢复后重复提出同一 Bash。 | 已写标记只出现一次，恢复使用已有结果。 | `TestResumeDoesNotReplayRecordedShellEffect` |
| STATE-05 | P0 | 日志中有已开始但无结果的 Bash 动作。恢复任务。 | 返回 `BLOCKED`，恢复期间模型和工具调用数均为 `0`。 | `TestResumeBlocksUnknownShellBeforeCallingModel` |
| STATE-06 | P0 | 用完 `1` 次模型额度后恢复，随后再用显式提高到 `2` 的额度恢复。 | 第一次恢复无新模型调用，第二次只允许再调用 `1` 次。 | `TestResumePreservesConsumedModelBudget` |
| STATE-07 | P0 | 制造 JSONL 尾部未写完的记录，然后恢复追加。另行损坏完整记录或快照。 | 撕裂尾部可恢复追加，完整记录或快照损坏时恢复报错。 | `TestRecoverInterruptedAppend`、`TestCorruptStateBlocksRecovery` |
| STATE-08 | P0 | 两个进程竞争同一工作区。另测试两个任务访问同一材料 ID 和通过软链接访问运行目录。 | 同一工作区只有一个主任务持有锁。材料按任务隔离，既有材料不能覆盖，越界路径被拒绝。 | `TestWorkspaceLockAcrossProcesses`、`TestArtifactsAreScopedAndImmutable`、`TestWorkspacePathsRejectSymlinks` |
| STATE-09 | P1 · CLI 联调 | 用可信模式完成 ACC-05。保存任务 ID，外部改动 status.txt，然后从相同目录和会话恢复该任务。 | 返回 `NEEDS_VERIFICATION/2`，历史验收不再有效，原已完成动作不重跑。 | `TestResumeCannotReuseStaleVerification` |

任务 ID 来自 CLI 的 `任务 <ID> · <状态> · <原因>`。人工观察时检查 `.claw/tasks/<ID>/events.jsonl`、`snapshot.json`、`artifacts/` 和启用计划时的视图。用 `-resume "$CLAW_QA_TASK_ID"` 配合原 `-dir`、原 `-session` 及当前允许的执行模式恢复。审批等待中、动作已开始、动作结果已落盘是不同的中断位置，应优先用夹具分别覆盖。

## 子智能体

```bash
go test -race -count=1 -v ./internal/engine -run '^TestSubagentReturnsOnlyReadEvidence$'
```

| 编号 | 优先级与方式 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| SUB-01 | P0 · 离线 | 父任务委派子任务读取两行文件。子报告引用实际读取的第 `2` 行及不存在的第 `100` 行。 | 第 `2` 行证据保留，第 `100` 行转为未核实项，报告为 `PARTIAL`。子模型仅看到只读工具。 | `TestSubagentReturnsOnlyReadEvidence` |
| SUB-02 | P0 · 离线 | 父任务和子任务使用同一套模型与工具额度完成 SUB-01。 | 全任务结算为 `4` 次模型调用和 `2` 次工具尝试。 | `TestSubagentReturnsOnlyReadEvidence` |
| SUB-03 | P1 · 定向补测 | 用假模型同批发起三条委派并记录正在运行的数量，再取消父任务。 | 同时运行的子任务不超过 `2` 个，父任务取消传到已启动子任务，等待中的委派不启动。 | — |
| SUB-04 | P1 · 定向补测 | 给子任务设置低轮数，令其重复读取直至耗尽。另一次向子任务直接提交 Bash 或递归委派请求。 | 子任务失败或阻塞通过工具结果交回父任务。Bash 和递归委派都没有执行。 | — |

## 用量 费用和追踪

```bash
go test -race -count=1 -v ./internal/observability
```

| 编号 | 优先级 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| OBS-01 | P0 | 配置人民币输入单价 `2`、输出单价 `3` 每百万 token。并发返回十次 `100` 输入、`200` 输出。另测非人民币单价和缺少 Usage。 | 合计 `1000/2000` token，费用约 `¥0.008`，竞态检查通过。非人民币价格使费用未知，缺 Usage 使相应的 token 和费用未知。 | `TestConcurrentUsageWithUnpricedCalls` |
| OBS-02 | P0 | 对同一 ID 连续导出两份包含失败状态和测试敏感标记的追踪。 | 两份文件都存在，保留失败状态，提示词、工具参数和输出原文的测试标记未进入追踪。 | `TestTraceExportsUniqueRedactedRecords` |
| OBS-03 | P0 | 将 `.claw` 链接到工作区外，再导出追踪。 | 导出失败，外部目录未新增文件。 | `TestTraceCannotFollowWorkspaceEscape` |

人工核对 CLI-07 的 `.claw/traces` 文件时，记录运行 ID、工具调用 ID、状态、停止代码与调用次数。用量以 Provider 回执为准。

## 飞书 AgentOps

```bash
go test -race -count=1 -v ./internal/feishu
```

| 编号 | 优先级与方式 | 前置和步骤 | 预期结果 | 已有离线用例 |
| --- | --- | --- | --- | --- |
| FS-01 | P0 · 离线 | 同群连续发送两条事件，两群绑定同一工作区并发发起任务，再重启模拟服务并重投相同事件 ID。 | 同群按入队顺序运行，共享工作区串行，相同事件 ID 只处理一次，未授权发起者无任务。 | `TestQueueSerializesSharedWorkspaceAndPersistsDedup` |
| FS-02 | P0 · 离线 | 等待动作审批。分别用非审批人、另一群、合法审批人和相同 ID 重复回复。另对等待中的任务取消。 | 只有同群合法审批人可以消费一次。取消后的批准无效。 | `TestApprovalWaitBindsChatAndActorAndCancels` |
| FS-03 | P0 · 离线 | 将任务剩余时间缩短到毫秒级，在过期后批准。 | 有效期以任务剩余时间与审批最长有效期中较短者为准，过期后无动作。 | `TestApprovalWaitExpiresAtTaskDeadline` |
| FS-04 | P0 · 离线 | 审批等待期间外部修改目标文件，再批准原写动作。 | 旧批准不会覆盖新版本，版本冲突可观察。 | `TestFeishuRejectsApprovalAfterFileChanges` |
| FS-05 | P0 · 离线 | 为可信任务配置验收命令，等待该命令审批，合法审批后放行。 | 验收命令本身也要审批，执行成功并通过版本核对后返回 `VERIFIED`。 | `TestFeishuVerificationUsesCommandApproval` |
| FS-06 | P0 · 离线 | 将事件台账配置为群工作区、其子目录、其祖先或软链接别名。再换成并列独立目录。 | 重叠配置被拒绝。独立目录配置可启动。 | `TestEventLedgerAndWorkspaceCannotOverlap` |
| FS-07 | P0 · 飞书联调 | 测试群绑定只读工作区。分别由已授权成员与未授权成员发送「读取 status.txt 并返回内容」。 | 已授权请求在原群收到回复与任务摘要，无写审批。未授权请求不启动任务。 | — |
| FS-08 | P0 · 飞书联调 | 可信测试群要求修改 `status.txt`，配置验收 `test "$(cat status.txt)" = ready`。先核对审批消息与文件内容，再由同群审批人批准写入，随后另行批准验收命令。 | 等待写审批时文件仍是 `draft`。批准后变为 `ready`。等待验收审批期间未运行该命令，通过后才得到 `VERIFIED`。 | — |
| FS-09 | P0 · 飞书联调 | 分别拒绝动作、从另一群批准、由非审批人批准、重复批准已处理 ID，以及发起人发送 `/cancel` 后批准旧 ID。每个场景重新发起任务。 | 非法或过时审批不会触发对应动作。取消停止等待。使用文件内容或动作事件核对，群消息措辞作为辅助。 | — |

联调需要独立测试群、独立工作区和事件台账，以及 `OPENAI_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_VERIFY_TOKEN`、`FEISHU_ENCRYPT_KEY` 和指向配置文件的 `FEISHU_AGENTOPS_CONFIG`。群配置样例见仓库 README。使用已配置的测试应用回调连接 `POST /webhook/event`。在仓库根目录用 `go run ./cmd/agentops` 启动，默认监听 `127.0.0.1:48080`。

发送两条相同文字会产生两个不同事件。验证重复事件 ID 时使用 FS-01 的离线夹具。服务重启后，尚未开始的内存排队任务需要发送新消息重新提交。

## 真实模型 Bench

在仓库根目录使用 `go run ./cmd/bench`。默认使用 OpenAI 的 `gpt-6.1-sol`，需要可用的 `OPENAI_API_KEY`，会产生真实模型调用。显式指定 `-model glm-4.7` 时使用智谱和 `ZHIPU_API_KEY`。需要核对原始退出码时，先用 `go build -o "$CLAW_QA_DIR/bench" ./cmd/bench`，再执行二进制。

| 编号 | 优先级与方式 | 操作 | 预期结果 |
| --- | --- | --- | --- |
| BENCH-01 | P1 · 真实模型 | 运行 Bench，查看 `test_001_edit`。 | 通过条件为 `config.json` 精确改成预期内容，工作区除 `.claw` 外无多余普通文件。 |
| BENCH-02 | P1 · 真实模型 | 查看 `test_002_code_gen`。 | 通过条件为 `math_test.go` 存在，指定 `TestMultiply` 实际执行并通过。 |
| BENCH-03 | P1 · 真实模型 | 核对逐用例摘要、总通过数及二进制退出码。 | 摘要有状态、耗时、模型次数、工具次数和费用口径。全部通过退出 `0`，否则退出非零。 |

## 测试记录

先执行离线 P0，再做 CLI 冒烟和所需的飞书联调。上线前，P0 中涉及越界、误报验收通过、未知副作用重放和越权审批的用例必须有直接证据。真实模型没触发预期动作时记录「未覆盖」，用对应离线夹具判断规则。

| 编号 | 环境与版本 | 实际结果 | 状态 | 证据或缺陷 |
| --- | --- | --- | --- | --- |
| 例 CLI-07 | 系统、Go 版本、提交或工作区标识 | 任务状态、退出码、文件检查结果 | 通过 / 失败 / 阻塞 / 未覆盖 | 任务 ID、追踪文件、测试输出或缺陷链接 |

保留能重现问题的命令、夹具、任务 ID 和关键输出。共享记录前检查其中的密钥、真实群聊身份和文件内容。
