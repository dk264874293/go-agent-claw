[返回首页](../README.md) · [文档导航](README.md)

# 上手指南

目标是先观察一次无密钥回放，再完成真实只读问答、体验写文件并观察任务记录。所有命令默认在仓库根目录执行。

## 准备环境

推荐 macOS / Linux，安装 Git 和 Go 1.25 或更新版本，用 `go version` 检查版本。

```bash
git clone https://github.com/dk264874293/go-agent-claw.git
cd go-agent-claw
```

## 先运行无密钥回放

```bash
go run ./cmd/learn -scenario react
go run ./cmd/learn -list
```

无需 `.env` 或 API Key。脚本 Provider 产生候选，正式引擎完成真实工具读取、观察回传和停止；工作区是自动创建并删除的临时目录。首次下载 Go 依赖可能需要联网。输出与七个课程的说明见[学习入口](learning/README.md)。

## 配置真实模型

```bash
cp -n env.export .env
```

编辑 `.env`，填写 `DEEPSEEK_API_KEY`。模板中的默认密钥为空，其余均为可选注释；只读任务也会请求模型 API。默认模型为 `deepseek-v4.1-flash-1017`，可通过[配置指南](configuration.md)改用其他模型。

CLI 已自动加载 `.env`，无需额外执行 `source`。模板可直接被 Shell 解析，如果希望其他命令也使用配置，可以在编辑后执行 `source ./.env`；导出的变量仅影响当前终端及其子进程。

## 第一次真实任务

```bash
go run ./cmd/claw -mode readonly -prompt "只阅读 README.md，概括项目用途"
```

终端会显示工作区、工具进度、最终结果和用量。先观察模型提出什么工具、读取了哪些文件、最后是否回答了问题。

不带 `-prompt` 进入交互模式：

```bash
go run ./cmd/claw -mode readonly
```

可继续追问“请进一步解释工具 Registry 的作用”。用 `/status` 查看会话状态、`/cost` 查看用量、`/exit` 退出。完整命令见 [CLI](cli.md)。

## 第一次修改文件

```bash
go build -o claw ./cmd/claw
mkdir -p workspace
./claw -dir ./workspace -prompt "创建 hello.md，写三条 Go 学习建议"
```

`workspace` 模式允许读写这个目录，默认不开 Bash。写入成功但没有运行验收时，任务可能以 `NEEDS_VERIFICATION` 和退出码 2 结束；可查看 `workspace/hello.md` 确认内容。

任务记录在 `workspace/.claw/tasks/`，Trace 在 `workspace/.claw/traces/`。学习时可另外打开[本地观测台](observability.md)，查看同一个任务的执行过程。

## 常见问题

| 现象 | 检查方式 |
| --- | --- |
| 提示缺少 API Key | 确认 `.env` 位于启动目录、已填写当前渠道对应的密钥，并重启 CLI |
| 改了环境变量却没有生效 | CLI 和飞书的 `.env` 会覆盖同名进程变量；检查是否仍有旧值 |
| 找不到文件或目录 | `-dir` 必须指向已有目录；工具路径相对该工作区，`.env` 仍从启动目录加载 |
| 编译提示 Go 版本过旧 | 对照 `go.mod` 使用 Go 1.25 或更新版本 |
| `workspace/main.go` 编译报错 | 使用 `go build ./cmd/... ./internal/...`，演示目录可能包含空草稿 |
| 不能执行命令 | Bash 和 `-verify` 需要显式 `-mode trusted` |
| 模型返回认证、端点或模型错误 | 核对密钥、渠道和模型是否匹配，自定义 Base URL 是否包含 API 路径；参见[配置](configuration.md) |
| 任务提示预算耗尽 | 查看已消耗次数和部分交付，再按[恢复说明](tasks-and-recovery.md)决定是否继续 |
| `.env` 无法加载 | 检查语法、读取权限或符号链接是否断链；已有文件损坏会停止启动 |

不调用真实模型也能学习：使用 `go run ./cmd/learn -scenario budget` 等课程、`go run ./cmd/claw -help` 查看参数，或按[开发指南](development.md)运行离线测试。
