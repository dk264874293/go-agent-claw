# 本地 Agent 任务观测台 v1

观测台集中展示任务、运行尝试、执行时间线、文件效果、验收、异常与评测结果。它不执行任务，不提供启动、取消、恢复、审批、重试或删除数据的网页接口，也不调用模型和 oracle。

## 启动

```bash
go build -o claw-monitor ./cmd/claw-monitor
./claw-monitor -workspace /绝对路径/工作区
```

工作区和评测目录可重复登记：

```bash
./claw-monitor \
  -workspace /绝对路径/项目A \
  -workspace /绝对路径/项目B \
  -eval-root /绝对路径/评测批次或批次父目录
```

服务只监听 `127.0.0.1`，默认由系统分配端口；可用 `-port` 指定端口。复制终端打印的完整地址到浏览器。地址中的 fragment 凭据用于兑换本机会话，登录后自动从地址栏移除；服务重启后须重新使用新地址登录。不要分享启动地址。

前端使用原生 HTML/CSS/JavaScript 并内嵌在 Go 二进制中，运行时不需要 Node、外网资源或 CDN。服务关闭不会取消 Agent；监测重启后重新读取源数据。

## 数据与状态

登记工作区中的 `.claw/tasks` 提供任务事件、快照及证据，`.claw/traces` 提供历史耗时。评测目录中的 v2 `result.json` 提供独立评分；导入不会重跑 Agent 或评分器。

任务状态与运行活动分开展示：

- 引擎结果保留 `ANSWERED`、`VERIFIED`、`NEEDS_VERIFICATION`、`BUDGET_EXHAUSTED` 等原始语义。
- 每次入口调用拥有独立 attempt ID；缓存结果可能引用之前的 RunID，两者不混用。
- `finished` 只表示任务日志中出现了结果；`run_finalized` 表示引擎收尾；`run_returned` 表示调用方已收到结果。终报事件仅说明发送尝试。
- 观测返回结果与持久结果冲突时分别展示并提示；监测不会改写引擎状态。
- 文件回执说明动作当时的确认。`UNKNOWN`、确认无变化、历史记录不完整均单独显示，不证明当前文件仍存在或任务已验收。
- 历史验收不自动重新核对工作区。外部 oracle 通过也不把 `NEEDS_VERIFICATION` 改为 `VERIFIED`。
- 历史事件缺少时间或阶段时显示未知；Trace 缺失不判失败。首版没有文件 Diff 和完整模型会话查看器。

Reader 只读，不取得执行锁、不修复日志。半行等待后续写入；坏行、序号或身份冲突及未知版本标记异常。索引仅在内存中，可重建；每秒增量刷新。

## 新增观测记录

CLI、飞书及 Bench 的新运行默认采集轻量观测，默认位置为 `~/.claw/observations/<attempt_id>/events.jsonl`。使用以下环境变量控制：

先重新构建实际使用的执行入口（例如 `go build -o claw ./cmd/claw`），再照常发起任务。已有旧二进制不会因启动观测台而获得新采集点；它的持久任务记录仍可作为历史数据读取。

```bash
CLAW_OBSERVE=off ./claw -dir /工作区 -prompt '任务'
CLAW_OBSERVE_DIR=/自定义观测目录 ./claw -dir /工作区 -prompt '任务'
./claw-monitor -workspace /工作区 -observe-dir /自定义观测目录
```

目录权限为 0700，文件权限为 0600。当前观测事件 `schema_version` 为 1。每个 attempt 内事件序号单调递增，包含版本、时间、工作区、任务、Agent、动作和模型调用关联。关键任务状态仍由原 taskstore 管理，新增观测不替代它。

事件包括运行生命周期、任务事件提交、模型开始/结束/用量、工具尝试/准入/Handler/结果、历史回执复用、审批等待与决定、子任务排队与执行、证据引用和终报尝试。子任务共享父任务预算，用独立 Agent ID 表示并发关系。

新增记录采用字段白名单：不复制模型消息、文件正文、Bash 命令或审批身份。参数保存路径、长度、范围、版本或摘要指纹。用量按模型调用 ID 去重；未知 Token、费用和价格保持 null，已知部分与未知调用数同时呈现。

记录器默认队列 1024 条、每条最多 8 KiB、每 attempt 最多 16 MiB，其中 64 KiB 留给结束与缺口记录；250 ms 刷新，退出等待最多 1 秒。观测队列满、超限或写盘失败不会改变引擎结果；通过序号缺口、`observation_gap` 和 `recorder_closed` 标记不完整。Dropped 是累计值，不应逐条相加。进程强杀或永久存储故障可能没有结束记录。

这与关键任务证据失败不同：原 taskstore/验收证据故障继续执行既有 `FAILED/storage_error` 规则。

## 页面与诊断

- **总览**：默认首页。按当前工作区查看运行活动、等待审批、有提示任务、活动未知四项任务计数，以及最近 6 个任务和按提示数量优先排列的 6 个待核实任务；工作区健康异常同步展示。
- **任务**：组合工作区、引擎结果、运行活动、关键词与“仅看有提示”筛选，按最近观察或提示数量排序，分页查看预算与活动。
- **详情**：切换运行尝试，查看模型、工具、审批和子任务时间线及结构化详情；按 Agent 和事件类别筛选已加载事件，默认收起记账噪声，可选择显示全部事件；历史 Trace 按运行展示。
- **交付与验收**：查看最终答复、效果账本、历史验收与外部评分，按需读取证据。
- **异常**：按任务分组分页，支持工作区、任务关键词与异常类别筛选；提示保留原始代码，可进入任务、定位事件或读取证据。
- **评测**：随工作区切换数据，再按实验、分组和综合结果筛选样本，分别核对五维评分和失败标签。

四项概览都按任务计数，覆盖当前工作区全部记录，不受任务列表分页影响。分类可以重叠：“等待审批”属于“运行活动”，“有提示任务”也可能正在运行。`active` 与 `finalizing` 计入运行活动；等待审批只读取最新非历史尝试的当前审批活动；`unknown` 仅表示活动未知；`returned` 与历史 `result_recorded` 分开展示。记录缺失不会被推算成执行成功或失败。

工作区、视图、任务筛选与页码、任务 ID、运行尝试、详情标签及事件定位写入 URL 查询参数。刷新和浏览器前进、后退可恢复阅读入口，例如 `/?view=detail&task=<观测任务ID>&attempt=<尝试ID>&tab=timeline&event=<事件序号>`。访问凭据仍通过启动地址的 fragment 兑换，登录后移除，不写入这些查询参数。实时更新保留阅读位置与展开内容，出现新增事件时提供查看入口。

初始告警阈值均为提示，不控制执行：

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `-model-remaining` | 1 | 剩余模型调用提示 |
| `-tool-remaining` | 2 | 剩余工具调用提示 |
| `-repeat-count` | 3 | 同一 Agent 连续相同只读请求与结果 |
| `-model-slow` | 60s | 模型调用耗时提示 |
| `-tool-slow` | 30s | 工具执行耗时提示，审批等待不计入 |
| `-stale-after` | 10s | 活动信号缺失提示，新运行每 2 秒产生信号 |

活动信号缺失只能表明状态未知，不能证明进程崩溃。未知效果提示核实，不自动重放操作。重复探索的请求身份包含分页参数，正常翻页不会按完全相同请求处理。

证据接口每页最多返回 16 KiB。为避免密钥被分页边界拆开，服务端先对最多 1 MiB 的有界内容脱敏，再按 UTF-8 边界分页；超过上限明确显示截断，不自动加载剩余正文。任务内容按纯文本展示，不执行其中的 HTML。

## HTTP 与访问边界

读取接口均位于 `/api/v1/`：`workspaces`、`overview`、`tasks`、`tasks/{id}`、`alerts`、`attempts/{id}/events`、`evidence/{id}`、`evaluations` 与 `stream`。

| 接口 | 查询参数与返回 |
| --- | --- |
| `GET /overview` | 可选 `workspace`；同一索引快照返回 `revision`、全量任务 `total`、`counts`、`recent`、`attention`、`workspaces`。`counts` 字段为 `running/approval/alerts/unknown`；`recent` 和 `attention` 最多各 6 个轻量任务，后者按全部提示数量降序再按最近观察排序。 |
| `GET /tasks` | 保留 `workspace/status/q/page/page_size`，新增 `activity_group/has_alerts/sort/view`。默认 `items` 仍为完整 Task，`view=summary` 改为轻量 TaskSummary；响应保留 `total/page/page_size`。 |
| `GET /alerts` | 支持 `workspace/q/category/page/page_size`；按匹配任务分页返回 `items: [{task, alerts}]`，`task` 为 TaskSummary。`total` 是全量匹配任务数，`alert_total` 是全量匹配提示条数；每条提示保留原始字段并增加 `category`。 |
| `GET /evaluations` | 可选 `workspace`；返回该工作区的 `items`，未指定时返回全部评测。 |

`q` 不区分大小写，匹配任务目标与原始任务 ID；异常搜索也采用同一任务搜索口径。`status` 匹配原始引擎结果。任务列表新增参数如下：

- `activity_group`：`running/approval/unknown/returned/recorded`，含义同页面活动分类；留空为全部，未知值无匹配。
- `has_alerts`：仅 `true` 和 `false` 生效，分别筛选有提示或无提示的任务；省略或其他值不增加该条件。
- `sort`：`alerts` 按提示数量降序，其他值及省略使用最近观察时间降序；相同时间用任务 ID 升序兜底，缺少时间的任务排在最后。
- `view`：仅 `summary` 切换轻量返回，其他值沿用原始完整 Task，`tasks/{id}` 继续返回完整详情。

TaskSummary 字段为 `id/workspace_id/task_id/goal/mode/kind/activity/activity_groups/engine_status/stop_code/record_quality/last_observed_at/budget/usage/alert_count`，不包含答复正文、结构化报告、历史尝试、效果明细或证据正文。`activity_groups` 可以同时包含 `running` 和 `approval`；`alert_count` 为任务全部提示数，异常页 `alerts` 则只包含当前类别匹配的提示。

任务详情可选 `local_checks` 展示局部确定性核对的身份、结果、前提和来源位置；这些是动作当时的记录，不能证明当前文件、自然语言结论或运行验收通过。完整检查输入和回执不枚举或经证据API提供。普通读取策略3只开放已提交的来源材料和可信产物，索引落后时不猜测新材料类别；通用受限材料保留CLI读取入口。构建并重启观测台后生效，详见[来源复用与局部核对](source-reuse-v1.md)。

异常 `category` 接受 `budget/unknown/repeat/slow/activity/record`，依次表示预算、未知效果、重复探索、调用耗时、活动未知、记录异常；留空为全部，未知值无匹配。未匹配其他类别的代码归入记录异常，原始 `code` 不被分类结果替代。

任务与异常分页默认每页 50 个任务，上限 200；页码下限 1、上限 1,000,000，数字越界时截到边界，无法解析时使用默认值。超出结果范围返回空 `items`，保留全量总数。时间线每页最多 200 条事件。概览统计与异常总数均在分页前计算，不需要浏览器遍历所有任务页。

服务校验本机来源、Host 与同源 API 请求，不开放 CORS。唯一的 POST 是 `/api/v1/session` 会话兑换，使用 HttpOnly、SameSite=Strict Cookie。静态首页允许从外部启动器导航，但不包含任务数据。出站 JSON 统一脱敏，不向浏览器提供源路径读取接口；证据 ID 由服务端映射，越界符号链接和非普通文件被拒绝。

SSE 只发送视图变化通知；断线使用监测实例 ID 与序号续接。最多保留 1024 条通知，过期或跨实例游标触发 reset，页面重新获取视图。慢客户端不能阻塞执行。

### 模型请求诊断

新运行的时间线可展示 SDK 实际序列化后的参数白名单、渠道及协议、HTTP 错误、服务请求 ID 和前次成功调用的结构差异。未发送、null、零值、false 和未知分别显示；差异清单不完整时明确标注。旧记录不补造参数。

本地清单及诊断 ID 独立于工具证据，页面没有正文读取或下载接口。默认 `metadata` 只保存参数清单；显式 `on_error` 的失败正文仍只存本机独立目录。诊断丢失单独提示，不改变引擎结果。启用方式、配额和清理规则见 [请求诊断 v1](request-diagnostics-v1.md)。

## 保留与清理

首版不自动清理日志。先停止相关执行进程，再人工删除不再需要的全局观测 attempt 目录。删除新增观测不会删除工作区 taskstore、验收证据或评测原件，但相应实时细节将不可用。不要在运行时清理正在写入的目录。

原有任务与效果记录必须按项目既有规则单独备份和管理；观测台不提供数据修复或删除接口。

## 验证

```bash
go test ./cmd/... ./internal/...
go test -race ./internal/...
go build ./cmd/... ./internal/...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/real_eval/run.py selfcheck
CLAW_MONITOR_EVAL_FIXTURE=/绝对路径/v2批次 go test ./internal/monitor -run TestRealEvaluationImport -v
CLAW_MONITOR_PERF=1 go test ./internal/monitor -run TestLargeIndexPerformance -v
CLAW_MONITOR_PERF_ACCEPTANCE=1 go test ./internal/monitor -run TestPerformanceAcceptance -v
node --check internal/monitor/web/app.js
# 仅开发时的离线 DOM 回归；复用已有 jsdom，不引入运行时依赖
MONITOR_UI_JSDOM=/绝对路径/jsdom node --test scripts/monitor_ui_test.cjs
```

测试使用脚本 Provider、故障注入和已有真实评测结果，不新增付费模型请求。完整 HTTP 性能验收包含认证、脱敏、编码、分页及一秒索引刷新，并覆盖概览、轻量任务列表和异常分页，不将纯内存查询代替接口耗时。基础验收见 [2026-11-05 验证记录](local-monitor-results-2026-11-05.md)，本轮界面与查询优化见 [2026-11-06 验证记录](local-monitor-ui-results-2026-11-06.md)。
