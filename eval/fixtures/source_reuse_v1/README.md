# 来源复用普通任务 v1 冻结夹具

本目录独立于旧profile。workspace仅含已与ai-knowbase核对的15份Markdown（87105字节）；manifest.json保存完整路径、字节数、SHA。报告产物、图片、凭据与任务记录不纳入资料。

semantic_oracle.json/rubric.json沿用已有来源绑定反例并新增timestamp_subject：good样例实际含collected_at，不能将bad或其他对象的缺失判断移植给good。原文标注84.5/39.5和静态97.5/29.5必须区分，未运行资料代码不得宣称运行已验证。

timestamp_cases.json保存明确JSON主体的原文、来源版本及行范围；有限自检按实际字段存在性驱动修订，区分缺失与null/false/0/空串。程序核对不代替主体选择和结论语义审查。

cases.json是已有12条有限反例的本profile冻结副本。未知自由结论为not_run，不能用固定文本标签自动给真实产物打语义分。真实评审通过匿名review-map、产物SHA、输出字节区间、来源SHA/行号/摘录绑定；source_audit_agent不是独立人类验收。33f仅症状参考，本夹具不宣称完整历史轨迹回放。

运行方法见 scripts/source_reuse_eval/README.md。仅run-one启动真实CLI；本目录不包含任何付费实验结果。
