[返回首页](../README.md) · [文档导航](README.md)

# 模型与环境配置

## 使用 env.export

仓库提供 [env.export](../env.export) 作为公开模板，支持 `.env` 的 `export KEY="value"` 语法，也支持 Shell 的 `source`。

```bash
# 首次复制，不覆盖已有 .env
cp -n env.export .env
# 编辑 .env，填写实际密钥后启动
go run ./cmd/claw -mode readonly
```

最小配置只有一项：

```bash
export DEEPSEEK_API_KEY="替换为你的密钥"
```

`.env` 已被 Git 忽略；模板保留空值。可选渠道、代理、观测和飞书变量在模板中均为注释，按需启用。无需全部填写。

## 模型与渠道

CLI 通过命令行的 `-provider`、`-model` 选择模型，Bench 使用同名参数。飞书服务使用 `CLAW_PROVIDER`、`CLAW_MODEL`；这两个环境变量不用于 CLI 模型选择。

| 渠道 | API Key | 可选端点变量 | 代码中的默认端点 |
| --- | --- | --- | --- |
| DeepSeek | `DEEPSEEK_API_KEY` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` |
| OpenAI | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `https://api.openai.com/v1/` |
| 智谱 | `ZHIPU_API_KEY` | `ZHIPU_BASE_URL` | `https://open.bigmodel.cn/api/paas/v4/` |

`-provider auto` 是默认值：`deepseek*` 模型走 DeepSeek，`glm-*` 走智谱，其余走 OpenAI。不传模型和渠道时，使用 DeepSeek 的 `deepseek-v4.1-flash-1017`；显式 `-provider openai` 且省略模型时，使用项目配置的 `gpt-6.1-terra`。智谱渠道须显式提供其实际可用模型。

```bash
# 默认 DeepSeek
./claw -mode readonly
# OpenAI：先在 .env 配置 OPENAI_API_KEY
./claw -provider openai -mode readonly
# 按模型前缀自动选择渠道，模型须在你的账号或网关可用
./claw -model gpt-6.1-sol -mode readonly
# 智谱：将模型名称替换为实际可用的名称
./claw -provider zhipu -model 'glm-你的模型名称' -mode readonly
```

以上模型名表示仓库默认值和路由规则，实际可用性以所用账号或网关为准。显式 `-provider` 可以覆盖前缀路由。自定义网关端点应包含完整接口版本路径，例如 `https://gateway.example.com/api/paas/v4/`。端点未设置或为空时使用该渠道默认端点。

OpenAI 兼容适配还支持 `OPENAI_ORG_ID`、`OPENAI_PROJECT_ID` 和按行配置的 `OPENAI_CUSTOM_HEADERS`，按网关要求使用。入口见 [configured.go](../internal/provider/configured.go)、[openai.go](../internal/provider/openai.go)、[compatible.go](../internal/provider/compatible.go)。

## 启动目录与配置优先级

`.env` 按进程启动时的当前目录加载，`-dir` 只指定任务工作区。例如在仓库根目录运行 `./claw -dir ./workspace`，读取的是根目录的 `.env`。

| 入口 | 同名配置优先级（从高到低） |
| --- | --- |
| CLI / 飞书 | 启动目录 `.env` → 进程环境 → 构建默认值 → 渠道默认端点 |
| Bench | 进程环境 → 启动目录 `.env` → 构建默认值 → 渠道默认端点 |
| `build-claw` 构建配置 | 进程环境（显式空值也优先）→ 配置文件 |

若不创建 `.env`，可直接使用终端中的环境变量：

```bash
export DEEPSEEK_API_KEY="替换为你的密钥"
go run ./cmd/claw -mode readonly
```

需要导出已有本地配置时使用 `source ./.env`。从其他目录运行二进制时，可在启动目录建立指向配置的 `.env` 符号链接；`env.export` 本身不会被程序自动加载。已有 `.env` 不可读、格式错误或断链时停止启动，修改配置后需重启进程。

## 网络代理

按需在 `.env` 中取消代理配置注释，端口替换为本机实际值：

```bash
export HTTPS_PROXY="http://127.0.0.1:7891"
export HTTP_PROXY="http://127.0.0.1:7891"
export NO_PROXY="localhost,127.0.0.1,::1"
```

HTTPS 请求使用 `HTTPS_PROXY`，HTTP 请求使用 `HTTP_PROXY`，`NO_PROXY` 设置绕过地址；也支持小写别名。未配置时直连，修改后重启。变量也会影响 Bash 子进程和同进程中的其他 HTTP 客户端。

## 将网络默认值带入二进制

普通构建足以使用运行时 `.env`。需要把代理或端点作为二进制默认值时，可使用现有构建入口：

```bash
go run ./cmd/build-claw -o claw
# 或显式指定构建配置文件
go run ./cmd/build-claw -env-file /绝对路径/build.env -o claw
```

仅允许写入 `HTTPS_PROXY`、`HTTP_PROXY`、`NO_PROXY`、`OPENAI_BASE_URL`、`ZHIPU_BASE_URL`、`DEEPSEEK_BASE_URL` 的非空值，**不嵌入 API 密钥**。默认读取当前目录 `.env`，运行时 `.env` 或环境变量可覆盖构建默认值。

运行时显式 `HTTPS_PROXY=` 可关闭对应构建默认值；`OPENAI_BASE_URL=` 等空值恢复渠道默认端点。普通 `go build` 不注入这些默认值。手动构建时可通过 `-ldflags -X` 设置 [runtimeenv](../internal/runtimeenv/defaults.go) 中的 `DefaultHTTPSProxy`、`DefaultHTTPProxy`、`DefaultNoProxy`、`DefaultOpenAIBaseURL`、`DefaultZhipuBaseURL`、`DefaultDeepseekBaseURL`，同样适用于 Bench 和飞书入口。

观测、费用和请求诊断配置见[预算与观测](observability.md)，飞书所需变量见[飞书接入](feishu.md)。
