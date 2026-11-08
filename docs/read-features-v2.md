# 基线默认与独立读取实验 v2

普通新任务默认策略2，保持已有读取记忆及自由Markdown/业务JSON交付。实验能力默认关闭，权限、共享预算、存储错误、未知效果和恢复边界不变。

```bash
./claw -prompt "汇总资料"
./claw -source-views on -local-checks on -claim-focus on -prompt "汇总资料"
```

## 配置与恢复

CLI三项均为off/on，显式提供与未提供分开解析，无环境变量或目标关键词路由。Engine、Bench用例及飞书服务端绑定使用同一个可选对象：

```json
{"read_features":{"version":1,"source_views":true,"local_checks":true,"claim_focus":false}}
```

JSON对象必须完整、Version为1、布尔非null，未知键拒绝。CLI构造未指定项为off。Bench用例显式配置覆盖全局实验配置；飞书文本不能开启能力。

新任务全off规范化为策略2/nil，任一on规范化为策略4/完整配置；local_checks不依赖source_views。claim_focus只加入≤1KiB短核对提示，不增加工具或模型阶段。开启实验不能与analysis/report/quality组合；严格模式仍策略2。

普通任务状态版本仍1，质量任务仍2，读取记忆存储仍1，新增字段可选。策略4作为旧读取器失败关闭的协议边界，旧二进制不能忽略开关后将实验误恢复成旧组合。

历史0/1/2/3不升级。历史3恢复沿旧Schema、提示和组合。CLI恢复拒绝任何显式新参数（包括off），只读原配置。Engine恢复传入非零不同读取版本会拒绝；恢复旧策略不得传新配置，恢复4只允许nil或完全一致配置。事件恢复和Inspection禁止同任务中途更换策略/开关，未知版本失败关闭。

## 来源取回

策略4来源视图开启时，模型主要使用精确证据入口：

```json
{"evidence_id":"E1","offset":0,"limit":4000,"review":{"target_id":"E1","question":"核对具体字段"}}
```

evidence_id选择该E实际已读片段，offset相对该片段。旧id继续表示32位不可变材料ID、offset相对材料；两身份互斥。id误传当前授权E时返回evidence_id_required和纠正调用，不执行读取或自动重试。未知/越权身份不泄露来源。

复制next_call续页，保留selected_evidence_id，不能将游标改绑到新生成E。选定证据EOF不等于源文件EOF。源视图仍新E/动作，flat到最初artifact切片，不重复保存正文、不继承处理资格。重复判断按真实源区间和当前Agent复用资格执行。UTF-8、行中续读、4000字节分页及实际消费offset保持；通用材料不升级来源。

## 文件句柄与局部核对

local_checks开启后，成功动作同步登记稳定file_ref。它绑定原读取Run、消费者、路径、版本及anchor，不是授权。文件表重排不改变句柄，失效/Bash/恢复后旧句柄不能重新取得资格，同SHA新读也不重用。

```json
{"checks":[{"kind":"json","subject":{"file_ref":"F1","start_line":10,"end_line":15},"assertions":[{"op":"exists","pointer":"/field"}]}]}
```

引擎仅组合当前有效、已成功交付、既有父子权限允许的同版本片段，检查连续性、重叠一致、UTF-8及行边界。不会扫描、补读工作区、增加覆盖或processed。旧evidence_ids入口兼容且与file_ref互斥，显式旧ID集合不自动扩大。完整JSON对象、数学关系和前提适用性继续分开，不能从样例证明整个系统或复杂代码运行语义。

来源不足保持not_run，resolution给出reason_code、请求范围、已用证据、可用/缺失字节、未知行边界和next_actions。模型反馈有界完整JSON，省略条目可见；完整回执在受限材料中。建议不自动执行，后续读取仍扣正常工具尝试。

每批最多8项、请求64KiB；单JSON主体64KiB、现有算术/JSON资源限额保持。反例属于正常工具完成；同批写入/Bash在历史回执复用和Handler之前拒绝，下一正常响应使用核对结果。来源错误不伪装pass，存储错误不降为not_run。

## 质量、费用与观测

普通核对采用独立精简Schema，不再复制严格质量草稿定义。实验关闭时不增加Schema或新提示；开启时保持工具可见，首次正文交付即可在同响应提出核对。claim_focus独立，不复用旧长提示、不保存私密思维；只提示对象/字段/版本、存在/非空/类型/合法性、静态/运行边界和范围限定。固定课程答案仅在私有oracle，引用通过不证明结论正确。

run_configured记录实际read_features及contract。来源视图分别记录created/delivered/processed/folded，按model_call_id和view ID去重；folded为请求投影，须关联实际编码尝试，失败请求不授交付资格。local_check_batch记录提交项，local_check_completed记录每项结果，不能把完成回执数当有效核对数。Schema、短提示、原文/笔记材料、实际编码字节、Token及费用独立；缺失未知null，关闭态不适用，费用未知不记零。

CLI/飞书/观测台继续区分局部核对、读取覆盖和引擎验收。观测台策略3/4未知材料失败关闭，prepared/陈旧ID/旧索引窗口不能暴露完整回执，开关关闭不会解除历史私有材料限制。

## 验证与构建

离线覆盖八组合及Chat/Anthropic/Responses真实SDK本地HTTP；测试只证明机制和编码，不证明模型采纳及真实降耗。全部项目检查及旧profile自检通过后，使用独立read_features_v2最多两样本（同构建off→on），不续跑旧实验。

每样本60模型/120工具/30轮/600秒/300000字节、8000读取档、同deepseek-v4.1-flash-1017及项目.env。先判质量，再报机制与费用。8项rubric逐项评审、五轴固定锚点、同一实施会话匿名来源审查及反向复查；不是独立人工验收。质量至少8/10、零重大错误/遗漏、不低于可评分baseline；同等质量不足时不宣称降耗，所有失败消耗保留。一对通过也不自动开启默认实验。

```bash
go build -o claw ./cmd/claw
go build -o claw-monitor ./cmd/claw-monitor
python3 scripts/read_features_eval/run.py selfcheck
```

构建后需重启对应CLI/飞书/监测进程，新普通任务才采用新默认；本轮不替换外部工作区二进制或重启日常服务。运行与审查命令见`scripts/read_features_eval/README.md`。

本轮已完成离线验收与唯一一对真实样本。候选交付通过，语义质量未达标，反序专项判断不一致使正式配对质量差保持not_run；实验仍默认关闭。完整证据和限制见[实施与验收记录](read-features-results-2026-11-07.md)。
