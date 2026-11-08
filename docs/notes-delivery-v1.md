# 工作笔记采纳与交付准确性修复 v1

本轮以任务 `ebe6313322c991652c2597463d423ffc` 为症状依据：首次完整交付的正文没有及时进入可处理目录，唯一笔记载体是空更新；模型终答又把原文中的其他机器路径作为本次产物链接。原任务不改写，不重跑；真实收益仅通过新建双样本实验验证。

实施及真实对照见[2026-11-07结果](notes-delivery-results-2026-11-07.md)：笔记机制生效，质量与整体效率未通过，不宣称降耗成功。

## 读取候选与软约束

读取策略保持 `read_context_version=2`，存储保持 `ReadMemoryState.version=1`。历史0/1沿用原策略，严格分析及报告仍使用既有结构化契约。

普通v2请求的4KiB候选目录分成 `previously_delivered`（先前成功完整交付、未处理）与 `delivered_this_request`（本请求首次完整提供）。来源必须属于当前Agent、本次运行、当前版本及权限；两组去重并显示真实总数和省略数。后者只表示本响应可处理，不提前授予成功交付或折叠资格。硬预算或模型请求失败时不标记成功交付。

普通 `_work_notes.findings` 不再必填。最小载体可附在本响应第一个既有适用工具参数中：

```json
{
  "path": "下一份资料.md",
  "_work_notes": {
    "schema_version": 1,
    "summaries": {"E1": "这里填写该证据实际主题、事实、适用条件和未解决边界"},
    "processed_reads": ["E1"]
  }
}
```

没有候选时省略载体；未完成可用 `deferred_reads` 附具体原因。缺失、空、超限或无效载体继续软处理，不增加专用模型、工具或修复调用。只有成功完整交付、显式处理、当前投影有有效笔记、来源与权限仍有效且不是最新完整工具批次时才能折叠。暂缓、投影省略、写入/Bash/恢复失效沿用既有边界。

载体仍限制8KiB；工具回执限制512字节，仅列接受、处理、暂缓、未取得资格的来源及原因、省略数，不复述笔记正文。剥离载体后的副本用于业务指纹、审批、Pending及Handler，原Session与Responses原始项不改写。

## 响应指标

`model_started` 增加 `notes_previously_delivered`、`notes_this_request`；有效模型响应增加一条关联相同 `model_call_id` 的 `work_notes_response`：

- `notes_candidates`：可处理来源数；`notes_opportunity`：有候选且本响应含适用工具。
- `notes_response_accepted`：保存了新有效增量，不把重复载体记为新采纳。
- `notes_processed_sources`、`notes_deferred_sources`：本响应新增的唯一有效来源数。
- `notes_submitted`、`notes_response_rejected`、`notes_duplicates`：载体计数。
- `error_class`：accepted、missing、rejected、duplicate、deferred_only、no_candidates或no_eligible_tool。

采纳率按唯一模型响应计算，分母只含 `notes_opportunity=true` 的响应。旧逐工具missing计数仍供诊断，不能把同批其余调用未携带载体算作多次违约。没有分母或历史缺字段时，采纳率为null。折叠量、实际编码请求大小与实际Token分别统计，不互相替代。全局观测不保存笔记、请求或响应正文。

## 可信交付与内容边界

系统上下文提供引擎规范化后的真实工作区。资料内路径只是资料内容，模型交付使用完整工作区相对名，由程序从确认文件回执生成实际入口。

`DisplayRunFinalText` 是纯展示投影：仅当本地Markdown链接的可见名称与完整可信产物相对路径精确且唯一匹配时，降为路径文字；不按basename猜路径，不把错误绝对地址改成推测地址。HTTP(S)、不匹配的来源引用、代码及无法可靠解析的文本保留。有效业务JSON不作为Markdown处理。CLI与飞书终报共用投影；观测台保留原 `final_text` 并增加可选 `final_text_display`。原FinalText、历史任务及产物内容均不改写。

CLI产物区依据实际工作区生成链接，飞书只显示相对路径，观测台继续使用受限服务端产物ID。链接和文件回执不代表当前文件一定存在或语义验收通过。

普通汇总在原生成轮核对数量/分类例外、公式端点/默认值、比较两端版本以及原文/静态推导/执行结果的边界，重要判断就地引用。引用检查只证明出处有效；自由文本的结论正确性通过离线来源审查单独判定，不新增生产语义阻断，不执行资料脚本。

## 验证与回退

Go测试覆盖首次候选、批内一次笔记、失效/恢复、安全折叠、三协议实际编码及精确产物链接。自适应脚本Provider必须根据真实候选目录决定是否记笔记，并在后续请求看到正文实际减少，不能用无条件预制笔记冒充提示奏效。

新增脱敏ebe症状及通用分类例外、线性映射端点反例。有限oracle只判断冻结标注表述，未识别的改写保持not_run；真实产物通过匿名来源审查，保存产物SHA、输出字节区间、原文依据、评审身份及方式。

真实评测使用独立 `notes_delivery_v1` profile，顺序baseline→candidate，最多2样本、120次模型准入。两组均为普通workspace/change、策略2、8000字节读取，固定Deepseek模型及项目.env，使用同一15份冻结资料。旧v3六样本实验不改序、不续跑。基础设施失败停止，不自动重试。质量、采纳、交付和成本分别判定，未知费用为null。

本轮检查点保存含既有未提交修改的源码、各批差异及冻结二进制，排除凭据、任务数据和构建产物。回退使用本轮before副本或分批逆向差异，不能用git reset覆盖先前用户修改。真实样本失败不重写或补跑；软约束与单对样本都不能保证稳定降耗。
