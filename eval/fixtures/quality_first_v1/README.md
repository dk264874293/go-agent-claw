# 质量优先 v1 脱敏夹具

- `semantic_oracle.json` / `rubric.json`：冻结 15 份 Markdown 的来源标注与六项重要问题量表。原文数据复用 `../write_efficiency_v1/workspace/`，由独立实验复制冻结；不向 Agent 暴露私有答案。
- `cases.json`：12 个有限、完整命题反例；合成来源和 SHA 均显式保存。未执行课程脚本，不是完整历史轨迹回放。

有限命题检查只能支持契约回归；真实产物必须按固定量表和原文另行审查。未知措辞记 `not_run`，不从关键词猜结论，不用机械引用通过替代语义准确性。
