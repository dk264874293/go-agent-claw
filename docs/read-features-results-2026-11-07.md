# 基线默认与独立读取实验 v2：实施及验收记录（2026-11-07）

实现及离线验收已完成；真实实验的候选交付通过、语义质量未达标，不推广实验默认。普通新任务维持策略2，来源视图、局部核对和短核对提示继续独立、显式开启。历史任务不升级。

## 实现与兼容

- `read_features` 以版本1完整配置持久化；三项全关规范化为策略2/nil，任一开启采用策略4。CLI、Engine、Bench及飞书绑定接入同一配置；严格分析/报告/质量模式保持策略2，不能混入实验。恢复和Inspection校验策略及开关不变。
- 来源视图提供 typed `evidence_id`，保留旧32位材料ID接口。误将E填入id只返回纠正调用；offset/next_call始终相对原选中E，实际取回生成独立视图，不继承处理资格、不复制原文材料。
- 局部核对使用稳定 `file_ref`，仅组合当前、授权、成功交付、同版本的已有片段。返回结构化范围、缺口、原因和补读建议，不自动补读；越过源EOF的请求不生成循环补读建议。普通Schema与严格质量草稿分离。
- `claim_focus` 是独立≤1KiB通用提示，不含课程答案、不新增模型阶段。各能力关闭时没有新增Schema/提示；来源视图、FileRef、父子权限、版本失效及恢复边界分别校验。策略3保留旧契约。
- 来源视图created/delivered/processed/folded按实际阶段分别观测；核对尝试、请求项、回执和四类结果分开。策略3/4的私有核对材料均在观测台失败关闭，关闭开关不会解除材料保护。

配置和接口见 [read-features-v2.md](read-features-v2.md)，实验及审查命令见 [runner说明](../scripts/read_features_eval/README.md)。

## 离线验收

以下均通过；日志及SHA见检查点 `validation/accepted-checks.json`：

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
python3 scripts/read_features_eval/run.py selfcheck
node --check internal/monitor/web/app.js
```

覆盖八种能力组合；三协议真实SDK本地HTTP请求，默认off与显式策略2的编码请求相同；legacy3及新4来源视图的载体清理、业务参数、工具配对、Responses不透明项及原Session保持；自适应场景经历身份纠正、not_run、按next_actions补读、pass，7次正常模型/6次工具，不新增专用模型阶段。

新profile自检32项通过。增加真实CLI＋仅回环网关的无付费测试：复用runner真实argv，验证off持久为策略2/nil、on为策略4/三true，错误true/false写法在模型调用前拒绝。此测试补足仅假CLI自检无法覆盖的接口差异。

## 冻结及真实执行

两组使用同一冻结源码和同一构建，依次三off→三on；并非以旧二进制混用新开关。项目.env只在运行时读取允许变量，保存摘要而不归档值。15份固定Markdown共87105字节，各样本独立工作区和会话；deepseek-v4.1-flash-1017；8000读取档；60模型/120工具/30轮/600秒/300000上下文字节。普通workspace+change，不启用analysis/report/quality/强制完整阅读。

- 源码清单SHA：`3d9ad5c595aefe98ce56e14bb2558981d1bfb3a61a232e68933c1b3342510fa7`。
- CLI SHA：`7aadc1e7c1898dbf533f9e26f56201cdedf5ec5ddab1e7a697bb6161e543c4df`。
- 观测台SHA：`b0e1a185e6f227e59eb33339061fa33862e07dcb059e701824b89b0e3fa83fb9`。
- 新实验：`/Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.claw/evals/read-features-v2-20261107T104013Z-0ef297c9`。
- 全关任务：`ea3539bfb020c9fa583573af8cccbad9`；全开任务：`baa2c94b7ab7ba46e2089198abf15492`。

仅运行两个样本，共29次模型准入，无基础设施失败、自动重试或扩样。两者均15/15实际原文完整覆盖、原资料未改、仅新增优化汇总.md、NEEDS_VERIFICATION/2、零未决动作、确认产物版本匹配。机械文件/引用行范围检查通过，不能据此声称结论正确。

## 质量优先结果

同一实施会话AI审查者，method=source_audit_agent，匿名产物先按导出顺序审查，再反序复查；非独立人类验收。全部8项rubric、固定五轴和严重度在付费前冻结；claim绑定真实UTF-8输出区间及来源SHA/行号/引文，omission只绑定量表。归属通过review-map→sample_id→artifact SHA解析。

| 最终逐样本审查 | 覆盖 | 引用 | 正确性 | 条件 | 重要发现 | 合计 | 重大错误 | 重大遗漏 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 全关 | 2 | 1 | 0 | 0 | 1 | 5/10 | 1 | 1 |
| 全开 | 2 | 1 | 1 | 0 | 1 | 6/10 | 0 | 2 |

这两个分数是逐样本最终判断，**不计算正式配对质量差**：反序复查时，全开产物的subject_identity专项由supported改为incorrect，五轴及八项assessment虽未变，仍如实保留consistent=false、初审值和变更轨迹。`paired_review_protocol`、`paired_quality_delta_result`及正式配对质量差为not_run/null，没有选择较高分或重做审查以求一致。导入时仅机械规范化专项pass/fail枚举，判断与初审均未变。

- 全关把good静态合计计算为82.5，遗漏无空洞词对应的15分；全开正确得到97.5/29.5。这是可定位的局部改进。
- 两者均未交代第5节进入V2的适用边界，属于预先冻结的major遗漏；不能把所有差异直接按同一V1冲突处理。
- 全开还遗漏“格式维度仅检查非空，不验证ID/URL/enum合法性”，为第二项major遗漏；并新增“第2节声明2026年7月版”的普通来源归属错误。
- 两者均未充分处理已有质量测试命令及organizer已有tags与映射完整性的区别。全关还未充分交代手工反馈已有与宿主Hook待实测两层边界。

因此两份逐样本语义验收均fail；全开不足8/10且有重大遗漏。引用行号合法、覆盖完整、算对一项数值都不能替代完整质量通过。

## 原始消耗与机制（不能冒充合格降本）

| 指标 | 全关 | 全开 | 原始变化 |
|---|---:|---:|---:|
| 模型调用 | 19 | 10 | -47.37% |
| 工具尝试 | 88 | 46 | -47.73% |
| 输入Token | 1,142,599 | 427,853 | -62.55% |
| 输出Token | 39,649 | 28,244 | -28.76% |
| 总Token | 1,182,248 | 456,097 | -61.42% |
| 实际编码请求字节 | 3,508,600 | 1,296,542 | -63.05% |
| 总耗时（秒） | 283.2 | 203.8 | -28.04% |

两组费用均null，不能由Token直接宣称实际金额下降。总Token原始值少61.42%，但候选质量未达标且反序专项不一致，正式同等质量成本比较为not_run，`token_reduction=null`，仅`raw_token_reduction=0.614212…`。模型≤12的观察目标达到，工具46仍超过40；失败及修订消耗全部保留。

| 机制 | 全关 | 全开 | 解释 |
|---|---:|---:|---|
| 来源视图created/delivered/processed/folded | 0/0/0/0 | 0/0/0/0 | 两组都未调用read_evidence，本次没有验证真实采纳 |
| check_evidence尝试/请求项/回执 | 0/0/0 | 0/0/0 | 全开未采用；可判定率null，不宣称核对生效 |
| 笔记有效响应/机会 | 3/14 | 2/6 | 21.4%与33.3%，按model_call_id去重 |
| 取得处理资格来源 | 23 | 11 | 两组均使用既有笔记；不代表无遗漏 |
| 每请求Schema字节 | 12669 | 17517 | 开启后更大，当前未被采用的工具仍有Schema成本 |
| 成功写/改文件调用 | 3 | 1 | 全关1次write＋2次edit，全开1次write |

实际定向read_lines成功次数39→11，search_text 14→8，两组均有5次duplicate_read拒绝。全开主要少了定向重读及修订；不能把差异归因于未使用的来源视图、局部核对，也不能用单对同时开启三项的自由轨迹证明claim_focus的单独因果效果。

报告生成响应耗时（返回成功write/edit调用的模型响应合计）全关98.117秒/3响应，全开127.661秒/1响应；该口径包含响应其他内容，并非纯token生成时间。全开总耗时降低，但报告生成没有变快。

折叠原文的已观察累计字节分别1426843与319637，均有3次请求缺少对应数值字段，总量仍为null。不能将未采集字段记零或把累计折叠字节当唯一源文长度。完整机制及材料统计见检查点experiment-mechanisms.json。

## 交付与后续边界

- 源码/二进制/分批差异/验证日志：`/Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.claw/repair-checkpoints/read-features-v2-20261107T095644Z-8f61814f`。before保留实施前未提交源码；A/B为并行集成的可构建检查点，C为离线接受版本，candidate-source用于两组冻结构建。没有删除归档输入文件。
- 项目根目录claw与claw-monitor已从冻结源码重建；已有进程需要使用新构建并重启。外部工作区二进制及日常服务没有自动替换或重启。
- 实验全量证据、逐样本结果、匿名审查及正式summary保留；历史任务/旧实验未重写。正式summary的passed=false、candidate_delivery_result=pass、candidate_quality_result=fail、promote_default=false。
- 本轮没有新增真实样本或调高预算。代码接口修复已由离线机制测试证明，真实模型是否采用仍未达成；本任务的一对原始消耗观察不能证明稳定泛化或合格质量下的降本。
