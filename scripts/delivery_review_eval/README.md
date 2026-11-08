# 普通交付前核对 v1：独立双样本

仅 `run-one` 可启动真实模型；自检、审查校验、封存、导入、汇总均零付费。旧profile及历史实验不修改、不重算。

两组必须同一冻结源码清单SHA和同一二进制SHA。固定顺序baseline→candidate，最多2样本/120模型准入：共同 `-source-views=off -local-checks=on -claim-focus=off`，只改变 `-delivery-review=off|on`。两组都读取策略4/B-only；普通taskstate分别1/3，读取记忆存储仍1。独立Reader校验实际版本，不将3重标为1。严格analysis/report/quality/fullread均不开。

## 自检与初始化

```bash
python3 scripts/delivery_review_eval/run.py selfcheck
python3 scripts/delivery_review_eval/run.py init \
  --output /绝对路径/新的实验 \
  --baseline-bin /绝对路径/冻结构建/claw \
  --candidate-bin /绝对路径/冻结构建/claw \
  --baseline-source-manifest /绝对路径/冻结构建/source-manifest.json \
  --candidate-source-manifest /绝对路径/冻结构建/source-manifest.json \
  --project-dotenv /Users/wangpeiliang/Desktop/AI-project/go-agent-claw/.env
python3 /绝对路径/新的实验/harness/run.py run-one --experiment /绝对路径/新的实验
python3 /绝对路径/新的实验/harness/run.py run-one --experiment /绝对路径/新的实验
```

同 `deepseek-v4.1-flash-1017`、项目.env运行时配置、冻结15份Markdown（87105字节）、同题面和独立工作区/会话。每样本60模型/120工具/30轮/600秒/300000上下文字节，读取8000；准入和阶段包含原共享预算。源码清单不含.env/凭据/任务数据，模型凭据不归档，只绑定.env摘要。评分进程无凭据、禁网、不执行资料脚本；CLI接口与真实隔离checker在付费前预检。基础设施/协议/取消/越界修改/未确认运行停止，不自动重试；baseline业务失败不阻止唯一candidate。缺产物不得记零分制造质量差，所有失败消耗保留。

## Typed来源审查：两遍封存后导入

两例结束后导出到实验之外的新目录：

```bash
python3 /新的实验/harness/run.py export-review --experiment /新的实验 --output /新的匿名包
```

同一审查者和method按index.first_order逐份初审，再按reverse_order反序复查，独立保存草稿。严格字段见匿名包review-schema.json；示例仅通用格式，不是私有答案。每个finding必须有冻结issue_types、判断、severity及真实来源；claim精确绑定实际产物UTF-8区间，omission只能绑定rubric/原文，不伪造输出。八项rubric和五轴固定锚点全覆盖。

```bash
# 每份初审：先只读校验，再完整封存。两份first完成后才能reverse。
python3 /新的实验/harness/run.py validate-review --experiment /新的实验 --review-file /匿名包/某份.first.json
python3 /新的实验/harness/run.py seal-review --experiment /新的实验 --phase first --review-file /匿名包/某份.first.json
# 按严格反序逐份封存复查；不得覆盖初审快照。
python3 /新的实验/harness/run.py seal-review --experiment /新的实验 --phase reverse --review-file /匿名包/某份.reverse.json
# 全部两遍封存后逐份导入reverse草稿，最后汇总。
python3 /新的实验/harness/run.py import-review --experiment /新的实验 --review-file /匿名包/某份.reverse.json
python3 /新的实验/harness/run.py summarize --experiment /新的实验
```

SPECIAL、major错误/遗漏/未核实、最终质量门槛由typed findings及冻结分类映射派生，禁止手填矛盾结果。“来源归属incorrect但主体专项supported”会被拒绝。ordinary问题只按固定分轴扣分，不额外偷加零普通遗漏门槛。至少8/10、零major错误/遗漏/未核实才绝对质量通过；本地字段校验不承担语义判定，AI来源审查不冒充独立人类。

first/reverse完整快照以受限文件、SHA及封存日志保存。程序比较结构化语义（判断/严重度/问题分类/来源与产物位置/分数），忽略理由措辞和展示排序；不接受手填consistent。真实判断变化保留分歧，正式配对质量差not_run，不能挑更好分或续评到一致。元数据格式错误在封存前通过validate-review发现，不改变原始来源判断。

归属只走review-map→sample_id→artifact_sha256，不按匿名列表顺序猜组别。整体quality_improved必须candidate绝对pass、正式pair有效、不退分、不增加重大错误/遗漏后至少一个指标改善；局部问题改善单独列出。成本次级：未知费用null；同等合格质量比较不足时正式降本not_run，只保留原始消耗观察。12模型/40工具/Token降30%为观察目标，不是candidate启动门槛或质量替代品。

## 机制回归对应

脚本Provider的自适应执行回归已接入Go：

```bash
go test ./internal/engine -run '^TestDeliveryReviewDefersWriteOnceAndUsesNormalEvidenceNotes$|^TestDeliveryReviewAdaptiveCheckCounterexampleChangesOutput$' -count=1
```

该场景根据真实delivery_review_required回执提交来源处理声明再写入，核对一次准入与原共享预算；不是固定正确轨迹冒充观察采用。配套覆盖最后允许写入、自然JSON仍可留下未解决项、取消准入后恢复不重调模型。Python自检验证独立结果解析、阶段计数、预算和评审控制面；nativeCLI复用真实argv、真实SDK、本地回环HTTP验证state1/3及错误true参数在HTTP前拒绝。

四核心rubric的unverified必须按major记录，不能通过全部降为普通待核实凑够8分。问题分类和SPECIAL诊断可有ordinary不正确项；整体gate只由冻结五轴、major错误/遗漏/未核实决定，普通问题按固定锚点扣分。

## 准入、实际请求与采用机制

`metrics.model_calls`和`budget_model_admissions`保留引擎Usage准入；`actual_model_requests`及main/delivery_review分阶段只计实际开始/结束的请求。仅允许由taskstate3的同Run票据解释一次差额：admission_count=1、有效配置、同Run、状态failed/outcome_unknown且实际delivery_review阶段为0；例如上下文硬限或取消发生在Provider调用边界前。版本1省略的消费bool按false，显式null/非bool拒绝，ModelConsumed=true要求TurnConsumed=true，但本地消费标志不能证明已调用Provider或已发HTTP。不得改Usage为0或伪造一次网络调用；其他差额仍为协议错误。

`delivery_review_mechanisms`单列有来源发现、文档处置计数、就绪后显式处置变化、核对工具尝试/请求/回执/声明采用。来源声明统计动作当时已提交记录，不把声明当语义正确，也不把fail回执改成pass。completed只表示机会执行完成，不表示来源目录、检查或报告语义通过；无待办的not_needed不算运行错误。机制未采用记not_achieved，未见就绪项为not_applicable；不为机制扩展付费样本。整体passed仍由交付与质量判定，mechanism_result另列，默认推广始终false。

`delivery_review_prompt_bytes`独立统计目录/交付提示材料，不并入老feature_prompt_bytes；缺失为null，显式off的0为已知值。自适应JSON反例回归根据真实false/0检查回执修正输出，原fail回执保持，不能把声明采用当机械检查改成pass。

actual_model_requests按model_started统计本地Provider调用边界，实际HTTP尝试另报；开始调用不等于收到有效模型结果或已收费。

## 冻结后Reader兼容修复记录

2026-11-08仅修改项目源Reader及回归：补齐准入后、消费turn前取消的omitempty标志，以及takeModel后、model_started前取消的合法一次未发出预留。该修复不改变预算计数、正常成功两组解析、语义门槛或任何已有实验/harness/冻结源码；不重写旧成绩、不启动模型或重跑样本。后续独立离线校验可比较前后对正常样本的投影，报告仅作兼容证据。

同日另修Go nil-slice兼容：有效的普通摘要笔记可持久化findings:null，Reader应按已知Go slice空值处理，不能因`dict.get(..., [])`仍返回None而误报协议错误。go_nil_slices.json仅保存该真实故障的脱敏结构形状，不含原始正文、身份或凭据；回归同时拒绝非数组的错误类型。修复仅在项目源及后续新profile生效，原已停止实验的协议失败与checker记录完整保留，不续跑、不改写旧成绩。
