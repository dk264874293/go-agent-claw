# go-agent-claw 真实模型与飞书联调记录

2026 年 11 月 4 日，Asia/Shanghai。按[功能测试用例](functional-test-cases.md)执行联调。更新密钥并修复工具参数定义后，**11 条真实模型用例全部通过**，3 条飞书用例仍缺测试配置。[本地自动化测试](functional-test-results-2026-11-04-automated.md)的 47 条用例已通过。

环境为 macOS arm64、Go 1.26.5，分支 `feture/03`，HEAD `926b56a`，使用当前工作区构建的 CLI 和 Bench。真实模型配置为 `gpt-6.1-sol`，请求目标为官方 `https://api.openai.com/v1/responses`。每次 CLI-07 均使用独立临时夹具，`status.txt` 初始内容为 `draft` 加换行。

## 密钥更新前

首轮执行 CLI-07 失败，其余 13 条因环境条件阻塞。

| 尝试 | 实际结果 | 任务 ID | 运行 ID |
| --- | --- | --- | --- |
| 项目 `.env`，直连 | `FAILED`，退出 `1`。连接超时，未收到 HTTP 响应。 | `92fe401956728973a03289cbe1dda0c7` | `adfdb9f84037d33d30d0720e3321aa3b` |
| 项目 `.env`，通过系统代理 | `FAILED`，退出 `1`。官方 API 返回 `401 invalid_api_key`。 | `bcf04efbe818bc6b240ded2e0682021b` | `14af2d0aa6f1e4e6b8a2c894cbbbf0eb` |
| 进程环境中的 `OPENAI_API_KEY`，通过系统代理 | `FAILED`，退出 `1`。官方 API 返回 `401 invalid_api_key`。 | `b9019070933bd7be1f5fd4db02060813` | `e9d38ed1d25e7074fddb9c146c9b059f` |

每次追踪均记录 1 次模型请求尝试、0 次工具调用，`status.txt` 字节与初始内容一致。用例期望的 `ANSWERED/0` 和实际 `read_file` 调用均未达到。系统代理仅通过测试子进程的 `HTTPS_PROXY` 使用，API 目标仍为官方网关。第三次从临时目录启动，以避免 CLI 加载项目 `.env` 时覆盖进程环境中的密钥。

### 首轮未执行用例

| 用例 | 阻塞原因 |
| --- | --- |
| CLI-06、CLI-08 | 官方 API 认证失败，无法验证多行输入的模型回复或会话用量。 |
| RUN-07、RUN-08 | 官方 API 认证失败，无法执行真实模型 Thinking 阶段或进入验收阶段。 |
| ACC-05、ACC-06 | 官方 API 认证失败，无法执行模型驱动的夹具修改与验收。 |
| STATE-09 | 需要 ACC-05 生成 `VERIFIED` 任务及其验收证据。 |
| BENCH-01、BENCH-02、BENCH-03 | 官方 API 认证失败，未启动真实模型 Bench。 |
| FS-07、FS-08、FS-09 | 未设置 `FEISHU_AGENTOPS_CONFIG`，无法确认测试群、独立工作区、台账及回调。飞书服务未启动。 |

## 密钥更新后

CLI-07 首次复测通过认证，官方 API 随后返回 `400 invalid_function_parameters`。错误指向 `list_dir` 参数定义中的 `required: null`。修复共享工具定义后，无必填字段的工具省略 `required`，有必填字段的工具仍发送原必填字段列表。使用实际 `list_dir` 定义的 Responses HTTP 回归测试通过，`go test -race -count=1 ./cmd/... ./internal/...` 退出 `0`。

| 用例 | 实际结果 | 证据 |
| --- | --- | --- |
| CLI-06 | 通过。多行输入只提交一次，正文中的 `/help` 和 `exit` 原样回显。任务 `ANSWERED`，模型调用 1 次、工具调用 0 次，单独的 `/exit` 使进程退出 `0`。 | 运行 `6933bff30b7e2291d61268b9f74e4894`。 |
| CLI-07 | 通过。`ANSWERED/0`，2 次模型调用、1 次成功的 `read_file`。原样返回 `draft`，夹具字节一致。API 回执为 1,360 输入、25 输出 Token。 | 运行 `c99c79804929a110f0dba5d782b73918`，工具 `call_AiO1Oro8bS8tiKS2CI5UnDaP`。 |
| CLI-08 | 通过。读取任务 `ANSWERED`，2 次模型调用、1 次成功的 `read_file`。`/cost` 显示上一轮与累计用量，`/clear` 后历史从 4 条变为 0 条。累计 1,332 输入、29 输出 Token 和任务记录保留。 | 运行 `3c3db7936bae19d051c651bd8eb157ef`。 |
| RUN-07 | 通过。Thinking 成功用掉唯一模型调用，工具调用 0 次。Action 被预算拦截，`BUDGET_EXHAUSTED/3`，停止代码 `model_limit`。 | 运行 `5a0375916af3b2755833bab4071ea574`，Thinking 用量 156 输入、7 输出 Token。 |
| RUN-08 | 通过。验收命令启动后发送 SIGINT，返回 `CANCELLED/130`，Bash 动作记录为 `CANCELLED/UNKNOWN`，恢复返回 `BLOCKED/2` 且未重放。另用 500 ms 时限得到 `BUDGET_EXHAUSTED/3`，停止代码 `deadline`。 | 取消 `a233545761d36518c3e8fe4a27a43499`，恢复 `11fdbd66b7da8ff6e9999b709271360d`，短时限 `d5f18b2de0102655f1eec9797651b846`。 |
| ACC-05 | 通过。两个夹具均写为 `ready` 并保留换行。`workspace` 为 `NEEDS_VERIFICATION/2`，`trusted` 为 `VERIFIED/0`，验收执行一次。 | 运行 `7ea37cd6e484b241c1395bb96e5b215a`、`57f7a8ee7bb30d284614818f39b27347`。 |
| ACC-06 | 通过。REPL 退出 `0`，两次修改均落盘。第一个任务 `VERIFIED`，验收调用 1 次。第二个任务 `NEEDS_VERIFICATION`，验收调用 0 次。 | 运行 `6d887b49e9174cd8f331afa1c6dd8600`、`5d4ba987143c146c89aa1aff83f51ea6`。 |
| STATE-09 | 通过。外部修改可信模式夹具后，恢复返回 `NEEDS_VERIFICATION/2`，提示原验收证据失效。未创建新追踪，夹具保留外部修改。 | 恢复任务 `ba61950078361ee168429f4e2ad9ca5a`。 |
| BENCH-01 | 通过。`test_001_edit` 为 `VERIFIED`。验收核对 `config.json` 精确修改且无多余普通文件。 | 7,539 ms，模型 3 次，工具 3 次。 |
| BENCH-02 | 通过。`test_002_code_gen` 为 `VERIFIED`。验收确认 `math_test.go` 存在，且指定的 `TestMultiply` 实际执行并通过。 | 15,275 ms，模型 5 次，工具 6 次。 |
| BENCH-03 | 通过。逐项摘要含状态、耗时、调用次数与费用，两个用例均显示费用「未知」。 | `评测通过 2/2`，Bench 退出 `0`。 |
| FS-07、FS-08、FS-09 | 阻塞。仍缺 `FEISHU_AGENTOPS_CONFIG`，无法确认独立测试群、工作区、台账及回调。 | 项目 `.env`、进程环境和仓库配置检查。 |

CLI-08 的检查脚本最初只接受裸文本，因回复把 `draft` 放在 Markdown 代码块中而误报。复核原始回复和读取追踪后修正判定。
