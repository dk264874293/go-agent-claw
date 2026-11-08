# 工作笔记 v3 普通任务评测

复用 `scripts/write_efficiency_eval/run.py` 的冻结、预算、覆盖、回执、沙箱 checker 和失败用量归档。只有显式 `run-one` 启动真实模型，其余命令均离线。

```bash
python3 scripts/work_notes_eval/run.py selfcheck
python3 scripts/work_notes_eval/run.py init \
  --baseline-bin /绝对路径/冻结v1-claw \
  --candidate-bin /绝对路径/候选v2-claw \
  --output .claw/evals/work-notes-v3-批次ID
python3 scripts/work_notes_eval/run.py run-one --experiment .claw/evals/work-notes-v3-批次ID
python3 scripts/work_notes_eval/run.py export-review \
  --experiment .claw/evals/work-notes-v3-批次ID --output /绝对路径/匿名评审包
python3 scripts/work_notes_eval/run.py review \
  --experiment .claw/evals/work-notes-v3-批次ID --review-file /绝对路径/已填写.review.json
python3 scripts/work_notes_eval/run.py summarize --experiment .claw/evals/work-notes-v3-批次ID
```

初次执行 `run-one` 后再执行一次，得到首对后导出匿名评审包。首对完整交付、两组均至少8/10且零major、候选不低于基线，候选最多12模型/40工具，才允许后续样本。后续每次 `run-one` 最多运行一个样本，按需导出新的匿名包并导入评审。导出目录不能已存在；已有评审不能覆盖。

顺序为 baseline普通1、candidate普通1、candidate普通2、baseline普通2、baseline普通3、candidate普通3。所有样本均是普通 `workspace/change`，没有 analysis、plan、report、read_scope 或 require-full-read。题面要求递归阅读全部Markdown并新建 `优化汇总.md`，不提供来源路径清单和文件数。候选持久 `read_context_version=2`，基线为1。源文件清单只在工作区外的私有manifest中保存；仍由独立回执计算完整覆盖。

使用原冻结15份Markdown，固定 Deepseek `deepseek-v4.1-flash-1017`、60模型、120工具、30轮、600秒、300000上下文字节、8000读取档。最多6样本、360模型准入。最终要求三对全部完整交付、质量合格且候选不低于同对基线、候选模型中位数≤10、工具中位数≤26、总Token中位数比基线降低≥30%。未知Token和费用保持null，基础设施失败后停止，不自动重试；失败用量也保留。

凭据仅在运行时读取 `init` 时项目目录的 `.env`，不从继承环境引入其他Provider凭据，不执行shell或变量展开，不把 `.env` 复制到子工作区。首个运行绑定 `.env` 的SHA，后续变化停止批次；只保存摘要，不保存密钥或端点。每例独立 observations/diagnostics 目录；诊断强制metadata，不留请求正文。Agent拥有独立HOME/TMP，checker复用无凭据、禁网、禁写macOS沙箱。

匿名包只包含随机review_id命名的产物、空评分模板、冻结来源、rubric和有限oracle，不含组别、调用统计和私有映射。评审者须阅读完整产物并核对来源，按五项0–2量表给分，提供实际身份及 `human` / `source_audit_agent`，未导入评审为 `not_run`。匿名只隐藏分组和运行统计，文风可能仍透露实现特点。

评审格式如下，所有占位值必须替换为真实内容；UTF-8 span采用左闭右开字节范围：

```json
{
  "review_id": "导出模板中的随机ID",
  "artifact_sha256": "该产物的SHA256",
  "reviewer": "实际评审者身份",
  "method": "source_audit_agent",
  "scores": {"coverage": 2, "citation": 2, "correctness": 2, "conditions": 2, "important_findings": 2},
  "score_basis": {
    "coverage": {"reason": "真实依据", "finding_ids": ["f1", "f2"]},
    "citation": {"reason": "真实依据", "finding_ids": ["f1"]},
    "correctness": {"reason": "真实依据", "finding_ids": ["f1"]},
    "conditions": {"reason": "真实依据", "finding_ids": ["f1"]},
    "important_findings": {"reason": "真实依据", "finding_ids": ["f2"]}
  },
  "major_errors": 0,
  "major_error_ids": [],
  "findings": [{
    "id": "f1",
    "kind": "claim",
    "claim": "必须与产物指定字节范围完全相同",
    "start_byte": 0,
    "end_byte": 54,
    "verdict": "supported",
    "rubric_ids": ["score_default"],
    "evidence": [{"path": "完整冻结来源相对路径.md", "source_sha256": "来源SHA256", "start_line": 164, "end_line": 174, "quote": "该行范围内真实短摘录"}]
  }, {
    "id": "f2",
    "kind": "omission",
    "rubric_id": "numeric_discrepancy",
    "reason": "全文未指出原文数值与静态推导的矛盾",
    "evidence": [{"path": "完整冻结来源相对路径.md", "source_sha256": "来源SHA256", "start_line": 387, "end_line": 389, "quote": "该行范围内真实短摘录"}]
  }]
}
```

`claim`必须绑定产物SHA、真实span、严格匹配的claim文本及来源SHA/行/quote；`omission`必须绑定已冻结rubric，不得伪造产物span。每个finding必须有唯一id，五项score_basis均须用finding_ids绑定真实证据；major_errors须等于major_error_ids数量，且ID须指向incorrect claim或omission。导入后及后续续跑/汇总均重新核对绑定，不能只信state中的评分副本。

有限oracle新增边界：原文84.5/39.5是准确转述；按当前代码静态推导good=97.5、bad=29.5，与原文不一致。准确转述不应误判为错误；漏报矛盾记rubric遗漏。缺少顶层score默认5映射12.5；`score_format`只按非空/时间戳存在计分，不证明ID、URL或枚举合法。oracle冻结来源SHA、行/quote及Decimal常量前提，不执行课程代码或任意表达式；仅匹配完整标注claim，未知措辞保持not_run。旧六类反例继续兼容。

派生指标包括动态来源文件数、首次读取前模型调用、find前list_dir数量、发布生成时间、新接受notes、原始accepted/rejected carriers、duplicate carriers、missing notes、采纳率及其有载体accepted+rejected分母、carrier/metadata字节、folded reads/source bytes和持久processed/summary数量，不额外记录笔记正文。

## 独立 notes_delivery_v1 双样本

新建目录时显式选择 `init --profile notes_delivery_v1`，其余参数及 `run-one/export-review/review/summarize` 命令不变。不能接续或重置旧的停止批次。默认不传profile仍为原 `work_notes_v3` 六例顺序。

该profile仅 baseline普通1 → candidate普通1，最多2例、120模型准入；两组均为本轮冻结源码的普通 `workspace/change`、读取策略2，其他模型、项目 `.env`、15篇冻结来源和预算不变。基线零笔记或缺少新版响应指标按实保留，不视为环境失败。候选必须完整交付、至少一个非产物来源同时有有效持久摘要及processed声明、实际请求folded_source_bytes>0、模型≤12/工具≤40，且两项均不超过基线。两组均需匿名评审≥8/10、零重大错误，候选不低于基线；总输入输出Token降低≥30%才报告passed。每个样本最多启动一次，失败停止，第三次run-one被拒绝，不扩样或补样。

匿名模板额外包含 `quality_checks`：`classification_exceptions`、`formula_endpoints`、`local_delivery_links`。每项填写 `verdict`（supported/incorrect/not_applicable）、具体reason及关联finding_ids；incorrect不通过。仍须引用真实来源，不能用空泛断言代替核对。产物没有相应陈述时可解释not_applicable，不伪造claim或要求新增无关课程内容。CLI链接的机械回归独立于文档语义评审。

`work_notes_response`按model_call_id去重；采纳率分母仅为notes_opportunity=true的响应，分子为有效增量accepted响应，重复、最终无适用工具和无候选响应不冒充采纳。缺新版事件时指标为null。同一响应多工具的旧missing计数继续保留，但不进入该分母；旧有载体比例改以carrier_acceptance_*单独报告。持久摘要和processed的交集另报processed_summary_sources，排除生成产物自身。

`eval/fixtures/notes_delivery_v1/cases.json`保留脱敏ebe症状及不同参数的分类例外、公式端点和错目录链接反例，只供离线有限回归，随新profile冻结，不复制进Agent工作区或题面。有限标注外的新措辞保持not_run，回归通过不等于真实模型质量通过；不执行课程代码或任意表达式。
