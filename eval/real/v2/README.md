# 真实任务评估 v2

本套件在独立临时工作区运行已构建 CLI，再由隔离的外部 oracle 验收。CLI 使用 readonly 或 workspace 权限，oracle 结果不回传模型，也不改变引擎终态。

## 入口

```bash
python3 scripts/real_eval/run.py selfcheck
python3 scripts/real_eval/run.py run --baseline-bin /绝对路径/旧claw --candidate-bin /绝对路径/新claw --candidate-source /绝对路径/源码归档.tar.gz
python3 scripts/real_eval/run.py summarize /绝对路径/实验目录
```

真实运行从进程环境读取 `OPENAI_API_KEY`，继承环境代理。凭据不写入 manifest；CLI 在没有项目 `.env` 的实验根目录启动。每次运行前自动执行全套离线自检，macOS 沙箱或 Go 依赖预检失败时停止，不发送模型请求。费用未知保留 `null`。

结果默认保存在仓库 `.claw/evals/<UTC时间>-<uuid>/`。`--output` 指定新目录；`--resume` 指定既有实验，仅执行尚未开始的样本，完成样本不重跑。已有未结束尝试须先核实，runner 不自动重试。`--stage first` 仅执行首阶段；缺省 `all` 仍受首阶段门槛控制。

续跑必须提供同样的二进制参数。suite、oracle、runner、二进制或其冻结副本摘要发生变化时拒绝续跑。候选源码归档须由构建方提供，仅包括实际构建输入，排除 `.env` 等凭据。旧基线只能承诺二进制摘要，不把 Git HEAD 当作可重建证明。

## 固定实验条件

固定模型 gpt-6.1-sol。runner 先通过 `-help` 检查模型参数，支持时显式传入 `-model`；旧版无参数时仅允许已核实为 sol 的二进制 SHA-256 映射，未知版本在付费前拒绝。模型控制方式写入 manifest。每个样本 8 次模型调用、20 次工具尝试、30 轮、120 秒，材料上限 300000 字节。旧新两组交错执行，每例各重复三次。

先执行 W02/W03 的 12 个样本。新实现每例至少 2/3 综合通过，零禁止路径变化、零虚假成功，才进入 R01/R02/W01 的 18 个样本。最多 30 个样本、240 次模型调用额度。基础设施异常停止批次，不自动重试。最终新实现至少 12/15 且每例至少 2/3。

## 结果协议

`result.json` 独立保存 artifact、scope、engine、environment、checker 五个维度，取值 pass/fail/error/not_run。`labels` 可同时记录范围失败和预算耗尽。`combined_pass` 要求全部维度通过、具有所要求的最终答复且没有未决动作。

R 类要求 ANSWERED/0；W 类没有引擎 verifier，要求 NEEDS_VERIFICATION/2。产物正确但预算耗尽仍然综合失败。终态读取唯一 session 对应的任务快照，必要时从完整事件恢复，不能从中文日志猜测。

评分在进程结束后复制候选工作区运行，评分器没有模型凭据且禁用网络。范围失败不跳过产物验收。评分器超时、沙箱错误和依赖错误单独记为 error，不能当模型能力失败。

每个实验保存 manifest、冻结 suite/runner/bin、可选源码归档、每次命令与题面、前后文件清单、工作区事件、最终答复、stdout/stderr、oracle 输出和 summary/report。可从每个样本 result.json 重新生成汇总。

`summary` 读取 v1 旧目录时只输出重新解释的多标签统计，不改写原始成绩。v1 的 9/15 不能与修正题面后的 v2 直接比较。

## 用例与 oracle

- R01 逐条核对 CLI 到 write_file Handler 的调用边、实际调用行、注册证据及读取区间；不检查磁盘落盘实现。
- R02 保留取消、未知效果、恢复和时限证据任务。
- W01 保留配置修复任务。
- W02 可修改共享 schema 构造文件并可选新增指定回归测试。独立 wire oracle 不加载候选新增测试；候选测试单独验证正确实现通过、历史缺陷由实际测试失败检出。
- W03 新增指定陈旧版本冲突回归测试，正确实现通过，缺陷实现由同一测试实际检出。

文件修改规则在 suite.json 统一声明，并自动附加到公开题面。`.claw` 仅排除引擎运行元数据，其他路径、文件类型与权限变化参与范围检查。用例原始快照源自 2026-11-05 首轮实验；R01 补充文件来自本轮改动前的源码归档。
