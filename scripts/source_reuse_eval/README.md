# 普通任务来源复用对照 v1

独立 `source_reuse_v1` profile，顺序固定为 `baseline-ordinary-1 → candidate-ordinary-1`，最多两个真实样本、120次模型准入。它不修改或继续历史实验，33f仅作症状参考。只有 `run-one` 启动真实CLI；`selfcheck`、导出、评审导入和汇总不调用模型。

两组均使用普通 `workspace + change` 和自由Markdown，不启用 `-analysis`、`-report`、`-quality`、`-require-full-read` 或显式读取范围。题面只给资料根、全部Markdown和产物要求，不泄露15份清单或私有答案。外部验收使用冻结清单判断完整覆盖，不能把题面要求替换成引擎强制门禁。

## 离线检查

```bash
python3 scripts/source_reuse_eval/run.py selfcheck
python3 scripts/source_reuse_eval/run.py --help
```

自检使用假CLI、实际冻结来源、有限标注及匿名审查协议；不把假CLI产物计作真实质量成绩。时间字段反例严格解析冻结JSON对象，按实际存在性检查驱动错误草稿修订；不执行资料脚本或任意表达式。这是局部检查机制的有限证明，不是生产模型行为或Token改善证明。生产引擎的源码、真实Provider编码与安全折叠另由项目回归验证。

## 冻结与初始化

实施前baseline和实施后candidate必须分别从完整冻结源码构建，源码包括未提交实现和测试夹具，排除 `.env`、`.claw`、根演示workspace及构建产物。两份清单均须为非空JSON数组，每项至少含 `path`、`sha256`；建议同时保存 `bytes`。保留对应源码归档和构建命令，清单本身不能证明二进制由该源码构建。

```bash
python3 scripts/source_reuse_eval/run.py init \
  --output /绝对路径/新实验 \
  --baseline-bin /绝对路径/baseline/claw \
  --candidate-bin /绝对路径/candidate/claw \
  --baseline-source-manifest /绝对路径/baseline/source-manifest.json \
  --candidate-source-manifest /绝对路径/candidate/source-manifest.json \
  --project-dotenv /Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.env
```

默认资料为本profile私有冻结的15份、87105字节Markdown，已与ai-knowbase来源逐份核对SHA。可指定 `--source-workspace`，但资料清单、路径、字节数和SHA必须与本profile冻结版本一致。只复制枚举的Markdown，不复制图片、现有汇总或运行数据。

初始化复制并计算两份二进制、runner/common/helpers/checker/oracle/量表/反例及资料摘要，保存两组源码清单及manifest封印。冻结runner可离线重建汇总。进行下一样本前拒绝任何冻结输入漂移；新实验不得续跑此前已停止的批次。

## 真实执行

```bash
python3 /绝对路径/新实验/harness/run.py run-one --experiment /绝对路径/新实验
python3 /绝对路径/新实验/harness/run.py run-one --experiment /绝对路径/新实验
```

每次仅执行下一个样本，不循环、不重试、不扩样。固定同一 `deepseek-v4.1-flash-1017`、项目`.env`、8000字节读取档、精简输出；每个样本独立任务/会话/工作区。预算为60模型、120工具、30轮、600秒、300000上下文字节，已包含所有局部核对。

运行时只从指定项目`.env`读取允许的Deepseek及代理变量，不使用进程中的其他模型凭据；绑定整个`.env`摘要，第二个样本发现变化立即停止。模型凭据值不进入冻结归档。两组CLI接口和本机禁网只读评分沙箱必须在付费运行前预检通过。

baseline持久读取策略2、candidate策略3；普通任务状态均为1，读取记忆存储仍1。任何严格模式混入或版本错误属于结果协议失败。预算/合法业务阻断/缺产物/低质量不会阻止唯一candidate执行；基础设施、协议、取消、禁止路径变更或运行未确认停止整批。失败消耗全部保留，不自动重试。

## 匿名来源审查

```bash
python3 /绝对路径/新实验/harness/run.py export-review \
  --experiment /绝对路径/新实验 --output /绝对路径/新的匿名包
# 对匿名包内每份 *.review.json 填写评审，逐份导入：
python3 /绝对路径/新实验/harness/run.py import-review \
  --experiment /绝对路径/新实验 --review-file /绝对路径/匿名包/某review_id.review.json
python3 /绝对路径/新实验/harness/run.py summarize --experiment /绝对路径/新实验
```

产物使用随机review_id匿名；归属只能经 `review-map → sample_id → artifact_sha256` 确认，不按列表顺序推断组别。自检覆盖逆序导出。导入后不得覆盖评审，未知身份、产物漂移、错误来源SHA、错摘录、错输出字节区间均拒绝。

评审者填写身份及 `human` 或 `source_audit_agent` 方式，后者明确不是独立人工验收。每项分数引用实际finding。claim必须绑定产物UTF-8字节区间及真实来源SHA、行号、摘录；omission只绑定对应rubric和原文，不伪造输出片段。逐项核对：V1/V2、默认值、标注与静态数值、格式非空与合法性、已有测试命令、Hook手工反馈/运行边界、good/bad时间字段主体。专项分别审查修正建议、主体身份及重要遗漏。评审未完成为 `not_run`，不能仅凭引用行号合法宣布结论正确。

先判交付及质量：15/15按实际成功文件读取回执覆盖；SourceView不补造物理读取；原文SHA不变，仅新增指定产物，`NEEDS_VERIFICATION/2`、无未决动作、确认文件回执版本匹配。质量至少8/10、不低于可评分baseline、零重大事实错误和重大遗漏。checker只检查文件边界、结构及 `.md:行[-行]` 引用范围；不能证明结论成立。缺少可评分产物时配对分差为 `not_run/null`，不记零分。

成本随后报告。12模型/40工具、Token下降30%只是次级目标，不作为执行candidate或质量通过的前置门槛。分别保留实际Token、HTTP尝试/请求字节、耗时及已知费用，未知为null。一个配对只报告配对差，不计算中位数或宣称稳定泛化。

## 机制指标与边界

- 模型用量按model_call_id去重；实际编码字节按HTTP尝试统计，不混算。
- SourceView按view_evidence_id统计current/historical及创建内容字节；创建不等于已成功交付模型。覆盖只接受read_file/read_lines成功的原始Read回执区间并集。
- local_check按check_id去重，各结果单列；内部pass不证明主体选择正确、语义正确或整份系统契约成立。
- 工作笔记采纳按work_notes_response的响应机会统计，不能用逐工具missing作分母；折叠字节来自实际请求投影。旧组不存在的新观测记未知，不补零。
- 不存在独立quality_review/quality_revision阶段；检测到这些阶段拒绝普通实验。main中的核对仍消耗原共享额度。
- 没有笔记时必须保留未处理正文；有有效笔记才能按引擎条件折叠。复用和局部核对不要求模型一定采纳，不因未采纳偷偷增加摘要或修复调用。
