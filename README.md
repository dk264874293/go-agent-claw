# go-agent-claw

go-agent-claw 是用 Go 编写的 ReAct 编码助手。模型提出工具调用，引擎管理执行权限、预算、任务进度与验收。支持 CLI、离线回归、真实模型评测和独立的飞书入口。

## 快速开始

支持 macOS、Linux，需要 Go 1.25。CLI、基准评测和独立飞书服务默认使用 OpenAI 的 `gpt-6.1-terra`，从环境变量或 `.env` 读取 `OPENAI_API_KEY`。CLI 可用 `-model` 显式选择模型；本轮真实对照套件固定使用 `gpt-6.1-sol`。

默认通过 OpenAI 官方端点 `https://api.openai.com/v1/` 调用 Responses API，使用 OpenAI API 密钥。需要使用兼容端点时，可显式设置 `OPENAI_BASE_URL`。

```bash
go build -o claw ./cmd/claw
./claw -dir ./workspace -prompt "阅读代码并总结入口"
./claw -dir ./workspace -mode workspace -prompt "修复 README 中的错别字"
./claw -dir ./workspace -mode trusted -plan -prompt "修复解析器并验证" -verify 'go test ./internal/parser'
```

| 模式 | 权限 |
| --- | --- |
| `readonly`（默认） | 工作区内读取、列目录、搜索与只读委派。 |
| `workspace` | 额外允许修改工作区文件，改动使用读取时取得的文件版本。 |
| `trusted` | 额外开放 Bash 和用户指定的验收命令，命令以当前用户权限运行。 |

留空 `-prompt` 进入 REPL。支持 `/help`、`/exit`、`/cost`、`/status`、`/clear`、`/verify <命令>` 和 `/resume <任务 ID>`。独占一行的三个双引号开始或提交多行输入。`/verify` 设置下一次提交的验收命令。修改任务缺少验收时返回 `NEEDS_VERIFICATION`，验收命令成功且工作区版本一致时返回 `VERIFIED`。

`-max-turns`、`-max-model-calls`、`-max-tool-calls`、`-timeout` 默认分别为 30、60、120、10 分钟。主任务与子任务共享调用次数和时间。使用 `-help` 查看全部参数。

## 任务状态与恢复

任务记录写入工作区的 `.claw/tasks/<任务 ID>/`，包含事件日志、检查点及工具材料。开启 `-plan` 后，任务目录额外生成可读的 `PLAN.md` 和 `TODO.md`。追踪按运行 ID 写入 `.claw/traces/`。

```bash
./claw -dir ./workspace -mode trusted -resume <任务ID> -timeout 15m
```

恢复沿用原任务、会话、验收命令及已消耗额度，权限受本次运行和原任务权限约束。文件动作中断后会核对版本，已确认的效果保留。通用命令的效果无法确认时，任务保持阻塞。已验证任务重新读取时会核对历史证据对应的工作区版本。

模型使用量从 Provider 回执统计。可通过 `CLAW_MODEL_PRICES` 配置人民币单价，例如 `{"模型名":{"currency":"CNY","input_per_million":1,"output_per_million":2}}`。缺少单价或用量时显示未知。

引擎在每次模型请求中提供共享预算快照。任务退出后，CLI 和飞书统一展示最终状态、已确认的动作效果与验收缺口。预算耗尽保留原失败终态，已写文件通过 `Delivery` 回执交付；回执表示动作当时的确认，不能代替当前文件检查或验收。旧任务没有完整效果账本时会显示历史缺口。

源码引用可使用 `read_file` 的 `line_numbers: true` 参数。行号仅用于显示，文件版本和分页 offset 仍按源文件字节计算。

## 构建与测试

```bash
go build ./cmd/... ./internal/...
go test ./cmd/... ./internal/...
go test -race ./internal/...
```

`workspace/` 是演示目录，可能包含空的 Go 草稿文件。请使用上述包范围。真实评测由 `go run ./cmd/bench` 显式启动，会调用模型并产生费用。它复用 CLI 的任务和验收机制。指定 `-model glm-4.7` 等 `glm-*` 模型时，评测使用智谱和 `ZHIPU_API_KEY`。

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

`cmd/agentops` 是独立服务。配置 `OPENAI_API_KEY`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_VERIFY_TOKEN`、`FEISHU_ENCRYPT_KEY`，并将 `FEISHU_AGENTOPS_CONFIG` 指向配置文件。

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
