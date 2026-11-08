# 独立读取能力 v2：同构建双样本

此profile仅 `run-one` 启动真实CLI；selfcheck、导出、导入及汇总不调用模型、不运行课程脚本。固定一对 `baseline-ordinary-1 → candidate-ordinary-1`，最多两个样本、120次模型准入，不续跑旧实验、不自动重试。

两组必须来自**同一份冻结源码和同一二进制**。初始化强制两份二进制SHA、两份源码清单文件SHA完全一致。baseline显式关闭三能力（读取策略2、read_features为空），candidate显式开启（策略4、read_features version1三项true）。不以旧二进制及新开关混合比较。

## 离线验证

```bash
python3 scripts/read_features_eval/run.py selfcheck
```

使用假CLI验证控制面与评审协议，并构建真实CLI在仅允许回环网络的本地HTTP网关验证实际on/off入口及持久状态（无付费、无外网），来源绑定的有限标签验证tags字段、Hook运行边界及格式仅非空等反例。自检成功不证明真实模型质量或Token收益。私有来源清单仍为15份、87105字节Markdown；五轴锚点和逐rubric严重度在anchors.json/rubric.json冻结，不注入生产题面。

## 初始化与真实运行

先从包含未提交实现、测试夹具的冻结源码构建一个CLI，排除.env、任务数据和根演示workspace，保存源码归档及路径/sha256/bytes数组清单。以下bin和manifest故意传同一文件：

```bash
python3 scripts/read_features_eval/run.py init \
  --output /绝对路径/新的read-features实验 \
  --baseline-bin /绝对路径/冻结构建/claw \
  --candidate-bin /绝对路径/冻结构建/claw \
  --baseline-source-manifest /绝对路径/冻结构建/source-manifest.json \
  --candidate-source-manifest /绝对路径/冻结构建/source-manifest.json \
  --project-dotenv /Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.env
python3 /绝对路径/新的read-features实验/harness/run.py run-one \
  --experiment /绝对路径/新的read-features实验
python3 /绝对路径/新的read-features实验/harness/run.py run-one \
  --experiment /绝对路径/新的read-features实验
```

默认使用私有冻结fixture，可用--source-workspace指定同SHA资料。普通workspace+change，自由Markdown交付；不启用analysis/report/quality/full-read/read-scope。共同题面只给全部Markdown、目标文档和精简要求，不给数量、清单或私有答案。实际argv中显式传：

- baseline：`-source-views=off -local-checks=off -claim-focus=off`
- candidate：`-source-views=on -local-checks=on -claim-focus=on`

两组均为deepseek-v4.1-flash-1017，同项目.env网关、8000读取档、60模型/120工具/30轮/600秒/300000上下文字节；每样本独立工作区/任务/会话。只从项目.env读取允许的模型和代理变量，不使用进程中的其他模型凭据，绑定.env摘要但不归档值。两组CLI入口和禁网评分沙箱都在付费前预检。

业务阻断、预算停止、缺产物或低分不阻止唯一candidate；基础设施、协议、取消、越界修改、未决/未确认运行停止整批。所有失败消耗保留，不重试、不扩样。

## 同审查者盲评与反序复查

两样本结束后统一导出；不得先按已知组别评分再选审查者。

```bash
python3 /绝对路径/新的read-features实验/harness/run.py export-review \
  --experiment /绝对路径/新的read-features实验 --output /绝对路径/新的匿名包
```

同一审查者用同一method盲评匿名包的两个产物，按index.review_process.first_order评一遍，再按reverse_order反序复查。填写每份*.review.json：

- 五轴各0/1/2及score_basis；以冻结anchors.json为依据。
- 八项rubric_assessments必须齐全，每项status为supported/incorrect/omitted/unverified，severity为none/ordinary/major，填写reason及真实finding_ids。遗漏severity遵守冻结量表，不能事后降级以过门槛。
- claim绑定真实产物UTF-8起止字节与原文，omission绑定rubric，二者都保存来源SHA/行号/引文；不得为遗漏伪造输出span。
- major_errors/major_omissions均为非负整数，与对应ID数组一致；仍需分别审查建议、主体身份和重要遗漏。
- reverse_check保留first_order/reverse_order、初审initial_scores，以及初审rubric_assessments的摘要。摘要算法：SHA256(JSON UTF-8，sort_keys=True，ensure_ascii=False，separators=(',', ':'))。一致时consistent=true，初审分数/assessment摘要必须与最终值一致；不一致时consistent=false并保留初审值及原因，配对质量差保持not_run，不能挑较高分。

审查method为human或source_audit_agent；后者是AI来源审查，不冒充独立人工。不同reviewer或method禁止作为同一配对导入，不能计算跨审查者质量差。归属只经review-map→sample_id→artifact_sha256，不按匿名列表顺序猜测组别。

```bash
python3 /绝对路径/新的read-features实验/harness/run.py import-review \
  --experiment /绝对路径/新的read-features实验 --review-file /匿名包/第一份.review.json
python3 /绝对路径/新的read-features实验/harness/run.py import-review \
  --experiment /绝对路径/新的read-features实验 --review-file /匿名包/第二份.review.json
python3 /绝对路径/新的read-features实验/harness/run.py summarize \
  --experiment /绝对路径/新的read-features实验
```

质量先判：15/15按真实文件读取回执完整覆盖、原文未改、仅新增指定产物、NEEDS_VERIFICATION/2、无未决动作、回执版本匹配；候选至少8/10、不低于可评分baseline，零重大错误/遗漏/未核实的重大断言。逐rubric判断不以引用行号合法替代语义。原七项全部保留，另加field_mapping（合同已有tags不能说不存在）。baseline不是正确答案。

分数差、重大错误/遗漏差及逐rubric变化分别报告；同8分但从有重大遗漏变为零重大遗漏也需明确。缺产物或未完成同审查者/反序协议时配对质量差not_run，费用未知null。成本是次级结果，12/40和Token下降30%仅观察目标，不用提前失败获取降耗分；一对样本不能证明稳定泛化。

## 机制与材料指标

SourceView created/delivered/processed/folded各阶段分开按view ID、model_call_id去重，同时显示唯一视图数和逐请求累计字节。没有交付成功、原文处理或实际折叠证据，不能拿created冒充生效。物理覆盖只统计read_file/read_lines，视图不补造读取。

check工具尝试、checks_requested、回执完成、pass/fail/error/not_run分别统计；可判定率为(pass+fail)/checks_requested。工具成功但3项not_run不能称三项核验生效。材料字节、Schema、编码请求体、Token与费用分别保留；缺失新指标为unknown/null，不能把未采集记0。main之外出现quality_review/quality_revision视为协议错误。

观测存在gap/dropped/incomplete时，机制的完整计数保持null，另列已观察部分，不用缺口期间的零事件宣称未发生。关闭能力且收到完整run_configured时，零值才是已确认禁用状态；缺失新指标保持unknown。

开关是String flag，只接受on/off，不能传true/false。原生CLI回归直接复用sample_arguments，检查off为策略2/配置空、on为策略4/三项true，并确认旧true写法在模型请求前被拒绝。原生测试在缺Go源码或macOS回环沙箱时明确skip；冻结harness的离线重算不依赖原生源码。
