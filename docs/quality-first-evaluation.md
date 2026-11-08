# 质量优先真实对照 v1

`quality_first_v1` 是独立的一对报告样本，不延续或重算旧实验。不以 12 次模型、40 次工具或 Token 降低 30% 作为准入/通过门槛。外部质量先判；同等质量再比较成本。未知用量或费用保持 `null`。

## 冻结与启动

先从各自冻结源码构建 baseline、candidate，保留实际构建日志和包含夹具的源码清单。清单采用 `[{"path":"cmd/claw/main.go","sha256":"...","bytes":123}]`；排除 `.env`、任务目录、根 `workspace/`、构建产物。夹具内部名为 `workspace` 的目录属于源码输入，可保留。runner 会冻结二进制及两份清单、15 份 Markdown、oracle、量表、控制器和检查器，核对摘要后才允许继续。

```bash
python3 scripts/quality_first_eval/run.py selfcheck
python3 scripts/quality_first_eval/run.py init \
  --output /绝对路径/全新实验目录 \
  --baseline-bin /绝对路径/baseline-claw \
  --candidate-bin /绝对路径/candidate-claw \
  --baseline-source-manifest /绝对路径/baseline-source-manifest.json \
  --candidate-source-manifest /绝对路径/candidate-source-manifest.json \
  --project-dotenv /绝对路径/go-agent-claw/.env
python3 scripts/quality_first_eval/run.py run-one --experiment /绝对路径/全新实验目录
python3 scripts/quality_first_eval/run.py run-one --experiment /绝对路径/全新实验目录
```

只有 `run-one` 启动 CLI 和付费模型；其余命令离线。顺序固定 baseline-report-1、candidate-report-1，最多两个样本、120 次模型准入。两组均使用 `-report`、相同显式资料范围、`-require-full-read`、精简偏好和 8000 字节读取档，仅候选添加 `-quality`。固定模型 `deepseek-v4.1-flash-1017`；每样本 60 模型、120 工具、30 轮、600 秒、300000 上下文字节，复核/修订计入其中。

运行环境只导入项目 `.env` 中允许的 Deepseek/传输项，隔离 HOME、临时目录、观测和诊断目录；不继承进程里的其他模型凭据。仅保存 `.env` 摘要，值不写入实验 manifest。首次启动绑定该摘要，第二个样本发生配置漂移即停止。

独立 checker 进程使用 macOS `sandbox-exec`，无凭据、禁网、禁止写入；先检查实际冻结夹具/检查器隔离能力。预检失败不启动付费 CLI。两个二进制的 `-h` 所需入口在首样本前检查，帮助调用不携带凭据、不创建任务。评分在进程退出后对独立副本执行，不运行课程代码、模型或原始任务验收命令。

## 停止与结果

基础设施失败、结果协议错误、取消、禁止路径变化、未决动作和未确认进程都会停止批次，不自动重试。质量门禁阻断、模型返回坏 JSON、预算耗尽、低分或缺产物属于业务失败，记录全部消耗，但不阻止本批唯一候选执行。

文件边界只判断原文未改、没有额外路径；指定产物是否存在另列 `artifact_exists`。因此合法质量阻断不会被误判为越界。完整交付要求 15/15 原文回执覆盖、仅新增指定报告、`NEEDS_VERIFICATION/2`、无 Pending、真实发布回执链和报告来源校验。正式引用另由 runner 对照实际成功读取动作、冻结源字节、版本和绝对行号独立复核。候选质量票据还必须满足状态与返回值一致、复核已完成/跳过，或复核格式无效但仍有逐字事实/机械可证核心可合法局部发布；修订已完成；受限冻结材料 SHA 必须与正式产物和报告版本完全相同。

## 匿名语义评审

```bash
python3 scripts/quality_first_eval/run.py export-review \
  --experiment /绝对路径/实验目录 --output /绝对路径/目录外匿名包
# 填写匿名包中的 <review_id>.review.json
python3 scripts/quality_first_eval/run.py import-review \
  --experiment /绝对路径/实验目录 --review-file /绝对路径/<review_id>.review.json
python3 scripts/quality_first_eval/run.py summarize --experiment /绝对路径/实验目录
```

先匿名审查产物，再审计受限任务材料中的核验、复核及修订前后记录，填写 `proposed_repairs`、`review_misses`、`revision_new_errors` 三项。专项无实际对象时填 `not_applicable` 和具体原因；不能把未执行评审填成通过。

五项量表为覆盖、引用、结论成立、版本条件、重要问题，各 0–2 分。候选须至少 8/10、零重大事实错误、零重大遗漏，且不低于可评分基线。每个错误结论绑定产物 SHA、真实 UTF-8 输出区间、原文 SHA/行号/引文；遗漏绑定私有量表及来源，禁止伪造输出片段。复核 `invalid` 只表示结果已确认但格式无效，不属于网络故障；有效部分可发布，外部评分仍可能因重要遗漏失败。复核 `failed` 不允许正常发布。

`major_error_ids` 只指向错误命题，`major_omission_ids` 只指向遗漏。每项评分必须引用真实 finding ID。

归属只通过 `review-map → sample_id → artifact_sha256` 确定，导出顺序随机且不承担组别语义。测试覆盖反转导出顺序；不得依据匿名列表的第一/第二项猜测组别，也不把不同抽查条目数换算成准确率。

没有可评分产物时，质量差为 `not_run`/`null`，不记零分。未填写语义评审也是 `not_run`，内部检查或模型复核通过不能替代外部评审。实施会话审查时使用 `source_audit_agent` 并填写真实身份，明确不是独立人工验收。

## 质量与成本分别报告

`summary.json` 分别记录候选交付、候选绝对质量、配对质量差及是否有测得的质量提升。质量相同而通过基本门槛，不等于已证明本轮质量改善；`quality_promote_default` 始终为 false，默认推广需另行决定。

每个样本的 `metrics.phases` 按 `model_call_id` 去重，分别统计 main、review、revision 的模型数、工具尝试数、输入/输出 Token、实际编码请求字节及模型耗时。未知值保持 null，并报告已知部分与未知调用数。未带阶段的工具按最近模型响应归属；无归属单列，嵌套复核阶段依靠 `quality_review`/`quality_revision` 标记。

总成本保留失败样本全部消耗。报告 Token 比例不意味着失败样本获得效率成绩；本 profile 不设置成本通过门槛。只有一对样本，不支持稳定泛化。

冻结的 `harness/run.py` 可在当前源码改变或缺失后独立汇总；摘要校验拒绝冻结文件被篡改。离线汇总允许重建丢失的 `state.json` 索引，但付费续跑要求逐例档案与索引完全一致。

## 脱敏反例

`eval/fixtures/quality_first_v1/` 包含原文来源 oracle/六项重要问题量表，以及 12 个有限命题反例。新增反例覆盖漏乘系数、默认值、送检 AST 与文字公式不一致、JSON null/同名字段/Pointer、对象范围越界、版本边界、非空与合法性、已有命令遗漏。有限检查仅识别完整标注文本；未知表达为 `not_run`，不冒充通用语义判断或真实模型质量提升。
