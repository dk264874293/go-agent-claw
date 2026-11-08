# 交付前定向处理 v1：实施与验收记录

日期：2026-11-08。实现已接入，默认关闭。本轮真实实验在基线完成后的评测协议错误处停止，候选未运行；不能据此宣称质量提升或降本。

## 已交付实现

- Engine、CLI、Bench显式启用`DeliveryReviewSpec{Version:1}`或`-delivery-review on`，普通任务默认off。飞书、只读Inspection、观测台复用统一结果展示；不增加飞书启动设置。
- 新启用任务使用内部状态版本3，旧普通版本1和质量版本2保持原义。旧任务不升级，恢复不能改变设置或重置一次性准入。
- `_work_notes.source_reviews`按消费者、运行、路径及完整版本归一来源；addressed要求真实有效发现、依赖闭合及对应文档引用。未核实和不相关仍只是模型声明。
- `DeferredRead.wait_for`记录绝对源字节半开区间。补页成功进入模型请求后，只标记ready，不自动清除暂缓、processed或授予折叠资格。
- 普通首次可准入写入批次或正常终答，先接受有效笔记，再检查已记录待办。存在待办且原预算允许时，整批新Handler退回一次，配齐工具结果，不创建Pending、不自动重放。
- 下一根普通请求消费已持久化的一轮及一次模型票据，不重复扣数。专用退回码不计普通错误恢复和连续失败；原权限、参数、同批核对拒绝及历史回执保持分类。
- 无待办不新增请求，也不提前消耗一次机会；有待办但预算不足则保留原执行或停止规则，不提高上限。取消、存储失败和未知效果保持优先级。
- 自由Markdown和业务JSON不改写；无工具终答不被猜测成笔记。阶段完成与未解决清单分开，不升级语义验收或VERIFIED。
- 候选仅在受限材料保存`content/tool_calls`，不新增持久化Responses不透明推理。观测只保存计数和阶段；候选与完整核对材料不通过观测台证据入口暴露。

接口、参数及恢复说明见[交付前处理v1](delivery-review-v1.md)。局部核对和来源视图仍由独立ReadFeatures控制。

## 离线验证与构建

接受的Go输入为冻结目录`candidate-source-v2/`。完整检查均通过：

```bash
go test ./cmd/... ./internal/...
go test -race ./internal/...
go build ./cmd/... ./internal/...
```

原有七个runner自检通过：real、write_efficiency、coverage_efficiency、work_notes、quality_first、source_reuse、read_features。新delivery_review冻结版42项自检通过；真实SDK与本地HTTP服务覆盖Chat、Anthropic、Responses、工具配对、原Session和opaque项保留，全程不调用外部模型。

适应性脚本依据真实退回回执提交处置并重新写入；另一场景依据实际JSON字段反例修正结论，原fail核对回执保持。还覆盖无待办、最后一次额度、混合批次、历史回执、取消和恢复、事件失败、快照失败、空来源占位写入、跨页等待、失效及私有材料访问。

冻结自检曾在与Go/race高度并发时遭遇假CLI帮助预检的10秒超时。原日志保留；随后同一不可变输入单独串行42项通过，没有放宽超时或付费重试。早期集成和未接受快照不作为最终通过依据。

项目根目录`claw`和`claw-monitor`已更新为接受的冻结构建，未替换外部工作区二进制，未重启任何日常服务。使用中的旧进程须在任务退出后重新启动。

| 二进制 | SHA256 |
|---|---|
| claw / 两组实验二进制 | `af6cb8b8d165f893565e6b58c3c5c336311edd1239e4097d35ad053f9bcea807` |
| claw-monitor | `ae311dc0fc7fd5da0f7075c8f0b3ffac0517081eb4777fb37bab6f956d9d1963` |

## 新真实实验：按协议停止

实验目录：`.claw/evals/delivery-review-v1-20261108T022254Z-6c32d81e/`。两组同一源码及二进制、`deepseek-v4.1-flash-1017`、项目`.env`、15份冻结资料、8000读取档和原共享预算。共同`source-views off / local-checks on / claim-focus off`，仅交付前处理off/on不同。配置及凭据未写入本文。

已执行的唯一基线：

| 项目 | 实际记录 |
|---|---|
| 样本 | baseline-ordinary-1，delivery-review off |
| 任务 | c4641759aab874aae41dd39550c21b95 |
| 运行 | 31120a46cb05c6a93384e56ef5db6811 |
| 持久终态 / 进程退出码 | NEEDS_VERIFICATION / 2 |
| 未决动作 | 0 |
| 读取 | 15/15完整，已发现材料87105字节 |
| 模型 / 工具 / 轮次 | 20 / 95 / 20 |
| 输入 / 输出 / 总Token | 934449 / 48936 / 983385 |
| 引擎累计运行 | 约311.34秒 |
| 费用 | null，20次调用单价未知 |
| 产物 | 优化汇总.md，15832字节 |
| 产物SHA256 | `043b2c50f59b5090774ec555adca8f742cbabe0ef45644ade8aeacd4fedbb78b` |

进程结束后的独立文件/格式checker给出artifact_result=pass、scope_result=pass；semantic_result=not_run。它仅检查产物、文件边界和引用行范围，不能证明引用支持结论或语义质量通过。

冻结Reader随后在统计机制时遍历`report.get('findings', [])`，遇到有效Go记录的`findings:null`而抛出`TypeError: 'NoneType' object is not iterable`。正常摘要允许省略发现，Go nil slice可编码为null；这是评测读取器兼容缺陷，不是Agent引擎失败或模型接口错误。

原state/result完整保留protocol_valid=false、stopped=true及原错误，checker不重跑，候选不启动。原始`finished`序号551和观测用量支持上表；另存只读摘录，不改写原实验结果。

| 正式判定 | 结果 |
|---|---|
| 本批协议 | error，批次已停止 |
| candidate | not_run |
| 匿名双样本语义审查与反序复核 | not_run |
| 正式配对质量差、整体质量提升、合格降本 | not_run |
| 默认推广 | false |

批准计划明确要求协议错误停止、不自动重试。任何后续候选执行需新的明确授权和独立延续记录，保留本批停止历史；本轮没有自动重跑基线、扩大样本或续跑候选。

## 冻结后读取器修复

仅在项目源的新runner修复已知Go slice的null兼容，同时拒绝非数组错误类型；加入脱敏真实形状夹具，不含原文或凭据。另补一次性票据在消费前取消的omitempty兼容：省略false不等于缺损，票据消费也不等于已经开始Provider/HTTP调用，不能伪造请求或清零Usage。

修复后的读取验证只在隔离副本执行，单独保存重建报告；原实验源码、manifest、state、result及checker输出不修改。诊断第一次导入原冻结Python模块时生成了`harness/__pycache__/run.cpython-312.pyc`非输入缓存，此后使用隔离副本并禁用字节码写入；该缓存不作为封存输入，也未自行删除原目录内容。

最新源runner完整自检50项通过（较冻结版新增8项，80.964秒）。已在隔离副本验证修复后解析通过，正常基线旧/新基础投影一致；封存输入SHA全部匹配，原始记录前后SHA不变。日志与只读重建记录为`validation/post-freeze-delivery-selfcheck.log`、`validation/post-freeze-baseline-reader-validation.json`。该修复不改变已构建Go二进制，不追认原批次为成功，也不产生新的外部模型调用。

## 可回退检查点与证据

根目录：`.claw/repair-checkpoints/delivery-review-v1-20261108T013258Z-34a6e08f/`。

- `before/`含实施前已有未提交源码；A、B、C及candidate-source-v2按阶段保存完整源码、清单、差异和构建。由于并行实施，A/B是可构建集成检查点，不宣称为互相独立的Git提交。
- 最终D检查点补充读取器兼容修复、夹具和本文。所有快照排除`.env`、凭据、任务数据、缓存及构建产物；被忽略的测试夹具纳入清单。
- `validation/accepted-checks.json`列接受的检查及日志SHA；保留早期失败日志，不覆盖为通过。
- `validation/baseline-engine-summary.json`只读记录真实终态、覆盖、用量、产物和原结果SHA。
- `binaries-manifest.json`及`build-environment.json`保存各构建身份和环境。

未执行Git提交、重置、清理或历史任务重写。现有用户修改保留。机制仍是显式软处理机会，不能保证模型登记全部重要条件；真实质量效果仍待有效候选与配对审查确认。
