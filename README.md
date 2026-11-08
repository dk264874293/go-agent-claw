# go-agent-claw

普通任务可显式使用`-delivery-review on`，在首个交付边界按需处理重要命题、适用条件和已记录待办，并展示处理前后确认正文的字节比较；默认关闭、不隐式开启读取实验，不代表语义或运行验收。数值或明确JSON核对需另加`-local-checks on`；命名变量缺少绑定会给出结构化纠正提示，算术结果与分项解释分开。长篇缺少可定位引用、未展开别名和原文目录差异提供非阻断提示，不自动改写正文。笔记建议随已有工具响应小批增量提交，8KiB上限及安全折叠规则保持。配置、一次性额度及恢复边界见[交付前处理v1](docs/delivery-review-v1.md)，此前离线检查与真实实验停止情况见[验收记录](docs/delivery-review-results-2026-11-08.md)。

独立读取实验 v2 已完成实现及一对真实验收：候选交付通过、语义质量未达标，实验能力继续默认关闭。结果、原始消耗及审查限制见[实施与验收记录](docs/read-features-results-2026-11-07.md)。

普通新任务默认恢复读取策略2。来源视图、局部核对及结论提示由三个显式实验开关控制，任一开启使用策略4；见[独立读取实验v2](docs/read-features-v2.md)。历史来源复用策略3说明见[来源复用v1](docs/source-reuse-v1.md)。已有工作笔记 v3可通过现有读写工具的 `_work_notes` 参数提交增量笔记，继续采用软约束及有依据的正文折叠；递归发现优先，普通汇总核对明显的数值、默认值与版本矛盾。兼容、评测与边界见 [工作笔记与汇总效率](docs/work-notes-efficiency-v3.md)。

笔记采纳与交付修复：首次完整交付的原文即进入本响应候选目录，普通摘要无需 `findings`；按模型响应统计采纳，已确认产物的链接由回执生成。原答复与业务JSON保留，说明及双样本验收见 [笔记采纳与交付准确性](docs/notes-delivery-v1.md)。

任务覆盖与效率 v2：支持重复 `-read-scope`、`-read-name-glob`、`-require-full-read` 和 `-output-style concise|detailed`。显式完整要求才增加交付门槛；普通片段阅读保留。范围、恢复兼容与独立对照说明见 [覆盖与上下文效率](docs/coverage-efficiency-v2.md)。

go-agent-claw 是用 Go 编写的 ReAct 编码助手。模型提出工具调用，引擎管理执行权限、预算、任务进度与验收。支持 CLI、离线回归、真实模型评测和独立的飞书入口。

## 快速开始

支持 macOS、Linux，需要 Go 1.25。CLI、基准评测和独立飞书服务默认使用 DeepSeek 的 `deepseek-v4.1-flash-1017`，从环境变量或 `.env` 读取 `DEEPSEEK_API_KEY`。CLI 可用 `-model` 显式选择模型；真实对照套件固定使用 `gpt-6.1-sol`。

CLI 默认按模型名称选择渠道，`deepseek*` 使用 Deepseek，`glm-*` 使用 Zhipu，其余使用 OpenAI。可用 `-provider openai|zhipu|deepseek` 显式指定渠道，Zhipu 需同时指定该渠道实际可用的 `-model`。显式选择 OpenAI 且省略模型时，使用 `gpt-6.1-terra`。

三个渠道的 OpenAI 兼容接口分别读取自己的密钥和端点配置，自定义 URL 应包含对应的接口版本路径：

| 渠道 | 密钥环境变量 | 端点环境变量 | 默认端点 |
| --- | --- | --- | --- |
| OpenAI | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `https://api.openai.com/v1/` |
| Zhipu | `ZHIPU_API_KEY` | `ZHIPU_BASE_URL` | `https://open.bigmodel.cn/api/paas/v4/` |
| Deepseek | `DEEPSEEK_API_KEY` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |

例如在 `.env` 中配置 `ZHIPU_BASE_URL=https://gateway.example.com/api/paas/v4/`。端点配置留空时使用该渠道的默认端点。

网络代理是可选配置。可在启动目录的 `.env` 中添加以下内容，端口按本机代理设置调整：

```dotenv
HTTPS_PROXY=http://127.0.0.1:7891
NO_PROXY=localhost,127.0.0.1,::1
```

也可以在终端执行 `export HTTPS_PROXY=http://127.0.0.1:7891` 后启动程序。HTTPS 请求使用 `HTTPS_PROXY`，HTTP 请求可另设 `HTTP_PROXY`；`NO_PROXY` 指定绕过代理的地址。未配置代理环境变量时使用直连，Go 默认 HTTP 客户端的代理选择来自环境变量。修改代理配置后重新启动进程。

CLI 和独立飞书服务优先读取 `.env` 中的同名配置；Bench 优先保留进程中已设置的环境变量。`.env` 按程序启动时的当前目录加载，`-dir` 指定任务工作区。

从其他目录启动时，可以在启动目录创建指向已有配置文件的 `.env` 符号链接，统一维护配置。配置文件不存在时可使用进程环境；现有 `.env` 断链、不可读或格式错误时会停止启动并提示错误。更新配置后需退出已有 CLI 会话，再重新启动。

在项目根目录使用构建入口，可自动将已配置的代理和三个渠道的端点默认值写入二进制：

```bash
go run ./cmd/build-claw -o claw

# 显式选择构建配置文件
go run ./cmd/build-claw -env-file /绝对路径/build.env -o claw
```

构建入口按白名单读取 `HTTPS_PROXY`、`HTTP_PROXY`、`NO_PROXY`、`OPENAI_BASE_URL`、`ZHIPU_BASE_URL` 和 `DEEPSEEK_BASE_URL`，构建进程环境变量优先于配置文件，进程中显式空值也优先。默认配置文件为当前目录 `.env`，仅将有值的配置作为构建默认值写入。代理变量同时支持小写别名。

运行时环境变量或启动目录的 `.env` 优先于构建默认值。代理变量的大写和小写形式均受支持；显式空值（例如 `HTTPS_PROXY=`）可关闭对应构建默认值。将渠道端点显式设为空值（例如 `OPENAI_BASE_URL=`）可恢复该渠道的默认端点。默认值会设置为进程环境变量，也会被 Bash 子进程和同进程中的其他 HTTP 客户端使用。

`OPENAI_API_KEY`、`ZHIPU_API_KEY`、`DEEPSEEK_API_KEY` 等密钥由运行时环境或 `.env` 提供。普通 `go build` 使用空的构建默认值。手动构建可通过 `-ldflags -X` 设置 `internal/runtimeenv` 包的 `DefaultHTTPSProxy`、`DefaultHTTPProxy`、`DefaultNoProxy`、`DefaultOpenAIBaseURL`、`DefaultZhipuBaseURL` 和 `DefaultDeepseekBaseURL`，同样适用于 Bench 和飞书入口。

```bash
go build -o claw ./cmd/claw
./claw -dir ./workspace -mode readonly -prompt "阅读代码并总结入口"
./claw -dir ./workspace -mode readonly -analysis -prompt "分析 Markdown 文档，引用具体原文并区分事实与推断"
./claw -dir ./workspace -prompt "修复 README 中的错别字"
./claw -dir ./workspace -read-page-bytes 16000 -prompt "读取并整理资料"
./claw -dir ./workspace -report 优化汇总.md -prompt "汇总优化建议，不修改原始资料"
./claw -dir ./workspace -mode trusted -plan -prompt "修复解析器并验证" -verify 'go test ./internal/parser'
```

| 模式 | 权限 |
| --- | --- |
| `readonly` | 工作区内读取、列目录、搜索与只读委派。 |
| `workspace`（CLI 默认） | 额外允许修改工作区文件，改动使用读取时取得的文件版本。 |
| `trusted` | 额外开放 Bash 和用户指定的验收命令，命令以当前用户权限运行。 |

未指定 `-task-kind` 时按执行模式推断：`readonly` 为回答任务，`workspace` 和 `trusted` 为修改任务。仅需阅读和回答时可指定 `-mode readonly`。

留空 `-prompt` 进入 REPL。支持 `/help`、`/exit`、`/cost`、`/status`、`/clear`、`/verify <命令>` 和 `/resume <任务 ID>`。独占一行的三个双引号开始或提交多行输入。`/verify` 设置下一次提交的验收命令。修改任务缺少验收时返回 `NEEDS_VERIFICATION`，验收命令成功且工作区版本一致时返回 `VERIFIED`。

`-max-turns`、`-max-model-calls`、`-max-tool-calls`、`-timeout` 默认分别为 30、60、120、10 分钟。主任务与子任务共享调用次数和时间。使用 `-help` 查看全部参数。

模型请求默认记录有界参数和输入来源元数据，不保存正文。需要排查新失败时，可在本次启动显式加入 `-request-diagnostics on_error`，把失败请求和同组上一成功请求的脱敏副本仅保存到本地 `~/.claw/request-diagnostics/`；`-request-diagnostics off` 可关闭。环境变量、限额、错误分类和清理方式见 [请求诊断说明](docs/request-diagnostics-v1.md)。观测台只展示摘要，不提供正文读取；历史未记录的请求不能补造。

新任务默认启用有来源的读取记忆：普通写入也能获得读取覆盖、可核对笔记和带目的复核。`-read-page-bytes` 可选择 `8000`（默认）或 `16000`，同时控制读取默认页长、最大源字节及编号正文上限；它不增加调用额度或上下文硬上限。恢复沿用持久档位，历史任务不自动升级读取资格。

`-report <相对路径.md>` 显式启用证据报告，仅支持 `workspace + change`。目标父目录必须存在，目标文件不能已存在；该任务只允许读取和 `write_report`，不能修改原始资料。模型提交结构化报告，引擎检查引用并统一渲染、写入和交付，不再追加模型复述。引用通过不代表结论正确，成功发布仍为 `NEEDS_VERIFICATION`。配置、恢复及测试边界见 [写入任务效率与证据报告](docs/write-efficiency-v1.md)。

## 任务状态与恢复

任务记录写入工作区的 `.claw/tasks/<任务 ID>/`，包含事件日志、检查点及工具材料。开启 `-plan` 后，任务目录额外生成可读的 `PLAN.md` 和 `TODO.md`。追踪按运行 ID 写入 `.claw/traces/`。

```bash
./claw -dir ./workspace -mode trusted -resume <任务ID> -timeout 15m
```

恢复沿用原任务、会话、验收命令及已消耗额度，权限受本次运行和原任务权限约束。文件动作中断后会核对版本，已确认的效果保留。通用命令的效果无法确认时，任务保持阻塞。已验证任务重新读取时会核对历史证据对应的工作区版本。

模型使用量从 Provider 回执统计。可通过 `CLAW_MODEL_PRICES` 配置人民币单价，例如 `{"模型名":{"currency":"CNY","input_per_million":1,"output_per_million":2}}`。缺少单价或用量时显示未知。

引擎在每次模型请求中提供共享预算快照。任务退出后，CLI 和飞书统一展示最终状态、已确认的动作效果与验收缺口。预算耗尽保留原失败终态，已写文件通过 `Delivery` 回执交付；回执表示动作当时的确认，不能代替当前文件检查或验收。旧任务没有完整效果账本时会显示历史缺口。

工具额度用尽后，根 Agent 可以在剩余模型、轮次和时间内进行一次禁工具收尾，计入一轮和一次模型调用，不预留或增加额度。部分分析通过 `PartialReport` 单独交付，保留 `BUDGET_EXHAUSTED` 和退出码 3；无额度或收尾失败时使用已记录事实生成摘要。同一工具额度边界恢复时不重复调用收尾模型。CLI、飞书和观测台均将其标为“部分分析／任务未完成”，不替代最终答复或验收。

父 Agent 可复用同次运行内已检查结构及引用的子报告、读取覆盖范围和缺口。覆盖目录按权限与当前文件版本筛选，不缓存工具执行、不减免工具尝试次数。结构及引用检查不等于结论已验收。详细契约与离线夹具见 [任务分析链路修复说明](docs/task-analysis-repair.md)。

源码引用可使用 `read_file` 的 `line_numbers: true` 参数。行号仅用于显示，文件版本和分页 offset 仍按源文件字节计算。

### 显式分析模式

`-analysis` 启用带结构化证据引用的分析报告，须同时指定 `-mode readonly`，仅支持 `readonly + answer`，不能配置验收命令。普通问答不会按目标关键词自动启用。REPL 启动时的选择用于后续新任务；`/resume` 和 `-resume` 沿用原任务的分析设置。

分析报告分别展示材料事实、分析推断、建议和待核实项。重要结论须引用本次有效读取的原文，校验文件版本、实际读取范围、行号和摘录；部分无效引用降级为待核实，全部核心结论无有效依据或格式无效时返回 `BLOCKED/analysis_report_invalid`，不自动请求模型修复。`ANSWERED` 仅表示报告已经答复，引用通过也不代表结论经过独立语义验收。

分析上下文优先去除 `Progress` 正文副本，并在已完整交付、模型声明处理且保存笔记后折叠旧读取内容。软目标为 96 KiB，既有 `-context-bytes` 硬上限保持有效；不能安全压缩时允许超过软目标。该机制不增加独立摘要、评审或格式修复模型调用，必要复读仍计入工具预算。

`read_file` 支持成对的 `start_line/end_line`（从 1 开始、两端包含），与显式 `offset/limit` 互斥；行模式默认编号。不要把搜索返回的行号直接用作字节 offset。启用、报告格式、飞书及 Bench 配置和记录保留规则见 [分析模式说明](docs/analysis-mode.md)。

## 构建与测试

```bash
go build ./cmd/... ./internal/...
go test ./cmd/... ./internal/...
go test -race ./internal/...
```

`workspace/` 是演示目录，可能包含空的 Go 草稿文件。请使用上述包范围。真实评测由 `go run ./cmd/bench` 显式启动，会调用模型并产生费用。它复用 CLI 的任务和验收机制。Bench 的 `-provider auto` 默认按模型前缀选择渠道，`glm-*` 使用 Zhipu，`deepseek*` 使用 Deepseek，其余使用 OpenAI；也可用 `-provider` 显式选择渠道。

独立真实任务对照使用 `scripts/real_eval/run.py`，先执行 `selfcheck`，再通过 `run` 指定旧、新二进制，最后用 `summarize` 重建报告。该套件保持 readonly/workspace 权限，外部 oracle 独立评分，结果保留在 `.claw/evals/`。`run` 会调用真实模型并产生费用，默认先运行 W02/W03 达到门槛后再扩展。完整行为和验收门槛见 [修复 Spec](docs/real-task-repair-spec.md)。

2026-11-05 的 v2 对照完成 30 个样本，新版 15/15、旧版 8/15，详见 [修复与评估结果](docs/real-task-repair-results-2026-11-05.md)。

## 本地任务观测台

```bash
go build -o claw-monitor ./cmd/claw-monitor
./claw-monitor -workspace /绝对路径/工作区
```

打开终端打印的本机地址，可查看任务历史、实时模型/工具/审批阶段、文件效果、验收与异常。支持重复传入 `-workspace` 和 `-eval-root`。页面只读，不发起任务或模型请求；新增运行的轻量观测默认写入 `~/.claw/observations/`，可通过 `CLAW_OBSERVE=off` 关闭。

接口、日志保留、隐私与诊断边界见 [观测台使用与实现说明](docs/local-monitor.md)。

## 飞书入口

`cmd/agentops` 是独立服务。配置 `DEEPSEEK_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_VERIFY_TOKEN`、`FEISHU_ENCRYPT_KEY`，并将 `FEISHU_AGENTOPS_CONFIG` 指向配置文件。

飞书入口默认使用 Deepseek 的 `deepseek-v4.1-flash-1017`。可通过 `CLAW_PROVIDER` 显式指定渠道，或通过 `CLAW_MODEL` 指定模型并自动匹配渠道，同时配置该渠道的 API 密钥和可选 Base URL。

```json
{
  "event_ledger": "/绝对路径/事件台账",
  "chats": {
    "oc_群聊ID": {
      "work_dir": "/绝对工作区",
      "allowed_users": ["ou_发起者"],
      "approvers": ["ou_审批者"],
      "mode": "trusted",
      "verify_command": "go test ./cmd/... ./internal/..."
    }
  }
}
```

服务默认监听 `127.0.0.1:48080`，Webhook 路径为 `/webhook/event`，可用 `FEISHU_LISTEN_ADDR` 调整。群聊按序提交任务。文件写入和本机命令逐次审批，审批限定到请求中的群聊、人员、动作和版本。

## 扩展入口

新工具实现 `internal/tools.BaseTool`，在对应 Registry 注册并配置可见权限。执行结果携带原 `ToolCallID` 回传给模型。新 Provider 实现 `internal/provider.LLMProvider`，并由入口装配。工具附加准入条件通过 `Registry.Use` 注册。

个人学习项目，采用 MIT 许可。

## 只读效率策略

新建 `readonly + answer` 默认使用 `-delegation auto`：小范围材料由根 Agent 执行；可显式设为 `off` 或 `on`。严格引用报告仍由 `-analysis` 单独启用。新增 `read_lines`、带目的复核、引擎计算读取覆盖，以及完整 JSON 的子报告目录和按发现读取。

启动方式、32 文件/128 KiB 阈值、子任务局部预算及验证边界见 [只读任务执行效率 v2](docs/readonly-efficiency-v2.md)。

## 显式质量核验

`-quality` 默认关闭，首版仅与 `-mode readonly -analysis` 或 `-mode workspace -report 报告.md` 组合。重要发现先登记证据票据，进行受限算术／明确 JSON 核验，按风险最多复核一次，再由根 Agent 修订／提交一次；全部计入原预算。普通任务保持原行为，内部通过不代表独立语义或运行验收。

接口、状态版本 2、恢复与隐私边界及 CLI／观测台同步更新和重启说明见 [质量优先 v1](docs/quality-first-v1.md)。
