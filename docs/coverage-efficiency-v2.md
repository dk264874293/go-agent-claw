# 任务覆盖与上下文效率 v2

普通任务仍可按片段阅读和正常编辑。只有显式配置完整阅读要求后，缺页或未解析范围才阻断正常交付；引擎不根据目标关键词自动切换严格报告。

## 配置

```bash
./claw -dir /绝对路径/资料目录 \
  -read-scope 01资料包 -read-scope 02资料包 \
  -read-name-glob '*.md' -require-full-read \
  -output-style concise -report 优化汇总.md \
  -prompt '整理这些资料的优化建议，不修改原始资料'
```

`-read-scope` 可重复，支持工作区内相对文件或目录。文件不受目录 glob 排除；目录必须由本次正常 `find_files` 的准确 path/name_glob 回执解析，不会额外扫描。截断、不可读、未发现的目录保持未知，需要用户选择更具体的来源或完善本次发现。没有来源范围时指定完整阅读或 glob 配置会在调用模型前被拒绝。

新任务 `output_style=concise`，可选择 `detailed`。它控制报告文字与终答展开程度，不截断代码、业务数据、原文或重要发现。读取档位仍为 8000/16000，默认 8000，软目标仍为 96 KiB，硬上限不变。

恢复沿用持久范围、读取档位及输出偏好，显式冲突被拒绝。缺少这些字段的旧记录不补造范围或输出偏好。CLI/Bench/飞书绑定使用同一 `read_scope` 对象及 `output_style`；本版没有新增飞书严格报告启动指令。

```json
{"read_scope":{"version":1,"paths":["01资料包","02资料包"],"name_glob":"*.md","require_complete":true},"output_style":"concise"}
```

## 范围与交付

`ReadingCoverage` 在现有字段外新增 `scope_kind`、`scope_id`、`scope_revision`、`manifest_ids`、`unresolved_paths`。范围身份和修订是程序生成的摘要，不是模型自述。

- `observed`：当前有效发现清单与读取材料的并集，同选择条件刷新替换旧清单；仅说明已发现材料覆盖，不代表整个任务资料完整。
- `declared`：明确声明的来源选择条件及本次解析成员；不证明目录此后没有新增文件。
- `legacy`：展示旧清单范围；旧事件不重写。空 `scope_kind` 的历史记录按此含义展示。

覆盖合并同路径、同当前版本的实际字节区间。空文件也须成功读取。子任务不能靠自己的局部清单宣布根任务全部完整；来源集合不是新的权限入口。报告目标和本次确认新建产物不自动进入输入资料；原有资料被修改后撤销旧读取资格。

只有 `require_complete=true` 才增加任务级门槛。普通任务在正常结束前检查，缺页返回 `BLOCKED/reading_incomplete/2`，已发生写入和可信产物清单仍保留。严格报告先保存草稿，再于正式写入前检查；不足时不发布文件。不自动补读或追加模型轮次。取消、预算、存储错误仍采用原有优先级。

报告的 `scope_requirement=manifest` 须带当前覆盖目录的 `scope_id/scope_revision`。不匹配或缺页的发现降为待核实。可选 `ReportState.scope_dependencies` 保存完整性判断所需的全部来源版本，逐条摘录的 `source_versions` 保持原义。发布前后和恢复均核对两类依赖；未被摘录的必读来源变化也使报告陈旧。旧报告没有新范围声明时兼容原格式。

缓存返回的历史答复不重新读文件，覆盖标为 `recorded_only`，不再把原运行的完整状态解释为当前核验。

## 笔记与观测

公共请求构建器提供最多 4 KiB 的待处理目录，只列本 Agent、当前版本有效且已成功交付的读取 ID、范围及未解决计数；省略完整条目并保留数量。模型可在现有含工具响应提交 `work_notes`，不新增笔记工具、专用摘要或修复调用。

折叠仍要求原文完整进入成功模型请求、模型声明处理、当前投影有有效摘要或发现。缺失、无效或被 16 KiB 笔记投影省略的材料不能被折叠。最新工具批次和未处理正文继续保留；软目标超出仅提供反馈，超过硬上限沿用原停止语义。

`model_started` 新增白名单计数 `unprocessed_read_count`、`folded_source_bytes`、`omitted_note_entries`。材料字节、实际编码请求字节、Token 和耗时分别记录；全局观测不保存笔记、读取正文或请求响应。未知 Token/费用仍是 null。

## 验证与真实对照

六类有限语义标注位于 `eval/fixtures/coverage_quality_v2/semantic_oracle.json`，覆盖版本迁移、默认值、已有测试命令、Hook 未实测、缺页否定及错误严重性。它复用冻结原文，只匹配完整已标注命题，未知表达记 `not_run`。标注方式是 `source_audit_agent`，不是独立人工评审，不执行课程代码，也不宣称完整历史轨迹回放。

`scripts/coverage_efficiency_eval/run.py` 新建独立 v2 批次，普通与严格各一对，最多四样本/240 模型准入。先完成普通首对及质量评审再扩展；两边严格组都使用 `-report`。来源、完整要求及精简要求写入共同题面，新范围参数仅传给候选。评分检查候选持久配置及覆盖展示，并独立从原始读取回执计算实际覆盖。

```bash
python3 scripts/coverage_efficiency_eval/run.py selfcheck
python3 scripts/coverage_efficiency_eval/quality_oracle.py selfcheck
python3 scripts/coverage_efficiency_eval/run.py init --baseline-bin /冻结/baseline-claw --candidate-bin /冻结/candidate-claw --output .claw/evals/新批次
python3 scripts/coverage_efficiency_eval/run.py run-one --experiment .claw/evals/新批次
```

只有 `run-one` 调用真实模型。凭据经外部启动器从项目 `.env` 覆盖进程环境，不归档 `.env`。评分在无凭据、禁止网络的隔离副本中运行；预检实际执行冻结 checker。失败不自动重试，旧停止批次不恢复；失败样本保留用量。普通与严格组分别报告 30% Token 目标、质量及调用门槛，不合并收益；四个样本只能支持本任务小样本结论。
