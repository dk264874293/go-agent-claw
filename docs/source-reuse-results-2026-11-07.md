# 来源复用与局部核对 v1：2026-11-07 验收记录

代码及离线验收已完成；本轮真实对照未通过质量验收，不能宣称优化成功。candidate评分6/10，baseline为8/10；两者均有一项重大遗漏。candidate总Token增加26.4%，耗时减少21.1%。这是同任务同模型的一对样本，不能外推稳定收益。

## 已实现

- 普通新任务策略3、独立来源视图EID、不可变原文切片复用、跨读取入口复核与安全折叠；严格模式保持策略2，历史任务不升级。
- check_evidence局部算术/JSON核对，正常扣共享工具额度，失败结论不伪装工具失败或语义验收；同批写入在回执复用和Handler之前拒绝。
- prepared→材料→completed的持久回执、Inspection/RunResult及CLI/飞书/观测台摘要；旧索引窗口和陈旧证据ID不能暴露完整检查材料。
- 三协议真实SDK本地HTTP验证、无付费自适应脚本、独立source_reuse_v1 runner及冻结反例。

接口、兼容和重启说明见[source-reuse-v1.md](source-reuse-v1.md)。当前实现按已确定方案对新普通任务默认启用策略3，但这不代表已取得真实质量准入或稳定降耗；本次不修改日常工作区的旧二进制或重启运行进程。

## 离线检查

以下全部通过：

```bash
go test ./cmd/... ./internal/...
go test -race ./internal/...
go build ./cmd/... ./internal/...
python3 scripts/real_eval/run.py selfcheck
python3 scripts/write_efficiency_eval/run.py selfcheck
python3 scripts/coverage_efficiency_eval/run.py selfcheck
python3 scripts/work_notes_eval/run.py selfcheck
python3 scripts/quality_first_eval/run.py selfcheck
python3 scripts/source_reuse_eval/run.py selfcheck
node --check internal/monitor/web/app.js
```

新profile为23项离线控制面测试。Chat、Anthropic、Responses本地HTTP测试验证来源视图载体实际消费、业务参数和配对、原Session及opaque保留。额外覆盖未提交canonical、伪造切片缓存、UTF-8/非零原offset、父子/恢复/Bash同摘要失效、同批写入与历史回执、prepared未确认和未刷新索引访问。测试不调用真实模型，不冒充生产Token或语义收益。

## 冻结与真实运行

- 实验：`/Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.claw/evals/source-reuse-v1-20261107T084807Z-0dfcffa2`。
- 检查点：`/Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.claw/repair-checkpoints/source-reuse-v1-20261107T082045Z-223d9e2f`。before保留既有未提交源码；A/B/C是并行实现过程的可构建集成快照，不是逐项git提交。
- baseline SHA256：`e2efc18b8b8858abe027349d2f93bf39f4a6363e05fadb3e81ffb5b4a083348e`。
- candidate SHA256：`b7805cf2992937f974e9cd8b658ad757733d13e3a1aab846e0962209f1f6d496`。
- 从各自冻结源码构建；清单包含忽略的测试夹具，排除凭据、运行数据和根演示workspace。共用冻结runner/oracle/15份87105字节原文。
- deepseek-v4.1-flash-1017、同一项目.env网关、普通workspace+change、8000读取档、60模型/120工具/30轮/600秒/300000字节。无analysis/report/quality/full-read配置。
- baseline→candidate，各一次，共40次模型准入和40次HTTP请求，无基础设施失败、无重试，未扩样。费用均未知null。

| 指标 | baseline | candidate |
|---|---:|---:|
| 终态 / 退出码 | NEEDS_VERIFICATION / 2 | NEEDS_VERIFICATION / 2 |
| 原文完整覆盖 | 15/15 | 15/15 |
| 仅新增指定产物、原文未改 | 通过 | 通过 |
| 未决动作 | 0 | 0 |
| 模型 / 工具 | 19 / 57 | 21 / 59 |
| 输入Token | 765191 | 986493 |
| 输出Token | 40137 | 31602 |
| 总Token | 805328 | 1018095 |
| 实际编码请求字节 | 2315118 | 3015295 |
| 总耗时（秒） | 313.746 | 247.477 |
| 来源审查分数 | 8/10 | 6/10 |
| 重大错误 / 重大遗漏 | 0 / 1 | 0 / 1 |

两组文件边界、回执、终态与引用行号机械检查均通过，外部语义审查均未通过。成本比较的同等质量前提未满足，因此正式comparison_result为not_run；原始Token差值仍完整保留为+212767（+26.4%），不据此宣称降耗。12模型/40工具和30%降幅仅次级目标，本次均未达到。

## 质量及机制结果

匿名映射通过review-map→sample_id→artifact_sha256绑定，不按导出顺序推断。评审身份为Codex实施会话来源审查A/B，方式source_audit_agent，不是独立人工；全部claim绑定真实产物UTF-8区间，遗漏绑定冻结rubric和来源SHA/行号/引文。两组使用同一七项rubric与五轴0–2分，不用不同审查条目数量计算准确率。

baseline五轴为覆盖2、引用1、正确性2、条件2、重要问题1；candidate为2、1、1、1、1。两者均正确讨论97.5/29.5、默认5→12.5及V1/V2边界，但都没有明确解释格式计分只检验truthiness/非空、不验证ID/URL/状态合法性，这是冻结量表中的重大遗漏。已有good/bad单条/批量命令未充分概览，另作次要遗漏。

candidate新增一个可证伪的普通事实错误：称organizer没有承载标签的字段，但同段所引用的标准合同明确包含tags。Hook宿主自动行为还有证据不足的推断，按待核实处理，未伪称已实测。good对象的collected_at未再被错误判为缺失。机械引用定位通过不能代替上述语义判断。

机制单独判定：

- candidate创建来源视图0次，实际视图折叠收益未在本样本验证；4次read_evidence都传了E1/E5/E18/E19等短证据ID，而该入口要求不可变材料ID，并携带review，结果被拒绝。离线有效材料ID路径通过，但真实入口身份提示仍有摩擦。
- 1次check_evidence提交3项算术检查，工具正常成功，三项结论均为not_run：所选证据不能证明请求的完整行范围。没有把来源不足记成检查通过，也没有额外修复请求；有效核对采纳目标未达到。
- 笔记响应采纳率均为25%（baseline 3/12，candidate 4/16）；candidate处理来源11条，baseline28条，这里是证据ID条数，不是文件数。
- 累计原文材料字节759581→1165421，Schema字节240711→388668，累计折叠原文字节872352→633093。这些是逐请求累计值，不能当唯一文件大小或直接归因成Token份额。

补充分段记录位于实验的supplemental-phases.json：工具层首次完整覆盖baseline发生于第10次模型/36次工具，candidate为第8次模型/21次工具；不表示此后调用都浪费，也不证明最后批次已被模型消费。生成首次写入调用的模型耗时分别108.634秒和143.027秒，baseline另有一次编辑生成8.255秒；这不是文件I/O耗时，不能把总耗时下降简单归因于输出或核对工具。

## 交付与限制

项目根目录的claw与claw-monitor已从冻结产物复制，CLI SHA与上述candidate一致。未替换ai-knowbase/claw，未重启旧会话；恢复旧任务继续原策略。需要使用新构建并启动新任务才能采用策略3。若要回退，baseline二进制及完整源码位于检查点，历史任务记录未重写。

本轮不再追加真实样本或调整口径。后续优先项是证据ID/材料ID的区分及纠正提示、来源行范围的可操作反馈，以及字段映射和运行边界的写前核对；不能把当前离线机制通过当成这些问题已在真实任务中解决。
