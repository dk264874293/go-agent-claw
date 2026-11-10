[返回首页](../README.md) · [文档导航](README.md)

# 预算、成本与链路观测

观测的用途是回答三个问题：任务现在在做什么、已经消耗多少、哪些结果有执行依据。

## 共享预算

默认预算为 30 轮、60 次模型请求、120 次工具尝试和 10 分钟累计运行时间。主任务、计划、子任务、额外思考和显式核验阶段按实际行为共享预算；被拒绝的工具尝试也计数。

```bash
./claw -mode readonly -max-turns 10 -max-model-calls 15 \
  -max-tool-calls 30 -timeout 3m -prompt "阅读 README.md，概括项目用途"
```

引擎在模型请求中提供剩余额度快照。耗尽后保留已发生效果与部分交付，但不会将未完成任务标为成功。继续执行见[任务恢复](tasks-and-recovery.md)。

## Token 和费用

`CostTracker` 包装 Provider，依据响应中的实际用量统计 Token。REPL 用 `/cost` 查看累计值，任务结束也会输出用量。缺少用量或单价时显示未知，不记作零费用。

可在 `.env` 中配置人民币单价，下面只是结构示例，需替换模型名和实际单价：

```bash
export CLAW_MODEL_PRICES='{"模型名":{"currency":"CNY","input_per_million":1,"output_per_million":2}}'
```

每百万输入和输出 Token 分别计价。`/clear` 清空上下文但保留已发生的累计费用。配置的费用估算与模型服务商最终账单可能有差异。

## 本地任务观测台

在另一个终端的项目根目录执行：

```bash
go build -o claw-monitor ./cmd/claw-monitor
./claw-monitor -workspace "$(pwd)"
# 若任务运行在 workspace/ 中，改为登记该目录
./claw-monitor -workspace "$(pwd)/workspace"
```

打开终端打印的本机地址，可查看历史任务、模型 / 工具 / 审批阶段、交付、文件效果、验收和异常。`-workspace` 和 `-eval-root` 可重复传入，目录必须为绝对路径。页面只读，不提交新任务或请求模型。

运行时的轻量观测默认写入 `~/.claw/observations/`。启动任务时设置 `CLAW_OBSERVE=off` 可关闭，或用 `CLAW_OBSERVE_DIR` 指定目录；观测台用 `-observe-dir` 读取自定义位置。观测台本身不自动加载 `.env`，如需使用其中的环境变量，先 `source ./.env` 或直接传参数。

协议、访问边界和清理方式见[观测台详细说明](local-monitor.md)。工作区 `.claw/traces/` 的 Trace 与全局轻量观测是不同记录，可结合阅读定位耗时阶段。

## 请求诊断

模型请求默认 `metadata`，只记录有界参数与输入来源元数据。排查新失败时显式启用：

```bash
./claw -mode readonly -request-diagnostics on_error \
  -prompt "阅读 README.md，概括项目用途"
```

失败请求及同组上一成功请求的脱敏副本仅保存在本地 `~/.claw/request-diagnostics/`；可用 `-request-diagnostics-dir` 或 `CLAW_REQUEST_DIAGNOSTICS_DIR` 改目录。`-request-diagnostics off` 关闭诊断。历史未记录的请求不能补造，观测台仅展示摘要，不提供这些正文。

诊断不会为记录额外请求或重试模型。限额、错误分类和保留规则见[请求诊断](request-diagnostics-v1.md)。

源码入口：[成本统计](../internal/observability/tracker.go)、[观测台入口](../cmd/claw-monitor/main.go)、[请求诊断接线](../internal/engine/request_diagnostics.go)。
