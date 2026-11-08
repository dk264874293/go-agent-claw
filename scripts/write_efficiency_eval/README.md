# 写入任务自由轨迹评测

只有 `run-one` 启动真实模型。默认夹具为 `eval/fixtures/write_efficiency_v1/workspace` 中原任务读取过的15份Markdown（87105字节）；不含 `.env`、图片、二进制、新增汇总或完整历史模型请求。

```bash
python3 scripts/write_efficiency_eval/run.py selfcheck
python3 scripts/write_efficiency_eval/run.py init \
  --baseline-bin /绝对路径/baseline-claw \
  --candidate-bin /绝对路径/candidate-claw \
  --output .claw/evals/write-efficiency-批次ID
python3 scripts/write_efficiency_eval/run.py run-one --experiment .claw/evals/write-efficiency-批次ID
python3 scripts/write_efficiency_eval/run.py review \
  --experiment .claw/evals/write-efficiency-批次ID \
  --sample baseline-ordinary-1 --review-file /绝对路径/review.json
python3 scripts/write_efficiency_eval/run.py summarize --experiment .claw/evals/write-efficiency-批次ID
```

运行凭据由启动器注入进程环境；runner不读取项目 `.env`。子工作区没有 `.env`。冻结manifest只保存有关环境变量名称与网关/代理摘要，不保存凭据或端点正文。checker使用新的无凭据环境，macOS沙箱禁止网络和文件写入；网络/凭据隔离预检失败不启动付费样本。checker在进程结束后的隔离副本执行，不加载Agent新增测试或执行课程代码。

样本顺序固定为baseline普通1、candidate普通1、candidate普通2、baseline普通2、baseline报告1、candidate报告1。每例60模型、120工具、30轮、600秒、300000上下文字节；固定Deepseek `deepseek-v4.1-flash-1017`、8000读取档。仅candidate报告例使用 `-report 优化汇总.md`；baseline报告例采用相同题面的普通workspace入口。candidate组须实际持久化启用 `read_context_version=1`。

前两例完成后，必须分别提交语义评审才能扩样；候选还须满足完整覆盖、合法文件范围、确认写入回执、独立机械oracle、`NEEDS_VERIFICATION/2`、零未决动作以及最多12模型/40工具。首对质量至少8/10、重大错误为零且不低于同对基线。基础设施失败、取消、范围违规停止批次；失败样本保留用量。未确认样本、既有工作区、输入变化和运行锁均不会被自动删除重试。

评审文件格式：

```json
{
  "reviewer": "实际评审者身份",
  "method": "human",
  "scores": {"coverage": 2, "citation": 2, "correctness": 2, "conditions": 2, "important_findings": 2},
  "score_basis": {
    "coverage": "逐项说明覆盖依据",
    "citation": "逐项说明引用定位检查",
    "correctness": "核对结论与反例",
    "conditions": "核对版本、默认值和执行条件",
    "important_findings": "核对重要问题是否遗漏"
  },
  "major_errors": 0,
  "findings": [{
    "claim": "实际被评审的结论",
    "verdict": "supported",
    "evidence": [{"path": "必须替换为夹具中的完整相对文件名.md", "start_line": 1, "end_line": 2, "quote": "必须替换为该行范围内的真实短原文"}]
  }]
}
```

上例分数仅为格式占位，不是实际质量成绩。`method` 仅允许 `human` 或 `source_audit_agent`；Agent审阅不能冒充独立人工评审。逐项证据路径、范围、摘录必须与冻结原文吻合，未完成评审为 `not_run`。checker的主题/行范围检查不能证明结论成立，严格报告的 `semantic_result=not_run` 也不会被runner改写成pass。

所有二进制、夹具、题面、runner/checker输入均冻结并校验摘要；实际命令、前后文件清单、日志、事件及checker输出逐例归档。`summarize` 从逐例结果文件重建，无需模型、网络或原始state索引。普通2对的Token中位数降幅独立计算；报告1对单独展示，未知用量或费用保持null。最多6样本360模型准入，样本不足或质量未评审不能宣布总体通过。

## checker 环境修复后的离线重检

基础设施失败后原批次保持停止，不因修复checker而自动重开付费样本。仅重检已保存产物：

```bash
python3 scripts/write_efficiency_eval/run.py recheck-original \
  --experiment .claw/evals/原失败批次 \
  --sample baseline-ordinary-1
```

该命令核对原manifest中的所有冻结输入摘要，使用**原冻结checker**和隔离副本、修复后的沙箱profile；另存 `checker-repaired.json`、stdout/stderr、命令和 `checker-repaired.evidence.json`。证据包含原记录摘要及未改变检查、原checker摘要、修复runner摘要和零付费调用声明。它不会改写manifest、state、result或原checker日志，也不会清除原错误、启动Agent或恢复批次。再次离线诊断可显式使用 `--label checker-repaired-第二次ID`，禁止覆盖已有修复证据。

当前沙箱仅给实验根各祖先目录的字面路径放行 `file-read-metadata`，供 `Path.resolve` 使用；HOME中无关文件和祖先目录的数据读取、文件写入及网络仍拒绝。付费前预检还会复制完整冻结夹具并真正执行冻结checker，不能仅用socket阻断作为checker可运行证明。自检在真实HOME的 `.claw` 隔离临时目录覆盖正反场景，并在完成后清理该临时目录。
