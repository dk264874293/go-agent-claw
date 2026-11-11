[返回首页](../README.md) · [文档导航](README.md)

# 扩展、测试与交流

建议从一个小工具或一个可复现的问题开始，先理解当前行为，再修改并验证。项目中的注释、日志和用户提示保持中文。

## 离线构建与回归

```bash
go build -o claw ./cmd/claw
go build ./cmd/... ./internal/...
go test ./cmd/... ./internal/...
# 共享预算、账本、存储或审批改动
go test -race ./internal/...
```

演示目录 `workspace/` 可能有空 Go 草稿，不使用 `go build ./...` 或 `go test ./...`。常规单元测试使用本地回放 / 测试服务；真实模型评测是下面单独的显式入口。

### 干净检出与本机工作区

本机目录可能包含被 Git 忽略的夹具、密钥或未提交依赖；本机测试通过不能证明其他人能复现。引擎所需的两套[合成夹具](../internal/engine/testdata/README.md)已纳入源码范围，macOS 元数据继续排除。

```bash
# Python 3.12+；默认只导出 HEAD，运行构建、无缓存全量测试和内部 race
python3 -B scripts/check_clean_checkout.py
# 单独检查一个阶段
python3 -B scripts/check_clean_checkout.py --check test
# 提交前，只将明确的候选文件覆盖到已提交源码上，可重复 --overlay
python3 -B scripts/check_clean_checkout.py --check build --overlay cmd/learn --overlay internal/learning
python3 -B -m unittest discover -s scripts -p test_clean_checkout.py
```

候选模式不会复制无关的未提交文件；新增依赖必须一并明确列入。它支持新增和修改，不支持删除覆盖。需要验证删除时先形成提交，再用 `--revision` 导出该提交；未提交的删除不会被静默视为已验证。隔离目录不包含 `.git` 或本机 `.env`，模型密钥及 CLAW 配置变量从子进程环境移除。检查只访问本地回放 / HTTP 测试服务；Go 依赖下载可能联网。

[GitHub Actions](../.github/workflows/offline.yml)按 `go.mod` 选择 Go，分别运行构建、普通测试与 race，并补充 macOS 测试。只有远端实际执行后才能称 CI 通过；本地隔离检查不冒充远端结果。

### 无密钥教学回放

`go run ./cmd/learn -list` 列出课程，`go run ./cmd/learn -scenario react` 运行最小循环。所有候选由脚本 Provider 提出，工具与状态使用正式引擎。错误轨迹会失败，预期阻塞也保留真实终态；详细说明见[课程目录](learning/README.md)。

## 新增工具

1. 在 `internal/tools/` 实现 `BaseTool`：`Name()`、`Definition()`、`Execute(ctx, args)`。参数类型为 `json.RawMessage`，由工具自己解析和验证。
2. 在入口或引擎装配的 Registry 注册，核对模型可见性与 ExecutionScope 准入。供子任务使用的工具还须在只读 Registry 中装配。
3. 有附加准入规则时用 `Registry.Use(...)` 注册中间件，拒绝理由应能帮助模型纠正调用。
4. 验证参数、路径、模式和失败结果；文件写入还要验证版本与未知效果边界。

参考：[Registry 与 BaseTool](../internal/tools/registry.go)、[read_lines](../internal/tools/read_lines.go)、[工具权限](../internal/tools/scope.go)、[Registry 装配](../internal/engine/execution.go)、[CLI 装配](../cmd/claw/main.go)。注册不是授权，不能只修改工具定义就假定它在所有模式可用。

完整接入点及可运行小工具见[工具扩展契约](extending-tools.md)。目前只读、变更和副作用有按工具名分类的规则；新增名称不会自动获得只读权限或持久动作恢复。

## 新增 Provider

实现 [LLMProvider](../internal/provider/interface.go) 的 `Generate(ctx, messages, tools)`，将渠道协议转换为 `schema.Message`、工具调用和实际用量。再为入口配置密钥、模型和端点，并在 [configured.go](../internal/provider/configured.go) 与对应启动入口接线。

重点核对普通回答、工具调用配对、失败响应、取消和用量缺失。更改 Responses 适配时还需保留 opaque 项及现有传输约定。可参考现有 Provider 的本地 HTTP 编码测试，避免为协议排查增加真实调用。

## 真实模型评测

```bash
# 显式启动，会请求真实模型并产生费用
go run ./cmd/bench
# 指定渠道和模型
go run ./cmd/bench -provider deepseek -model deepseek-v4.1-flash-1017
```

Bench 复用任务和验收机制，使用 SetupScript / TaskPrompt / ValidateScript 描述用例。`internal/eval/benchmark.go` 的包名为 `observability`，引用时注意该历史命名。

独立对照套件先进行离线 `selfcheck`，再按照对应 README 运行真实实验。不要直接把自检通过理解为真实质量或效率提升。

| 套件 | 用途与说明 |
| --- | --- |
| [真实任务 v2](../eval/real/v2/README.md) | 运行器为 `scripts/real_eval/run.py`，对比旧 / 新二进制及独立 oracle |
| [只读效率](../scripts/efficiency_eval/README.md) | 阅读与上下文效率实验 |
| [覆盖效率](../scripts/coverage_efficiency_eval/README.md) | 范围与完整性实验 |
| [写入与报告](../scripts/write_efficiency_eval/README.md) | 写入效率、证据报告实验 |
| [工作笔记](../scripts/work_notes_eval/README.md) | 笔记采纳、材料与 Token 统计 |
| [来源复用](../scripts/source_reuse_eval/README.md) | 来源视图与局部核对历史实验 |
| [独立读取实验](../scripts/read_features_eval/README.md) | 三项显式读取开关的对照 |
| [质量核验](../scripts/quality_first_eval/README.md) | 分析 / 报告质量流程 |
| [交付前处理](../scripts/delivery_review_eval/README.md) | 普通任务一次定向处理 |

真实任务 v2 先执行 `python3 scripts/real_eval/run.py selfcheck`，再用 `run` 指定基线和候选，最后 `summarize` 重建结果；实际参数与门槛以套件说明和[修复 Spec](real-task-repair-spec.md)为准。真实对照保持指定模型、相同题面、独立工作区和相同预算，基础设施失败、失败样本消耗及未知费用也应保留。

## 如何提出一个便于交流的问题

附上 Go 版本、启动命令、渠道与模型名、执行模式、实际终态、预期行为和最小复现。必要时提供脱敏的任务记录或诊断摘要。不要提交 `.env`、密钥或私有资料。

讨论效果时，区分“代码机制已实现”“离线验证通过”“模型实际采纳”“语义质量达标”和“同等质量下成本下降”。历史小样本只说明对应条件，具体证据见[实验记录索引](README.md#机制与实验记录)。
