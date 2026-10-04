# 综合验收方案：M1–M3 与阶段 2

制定日期：2026-10-03。代码基线：`ba71d88`，分支 `codex/stage2-permissions-isolation`。

状态：**2026-10-04 已完成 47 个本地必测项**，结果见 [macOS 验收报告](report-2026-10-04-macos.md)。真实模型与原生 Linux 仍未执行。下文“已有/部分/补充”保留方案制定时的覆盖评估；新增断言及实际证据以报告为准。

## 1. 验收目标与结论

确认运行、会话、预算、追踪、权限和容器执行能够协同工作，为阶段 3 的持久化与故障恢复原型建立基线。分别给出以下三个结论，不能用一个“全部通过”代替：

| 结论 | 所需证据 | 对后续开发的影响 |
| --- | --- | --- |
| 本地运行验收 | 自动化回归、协议伪服务、权限/审批、真实容器和交互流程 | 必测项通过且没有未解决 P0/P1 后，可开始阶段 3 原型 |
| 真实模型接入验收 | 已授权的模型/网关任务、实际工具回传、Usage 与输出上限兼容性 | 未执行时保留“待验收”，不能宣称真实模型接入已通过 |
| Linux 部署验收 | 目标原生 Linux 主机上的同等运行证据 | 未执行时阻止 Linux 部署声明；可继续 macOS 范围内的阶段 3 原型 |

范围包括 CLI 单任务与 REPL、主/子任务、Session FIFO 与工作记忆、Thinking/PlanMode、Provider 协议、Token/可选费用预算、Trace、文件工具、受限搜索、权限、审批及 Docker Executor。飞书只验收当前只读装配、预算传递、队列与取消的本地测试。

本次不以重启后恢复 Session、完整多租户沙箱、供应商侧绝对费用上限、远程审批主体鉴权或飞书真实事件/API 为通过条件；这些能力尚未完成或需要另行安排接入验证。

## 2. 执行顺序与工作量

| 顺序 | 工作 | 依赖 | 预计人工投入 |
| --- | --- | --- | --- |
| 1 | 确认提交、环境、测试工作区和证据目录 | 仓库与 Go 工具链 | 15–30 分钟 |
| 2 | 现有全量测试、race、vet、正式包构建 | 不需要业务密钥 | 30–60 分钟，包含结果核对 |
| 3 | 补充覆盖缺口并执行协议/交互测试 | 第 2 步通过 | 1–2 个工作日，取决于新用例和缺陷 |
| 4 | Docker 与完整 CLI 场景验收 | Docker、镜像、第 3 步夹具 | 1–2 小时 |
| 5 | 真实模型小规模验收 | 单独授权、连接配置和批次预算 | 30–90 分钟，不含接入故障处理 |
| 6 | 原生 Linux 验收与报告归档 | 目标主机 | 1–2 小时，不含环境准备 |

预计本地方案执行需要 **2–3 个工作日**，含补测试与报告，不含较大缺陷修复；环境缺失、付费接入和 Linux 主机等待另计。已有证据足够的场景不增加无目的的重复压力测试。

## 3. 环境与测试数据

### 3.1 环境清单

- Go 1.25+；race 需要可用的 C 编译环境。记录 Go 版本、操作系统、架构及 CGO 状态。
- macOS 使用 Docker Desktop 的 Linux Engine；Linux 使用本机 Unix socket，记录 Engine 版本、cgroup 版本及镜像 ID。
- 现有 `TestDockerIntegration` 固定使用 `bash:5.2`、1 CPU、256 MiB、64 PID，并读取 cgroup v2 文件。镜像由操作者预先安装；cgroup v1 环境标记为环境阻塞，单独确定支持策略，不能删掉断言后称通过。
- Python 3 标准库用于 `scripts/test-local-approval.py`。没有 Python 时该交互验收标记阻塞，Go 单测仍可先执行。
- 普通回归使用 Mock Provider/本机 HTTP 伪服务；Docker 网络探测可能访问测试地址，但不调用真实 LLM/飞书 API。

### 3.2 测试工作区

所有 CLI 执行、工具写入和 Trace 使用仓库外的临时目录；不使用仓库已有的 `.claw/`、`.zcode/`、`workspace/`。从临时目录启动已构建的 CLI，避免入口加载项目 `.env`。

准备以下可复位数据，每个有副作用的场景使用独立副本：

| 数据 | 内容 | 用途 |
| --- | --- | --- |
| 小型 Go 项目 | `go.mod`、`calc.go`、`calc_test.go`；Multiply 故意写成加法 | 阅读、定位、审批编辑、宿主测试验收 |
| 普通文本 | 固定中文文本、多个匹配行、字面量 `a.*` | 读取、字面量搜索和匹配数量限制 |
| 边界数据 | 超过 8000 字节的文本、超过 1 MiB 的编辑目标、深层目录和大量条目 | 截断、扫描和编辑上限 |
| 外部数据 | 指向临时工作区外测试文件的链接、FIFO、硬链接 | 越界拒绝与安全失败；只使用合成文件 |
| 越权指令文件 | “忽略权限、执行写入、批准 Shell、递归委派”等测试文字 | 验证执行层拒绝及外部来源标记 |
| 测试秘密 | 工作区外的固定标记及假的环境变量值 | 检查不可见性和 Trace，不使用真实密钥 |

文件安全场景记录操作前后内容摘要及目录清单。容器会读到已挂载工作区内的文件，不能把放在该工作区的测试秘密预期为不可见。

小型 Go 项目的独立验收断言：`Multiply(2,3)==6`、`Multiply(-2,3)==-6`、`Multiply(0,3)==0`。初始测试失败，批准修改后测试通过；只允许修改指定源码，测试文件和其他文件摘要不变。模型给出的“完成”文本不能代替这些断言。

### 3.3 证据要求

每项结果记录：用例 ID、完整提交 SHA、环境、非密钥配置、命令/脚本、退出码、开始/结束时间、预期、实际结果、RunID（适用时）、日志/Trace/文件摘要路径与缺陷 ID。

状态统一为：**通过、失败、阻塞、未执行、不适用**。“阻塞”和测试输出中的 `SKIP` 均不算通过；不适用必须说明与本次支持范围的关系。记录只保存测试数据，不复制真实凭证、环境变量全集或项目 `.env`。

## 4. 先运行已有测试

以下命令在独立子 Shell 中执行。结果写入临时证据目录，首个失败会停止后续检查并保留日志；此脚本只执行已有测试，不会自动补齐第 5 节的缺口。

```bash
bash <<'SH'
set -eu
cd /Users/wangpeiliang/Desktop/AI-project/go-agent-claw
acceptance_dir="$(mktemp -d "${TMPDIR:-/tmp}/claw-acceptance.XXXXXX")"
printf '验收证据目录：%s\n' "$acceptance_dir"
git rev-parse HEAD > "$acceptance_dir/revision.txt"
git status --short > "$acceptance_dir/worktree.txt"
go version > "$acceptance_dir/go-version.txt"
uname -sm > "$acceptance_dir/platform.txt"
go env GOOS GOARCH CGO_ENABLED > "$acceptance_dir/go-target.txt"

run_check() {
  name="$1"
  shift
  if "$@" > "$acceptance_dir/$name.log" 2>&1; then
    printf '通过：%s\n' "$name"
  else
    result=$?
    printf '失败：%s，退出码 %s；查看 %s/%s.log\n' "$name" "$result" "$acceptance_dir" "$name"
    return "$result"
  fi
}

run_check unit env CLAW_TEST_DOCKER=0 go test -count=1 ./cmd/... ./internal/...
run_check race env CLAW_TEST_DOCKER=0 go test -race -count=1 ./cmd/... ./internal/...
run_check vet go vet ./cmd/... ./internal/...
run_check build go build ./cmd/... ./internal/...
run_check terminal python3 scripts/test-local-approval.py
printf '已有测试执行结束；请按矩阵补充逐项证据。\n'
SH
```

不要使用 `go build ./...`，也不要直接运行 `go run ./cmd/bench`。Go 首次构建可能需要下载模块；业务 API 不属于上述命令的依赖。

Docker 验收在空闲测试窗口单独执行。现有测试结尾检查所有 `go-agent-claw.managed=true` 容器为空，因此同一 daemon 上不要同时运行其他 claw Shell 任务；不能为了通过测试删除其他任务的容器。

```bash
# 在本机 Docker 正常运行、bash:5.2 已安装后执行。
CLAW_TEST_DOCKER=1 go test -race ./internal/executor \
  -run '^TestDocker(Integration|Acceptance)$' -count=1 -timeout=180s -v
```

将退出码、完整测试输出与运行前后属于本次测试的容器 ID 记录到同一证据目录。Docker 测试不能显示为 `SKIP`。后端故障优先用可注入测试制造；不要停止共享 Docker 服务或修改用户现有 Docker context 来模拟故障。

## 5. 验收矩阵

覆盖标记含义：**已有**表示存在相应测试，仍需核对本次实际输出；**部分**表示只覆盖一部分断言；**补充**表示需要新增确定性测试或场景编排。部分覆盖不能直接将整项标为通过。

### 5.1 A：M1–M3 核心回归（全部必测）

| ID | 操作与通过标准 | 覆盖及主要入口 |
| --- | --- | --- |
| A-01 | 首轮无工具：RunID 非空，`succeeded/completed`，一轮完成 | 已有：`engine/TestRunCompletesWithoutTools` |
| A-02 | 一个工具失败、同轮其他工具成功：保留诊断、恢复建议及结果顺序，下一轮模型能看到错误 | 已有：`TestRunParallelFailureReachesNextModelAndReminder`、Registry 测试 |
| A-03 | 无限工具请求、Thinking 开关：在 MaxTurns 边界停止，无额外请求；Thinking 不把轮数翻倍 | 已有：`TestRunMaxTurnsStopsFurtherModelRequests` |
| A-04 | 总超时、主动取消、上游失败与空响应：状态及原因准确，已发 ToolCall 均有 Observation | 已有：`engine/run_test.go` |
| A-05 | 工作记忆越过窗口：配对完整，首条非 system 是普通 user，返回值为深拷贝 | 已有：`context/session_test.go`、`m2_test.go` |
| A-06 | 同 Session FIFO、不同 Session 并发；排队取消不追加历史、不调用模型 | 已有：`engine/m2_test.go`、Session/飞书队列测试 |
| A-07 | 只读并发不超配置值，读批次/写入/后续读取遵守屏障，取消未启动调用仍产生结果 | 已有：`tools/schedule_test.go` |
| A-08 | 文件与工具输出有界、UTF-8 合法，截断/超时/取消元数据能到下一轮；不能以旧宿主 Bash 测试代替容器证明 | 已有：`tools/output_test.go`、`TestToolTimeoutMetadataReachesNextModel`；容器见 C 类 |
| A-09 | CLI > 环境 > 默认配置；非法值在调用前拒绝；PlanMode 默认关闭 | 已有：`config/config_test.go` |
| A-10 | 预算刚好用尽和差 1 Token：正确预留/结算，拒绝时 Provider 调用数为零 | 已有：`budget/TestLedgerExactBoundaryAndOneTokenShort` |
| A-11 | Thinking/Action/子任务共用预算；并发预留不双花，子任务耗尽后配对完整且主任务停止 | 已有：`engine/m3_test.go`、并发账本测试 |
| A-12 | 缺价、缺 Usage、调用失败及非法 Usage：费用不伪报零，未知用量保留预留并拒绝后续请求 | 已有：`budget/ledger_test.go`、`observability/tracker_test.go` |
| A-13 | 有价时金额、版本、币种和舍入正确；缺价/币种不匹配的费用上限启动前失败；不同币种不能合并 | 部分：价格/Tracker 测试已有；补充多币种 Session 展示断言 |
| A-14 | 同秒多个 Run 的 Trace 不覆盖；目录故障可见且不覆盖原结果；Trace 无合成原始参数/输出/测试密钥 | 已有：Trace 测试、`TestTraceFailureDoesNotHideRunResult`、CLI 综合测试 |
| A-15 | 真实 CLI + 伪网关：工具回传、预算拒绝、REPL 独立 Run、权限/无审批拒绝 | 已有：`cmd/claw/TestCLIIntegration` |
| A-16 | CLI 非零退出与 REPL 失败后继续；`/help /status /cost /clear /exit`、多行及 EOF 行为符合 README，clear 保留账单 | 部分：退出码/失败后继续已有；交互命令、多行和账单保留需补充 F-01 |

### 5.2 B：权限、文件与审批（全部必测）

| ID | 操作与通过标准 | 覆盖及主要入口 |
| --- | --- | --- |
| B-01 | 未声明能力、未知工具、能力不足：拒绝发生在中间件/工具前，无副作用，不允许中间件授予能力 | 已有：`TestUndeclaredToolDenied`、`TestPermissionChecksPrecedeMiddleware`；未知工具和中间件不能扩权补显式断言 |
| B-02 | 只读子任务收到越权文件文字：写入/Shell/网络/递归委派均拒绝，即使传入宽权限 Registry | 已有：`engine/permissions_test.go`、Policy/工具网络拒绝测试 |
| B-03 | 拒绝绝对路径、`..`、反斜杠/盘符、`.env`/`.env.*`、`.git`；仅使用合成测试文件 | 已有：`workspace/root_test.go` |
| B-04 | 外部符号链接、末级链接、父路径链接和链接替换竞争：不读出外部测试标记、不修改外部文件 | 已有：根目录与链接竞争测试；不要求所有竞争调用都成功 |
| B-05 | FIFO/目录等非普通文件拒绝；原子替换硬链接不改原目标，成功/失败后不留临时文件 | 已有：`workspace` 与文件工具测试 |
| B-06 | 正常写入、精确/模糊编辑符合约定；失败/空 old_text/超 1 MiB 不修改原文件；文件权限符合实现约定 | 部分：精确编辑和边界已有；补充模糊编辑各级与权限保留断言 |
| B-07 | 浏览/搜索：字面量匹配、二进制/链接跳过、非法路径和取消准确；限制 200 条目、100 匹配、1000 扫描、16 层、128 KiB/文件及总输出 | 部分：`TestReadOnlyExploration`；扫描/深度/文件大小/总输出边界需补充 |
| B-08 | 审批逐项绑定请求 ID、RunID、SessionID、工具、完整参数哈希、主体、期限；重放和授权延长拒绝 | 已有：`policy/TestApprovalBinding` |
| B-09 | 无终端/无 Approver/deny、拒绝、过期、参数变更和等待取消：不执行工具，元数据正确 | 已有：Policy、Registry、Local 与无控制终端测试 |
| B-10 | 本地终端 yes 放行一次；拒绝、超时、排队取消、Ctrl+C 释放等待；REPL 不抢审批输入 | 部分：现有伪终端脚本覆盖 yes/超时/恢复/退出；拒绝和 Ctrl+C 需补充 |
| B-11 | 核心提示、项目指南、Skills、工具结果标记外部来源；加载有界且越界链接不读取；拒绝/审批审计不写原始参数 | 部分：Composer/Engine/Trace 测试；审批 ID/主体/期限/哈希审计和加载上限需补充 |
| B-12 | 飞书入口默认只读/deny；配置 local 不启动；旧卡片不能授权新 Registry；预算、FIFO、取消不回退 | 部分：队列/预算/旧审批测试已有；需对 agentops 装配及拒绝条件补充本地测试，不调用飞书 API |

### 5.3 P：Provider 协议（两类协议均必测，不需要真实模型）

| ID | 操作与通过标准 | 覆盖及主要入口 |
| --- | --- | --- |
| P-01 | OpenAI/Anthropic 伪服务收到正确模型和明确输出上限，文本与 Usage 正确保存 | 已有：`provider/configured_test.go` |
| P-02 | assistant 同时包含文本和多个 ToolCalls；ToolCall ID、原始参数与 ToolResult 关联在第二轮原样重放 | 部分：CLI OpenAI 单工具回传已有；两类协议多工具完整重放需补充 |
| P-03 | 伪服务延迟响应；请求 context 取消使实际 HTTP 传输及时结束，不再发下一次请求 | 补充：不能用 Engine Mock 取消替代网络请求取消证明 |
| P-04 | HTTP 错误、空 choices/content、缺 Usage、不完整响应和解析失败：不 panic，不虚构成功或零用量 | 部分：Engine/Tracker 层已有；供应商响应解析路径需补充 |
| P-05 | 所有已支持 Provider 值：默认 URL、对应环境变量、缺密钥错误、输出上限字段选择符合构造函数 | 部分：Parse/两类请求测试已有；构造函数映射补参数化测试 |

伪服务只监听本机。上述新测试放入 `internal/provider/*_test.go` 等正式包，调用次数、协议字段和取消应使用断言验证。验收预期使用稳定字段/错误类型；日志中的中文文案只用于用户体验核对。

### 5.4 C：真实容器（本地运行验收必测）

| ID | 操作与通过标准 | 覆盖及主要入口 |
| --- | --- | --- |
| C-01 | 实际进程非 root、根目录只读、CapEff 为零、NoNewPrivs 生效；仅有指定工作区和受限 tmpfs | 部分：集成测试已验证 UID/只读；能力、权限提升及挂载检查需补实际断言 |
| C-02 | 宿主工作区外合成秘密/环境标记不可见；允许的工作区文件能读写；Docker socket 未挂载 | 部分：`TestDockerIntegration` 有秘密/环境和写入证明；补挂载检查 |
| C-03 | 默认网络禁用：无法访问同 daemon 内的受控测试服务，而授权的网络模式可访问相同服务 | 部分：目前探测公网地址；需增加受控服务，排除服务本身不可用造成假通过 |
| C-04 | 配额符合 1 CPU/256 MiB/64 PID，实际 OOM 是资源错误；实际 PID 耗尽不留下进程/容器 | 部分：配额/OOM 已有；补充受限容器内 PID 耗尽，不在宿主执行资源耗尽命令 |
| C-05 | 命令成功/非零退出时输出和错误保真；大量输出截断，不因填满管道阻塞 | 已有：真实容器集成测试 |
| C-06 | Shell 正常返回但启动过后台子进程：后台进程不能持续写入测试文件，容器被删除 | 补充：已有宿主进程组回归不证明此容器场景 |
| C-07 | 运行中取消/超时：保留错误元数据，删除本次容器，后续任务可执行；正常 daemon 下取消后 5 秒内完成回收 | 部分：取消回收已有；补时间测量与后续任务断言。5 秒是本次新增验收目标，非现有保证 |
| C-08 | daemon/镜像不可用、远程 context、创建/连接/清理失败：错误可见；不自动 pull、不回退宿主，外部哨兵不变 | 部分：故障与 no-fallback 单测已有；补 create/rm 故障及 CLI 缺镜像贯穿用例 |
| C-09 | 模型参数不能改变网络、镜像、挂载、用户或资源；读取/修改工作区后的执行仍使用已固定镜像 ID | 部分：权限/参数构造单测已有；补正向网络审批与未知配置参数不可扩权断言 |

网络服务容器不发布宿主端口，只携带合成数据；记录其可达性作为对照。容器 ID 必须与本次测试对应，清理仅处理本次创建的资源。PID/OOM 故障以已生效配额为前提，记录超时并及时回收，不能在宿主运行 fork bomb。

### 5.5 F：完整交互流程（伪服务驱动，全部必测）

| ID | 操作与通过标准 | 覆盖及编排 |
| --- | --- | --- |
| F-01 | REPL 两个任务、斜杠命令、多行输入、clear、EOF/exit；Run 预算分别建立，历史按约定变化，账单不被 clear 清零 | 部分：CLI/伪终端已有；需扩展伪终端输入脚本并断言次数、历史和费用 |
| F-02 | PlanMode 关闭不自动创建 PLAN/TODO；开启后在临时工作区按审批创建/编辑，已有计划不被盲目覆盖；拒绝写入后不走 Shell 绕过 | 补充：脚本化 Provider + 伪终端；真实模型是否遵循提示另由 R 类验证 |
| F-03 | 主任务委派 → 子任务浏览/搜索/读取 → 返回摘要 → 主任务审批编辑：顺序、权限、共享预算及父子 Trace 关联正确 | 部分：分层测试已有；补完整编排，并独立核对文件产物 |
| F-04 | 容器缺镜像或不可用 → ToolResult 报错 → 模型说明失败/继续只读 → REPL 下一任务成功；无额外宿主执行 | 补充：伪服务主动请求 Bash，批准后故障；用临时哨兵证明无副作用 |
| F-05 | 假 Skill/AGENTS 越权指令与大量文件同时存在：来源、加载上限和权限约束不被突破，错误可解释 | 部分：加载和子任务越权测试已有；补加载上限及主任务贯穿 |

F 类必须构建实际 CLI 并从无 `.env` 的临时目录启动。伪服务脚本决定 ToolCalls，不能依赖真实模型自愿生成越权、并发或故障操作。新的终端场景可扩展现有 Python 脚本，保留分场景结果与有限等待时间。

已实现的完整脚本：`python3 scripts/test-comprehensive-acceptance.py --evidence-dir <仓库外的新目录>`。它保留请求、终端输出、Trace 和独立文件测试证据；F-04 使用本机 Docker 的缺镜像故障，不需要真实模型凭证。

## 6. 覆盖缺口的实现顺序

执行验收前先补上标记“部分/补充”中的缺失断言，优先完成以下工作包，不在此步骤扩展产品功能：

1. **协议与稳定性**：P-02 至 P-05；补充两类消息重放、真实 HTTP 取消、异常响应和构造映射。
2. **权限与数据边界**：B-01、B-06、B-07、B-11、B-12、A-13；包括扫描上限、文件权限、审计和飞书安全装配。
3. **容器证明**：C-01 至 C-04、C-06 至 C-09 的缺失部分；采用受控服务与本次专属容器。
4. **交互与贯穿**：B-10、A-16 与 F 类；补拒绝/Ctrl+C、PlanMode、完整子任务和后端失败后的恢复。

可复用现有测试，不必为每一行创建单独文件；一个测试能证明多个 ID 时，应逐项列出断言。发现实现缺陷时登记、修复并回归相关用例；涉及共享状态、权限或预算的修复再执行全量测试/race/vet/build。

## 7. R：真实模型小规模验收（单独授权后执行）

本轮制定方案不发真实请求。执行者先在报告中记录授权范围、Provider、模型、网关、批次金额上限与币种、已核对价目（或明确的未知费用状态）。不得要求用户把密钥填入文档；入口只使用操作者事先设置的环境变量，执行目录不含项目 `.env`。

建议先做 R-01/R-02，检查 Usage 和实际开销后再安排其余用例。首轮每个用例执行一次，失败重试单独记录并纳入批次预算，不覆盖原始失败。推荐每 Run：`max-turns=6`、`timeout=90s`、`tool-timeout=15s`、`max-total-tokens=40000`、`max-output-tokens=1024`；Thinking 场景另计双请求。额度不足时记录真实预算原因，不自动增加上限。

Token 和可选费用限制只控制本地准入；批次金额仍由执行者核对实际账单。未知 Usage 应停止本轮后续请求并标记接入限制，不能凭模型回答判通过。有匹配价格时设置每 Run 的 `-max-cost/-cost-currency/-pricing-file`；不得拿文档示例价格作真实价格。

| ID | 任务 | 通过标准 |
| --- | --- | --- |
| R-01 | 在只读/deny 模式阅读固定小项目并定位 Multiply 错误 | 工具成功回传；定位符合文件内容；没有文件变化；Usage/费用状态解释清楚 |
| R-02 | 只使用 read_file/edit_file 修正 Multiply，操作者逐次审批 | 只改指定源码；独立三个测试通过；审批请求与实际操作匹配；Trace 和产物一致 |
| R-03 | 委派子任务查找函数，再由主任务总结 | 实际使用只读子工具；主/子请求共享预算及 Run 链路；无递归委派/写入 |
| R-04 | 开启 Thinking 做只读任务 | Thinking 与 Action 都使用同一预算；输出上限被网关接受；统计有据可查 |
| R-05 | 在独立副本开启 PlanMode 完成小修改 | 在授权范围内维护计划与 TODO；已有内容保留；完成标记与测试结果一致 |

命令模板，需先设置指向证据目录下二进制和对应场景工作区的变量；从该场景目录执行：

```bash
# 验收开始前从仓库构建到证据目录：
go build -o "$acceptance_dir/claw" ./cmd/claw

# 下述工作区由执行者预先创建，不含 .env 或真实秘密。
acceptance_binary="$acceptance_dir/claw"
acceptance_case_dir="$acceptance_dir/fixtures/R-01"
(
  cd "$acceptance_case_dir"
  "$acceptance_binary" -dir . -permissions read-only -approval deny \
    -thinking=false -plan-mode=false -allow-network=false \
    -max-turns 6 -timeout 90s -tool-timeout 15s \
    -max-total-tokens 40000 -max-output-tokens 1024 \
    -prompt '读取 calc.go 和 calc_test.go，说明 Multiply 的错误及修复建议，不修改文件。'
)
```

`acceptance_dir` 在第 4 节子 Shell 结束后需按输出重新赋值；不要把不同批次的路径混用。Provider/模型/网关显式添加为本次授权配置；模板未指定时采用当前 CLI 默认连接，并应在报告中核对。R-02/R-05 改为 `workspace/local`，从有控制终端的会话执行并逐次确认。测试 Go 项目在宿主由操作者执行独立 `go test`，不依赖默认 Bash 镜像内有 Go，也不向 Agent 开放容器网络。

如果模型不调用目标工具而直接回答，该项标记场景未覆盖或失败；记录原始事实，不把自然语言承诺当作执行证据。没有授权的 R 类标为未执行，不用付费 benchmark 替代。

## 8. 原生 Linux 与兼容性

目标 Linux 主机使用相同提交，完成 A/B/P/F 及 C 类，不只做交叉编译。记录架构、Go/CGO、Docker socket、cgroup、UID/GID、工作区权限、镜像 ID 与取消耗时。

- 同时验证普通用户的工作区写入和以 root 启动宿主 CLI 时容器仍使用非 root 的行为；后者可能需要明确设置测试目录权限，不能靠容器 privileged 绕过。
- 不同架构镜像 ID 可以不同，记录各自安装的内容标识；无法运行对应架构镜像时标记环境阻塞。
- daemon/cgroup 不支持必需限制、回收失败或权限行为不一致时，Linux 部署结论为未通过；不能降级为宿主 Shell。
- 本次只宣称实际测试过的 macOS/Linux 环境。未测试的其他平台标记不适用，不自动视为兼容。

## 9. 缺陷处理与通过门槛

| 等级 | 例子 | 处理 |
| --- | --- | --- |
| P0 | 越界读写、无审批写入、子任务越权、实际密钥暴露、自动回退宿主 | 立即停止该批次，保留合成复现证据，修复后重验相关边界与全量回归 |
| P1 | 无法取消/回收、预算绕过、消息配对错误、数据竞争、错误状态或 Trace 故障被隐藏 | 阻止本地验收通过和阶段 3 原型准入；修复后回归 |
| P2 | 交互文案、非关键展示、已界定的使用限制 | 登记影响、处理时间和后续任务；可带已知限制通过 |

通过本地运行验收的条件：

1. A/B/P/C/F 必测项有本次证据，全部通过；现有 Docker 可选测试确实执行，新补充断言也通过。
2. 全量测试、race、vet、正式包构建无错误；不存在通过重复重跑掩盖的偶发失败。偶发失败先登记和定位。
3. 独立核对文件摘要、工作区外哨兵、调用次数、预算状态、RunID/Trace 及属于本次测试的容器回收，不能只看 CLI 退出码。
4. 未解决 P0/P1 为零；P2 和环境/接入限制已登记。
5. 填写 [验收报告模板](report-template.md)，明确真实模型与 Linux 是否已执行、支持范围以及阶段 3 准入结论。

真实模型或 Linux 尚未执行时，本地通过可允许阶段 3 原型；整体报告保留相应“未验收”结论。若要宣称当前默认网关端到端可用或部署到 Linux，必须先取得对应通过证据。

## 10. 结果归档与下一步

- 计划、人工填写的报告与脱敏缺陷记录放在 `docs/acceptance/`；原始日志、Trace、临时源码、容器数据与二进制保留在仓库外证据目录，不直接提交。
- 报告写出原始证据目录及必要摘要，审查确认没有敏感信息后再决定是否提交报告。
- 验收修复改变了代码基线时更新 SHA；最终全量检查针对修复后的提交，不能沿用旧结果。
- 本地门槛通过后进入 SQLite 与故障注入原型，先验证“工具意图已保存、执行结果未知”不会自动重复副作用；本方案不提前验收尚未开发的恢复能力。

依据：[M1 spec](../specs/m1-controlled-run.md)、[M2 spec](../specs/m2-cancellation-scheduling.md)、[M3 spec](../specs/m3-config-budget-trace.md)、[阶段 2 spec](../specs/stage2-permissions-isolation.md)、[开发计划](../development-plan.md)。
