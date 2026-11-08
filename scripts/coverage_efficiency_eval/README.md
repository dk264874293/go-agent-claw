# 任务覆盖与上下文效率 v2 对照

此入口建立新的 v2 批次，复用写入评测的冻结、隔离 checker、用量与评审协议。不会继续此前已停止的 v1 实验。只有显式执行 `run-one` 会启动 Agent、可能产生模型费用；`selfcheck` 只运行本地假二进制，`summarize` 和 `review` 不调用模型或课程脚本。

```bash
python3 scripts/coverage_efficiency_eval/run.py selfcheck
python3 scripts/coverage_efficiency_eval/run.py init \
  --baseline-bin /绝对路径/baseline-claw \
  --candidate-bin /绝对路径/candidate-claw \
  --output .claw/evals/coverage-efficiency-v2-新批次
python3 scripts/coverage_efficiency_eval/run.py run-one \
  --experiment .claw/evals/coverage-efficiency-v2-新批次
python3 scripts/coverage_efficiency_eval/run.py review \
  --experiment .claw/evals/coverage-efficiency-v2-新批次 \
  --sample baseline-ordinary-1 --review-file /绝对路径/review.json
python3 scripts/coverage_efficiency_eval/run.py summarize \
  --experiment .claw/evals/coverage-efficiency-v2-新批次
```

运行凭据由项目 `.env` 启动器注入运行进程，runner 不加载或归档密钥。每样本工作区只复制冻结的 15 份 Markdown；不复制 `.env`、历史任务、二进制或汇总产物。网关和代理配置须保持一致。独立 checker 在进程结束后对隔离副本执行，环境没有模型凭据，禁网且只读；付费前必须通过真实 checker 与隔离预检。

## 固定样本和准入

| 顺序 | 样本 | 入口 |
| --- | --- | --- |
| 1 | baseline-ordinary-1 | 普通 workspace + change |
| 2 | candidate-ordinary-1 | 普通入口，增加声明来源与完整性门槛 |
| 3 | candidate-report-1 | `-report 优化汇总.md`，增加声明来源与完整性门槛 |
| 4 | baseline-report-1 | 同样使用 `-report 优化汇总.md` |

两组题面相同，均要求精简呈现、重要问题不遗漏、完整阅读指定原资料，并只新增 `优化汇总.md`。候选额外收到 15 个独立的 `-read-scope` 参数、`-require-full-read`、`-output-style concise` 和 `-read-page-bytes 8000`；runner 核对持久配置和声明范围的覆盖展示，不能仅因独立回执完整便忽略页面错误的 3/3 统计。

固定模型 `deepseek-v4.1-flash-1017`。每样本独立工作区、任务和新会话；预算为 60 模型、120 工具、30 轮、600 秒、300000 上下文字节，主对照读取档位 8000。共最多四个样本、240 次模型准入，不自动提高预算或重跑样本。

首对完成后，只有同时满足以下条件才准入严格组：

- 两组首对均提交带实际原文依据的语义评审；候选至少 8/10、零重大事实错误且不低于基线。
- 候选 15/15 按实际读取回执完整覆盖，仅新增指定产物，原资料摘要不变。
- 持久终态与返回一致，为 `NEEDS_VERIFICATION/2`，无未决动作，有确认写入回执。
- 候选不超过 12 次模型和 40 次工具。

基础设施失败、取消、文件边界违规或候选未过调用及完整交付门槛时停止批次；失败样本消耗保留。语义未评审时为 `not_run`，不能靠结构检查进入下一组。发现基础设施错误后修复工具也不能恢复原批次或自动重试。

## 评分与离线复算

语义评审格式沿用 [v1 协议](../write_efficiency_eval/README.md)，五项各 0–2 分：覆盖、引用定位、结论成立、版本条件、重要问题。评审须标明 `human` 或 `source_audit_agent`，每项分数及结论有准确原文依据；Agent 核对不能冒充独立人工。严格报告的引擎 `semantic_result=not_run` 不因外部评审而改写。

普通与严格各自比较一对，总 Token 降低至少 30% 为每组目标；分别列出模型、工具、输入输出 Token、耗时和费用。不混算两组中位数，不把材料字节下降等同 Token 降幅，费用未知保持 `null`。四个样本只支持本任务小样本结论。

归档包含冻结的 runner、共用协议、checker、helpers、二进制与资料摘要，以及逐例命令、事件、终端输出、效果和评审。`summarize` 从逐例结果离线重建，不依赖可重建的 `state.json`；冻结输入变更拒绝续跑，v1/v2 不能混用。工作代码变化后可以用已归档的 `harness/run.py summarize` 核对原实验。

自检覆盖重复路径参数、独立布尔参数、四样本上限、双方严格入口、候选缺配置与虚假覆盖、首对门槛、基础设施不重试、凭据脱敏、分组降耗目标、归档离线汇总及版本隔离。此处的假样本分数仅测试协议，不是实际模型质量或成本收益。

普通任务允许新建后继续编辑；文件效果按确认动作的完整版本链验收，包括确认无变化。未知效果、断链、重复动作身份及最终版本不符不能通过。协议检查失败后仍从唯一且完整的观测恢复可核对的Token，不把已记录消耗丢成零。

原批次评分器错误时，只对已保存样本另存离线回执复算：

```bash
python3 scripts/coverage_efficiency_eval/run.py recheck-delivery --experiment .claw/evals/原批次 --sample candidate-ordinary-1
```

该命令不启动Agent、不调用Provider、不修改原manifest、state、result或review，不清除停止状态。结果保存为样本的`delivery-recheck-v2.json`，包含原记录摘要及修复检查器摘要；已有复算禁止覆盖。修复后的工作代码不用于恢复旧批次。
