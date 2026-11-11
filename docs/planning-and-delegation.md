[返回首页](../README.md) · [文档导航](README.md)

# 计划与子智能体

计划用于表达任务步骤和依赖，子智能体用于分担独立的只读问题。两者都消耗原任务预算，不是额外的免费执行通道。

先看[计划课程](learning/06-plan.md)，实现层次及真实验证状态见[计划边界](mechanism-boundaries.md#计划与步骤完成)和[协作边界](mechanism-boundaries.md#协作与子任务)。当前计划重排保留 Done，但尚无完整失败子图补丁约束；步骤对话完成不等于业务验收。

## 结构化计划

```bash
./claw -mode trusted -plan \
  -prompt "阅读项目文档，修正启动说明中的不一致之处" \
  -verify 'git diff --check'
```

开启 `-plan` 后，引擎请求结构化计划，校验步骤和依赖，再执行可运行步骤；遇到阻塞时可调整计划。计划保存在任务状态中，并导出到 `.claw/tasks/<任务 ID>/PLAN.md` 和 `TODO.md`，便于观察进度。

计划不意味着自动进入只读模式，权限由 `-mode` 决定。`-thinking` 是另一个独立开关，每轮先执行一次无工具推理，会增加模型调用；学习最小循环时可以先保持关闭。

## 只读子任务

只读回答默认 `-delegation auto`。引擎根据已正常发现的资料规模判断是否允许自动委派：不超过 32 个文件且不超过 128 KiB 的小范围由根 Agent 处理；未知、截断或不可读范围也不自动委派。

```bash
# 强制由根 Agent 处理，便于观察单个循环
./claw -mode readonly -delegation off -prompt "总结 README.md"
# 放宽规模准入，模型仍需提出合适的独立子任务
./claw -mode readonly -delegation on -prompt "分别阅读 cmd/claw 和 internal/provider，解释两者如何衔接"
```

大范围只读任务最多两个子任务，要求明确且不重叠的未读范围。每个子任务最多 4 次模型调用、8 次工具尝试，同时扣除父子共享预算；`on` 只放宽规模判断。子任务始终使用只读工具，不能通过委派获得写文件或 Bash 权限。

父 Agent 可通过 `read_report` 查看子报告目录和发现，通过 `read_evidence` 取材料。交接保留来源、版本、覆盖与缺口，减少父子重复读取；结构和引用校验并不保证语义结论正确。

## 组合与恢复

委派参数仅适用于 `readonly + answer`。严格分析可以使用只读委派，质量模式则禁止子任务并自动关闭默认委派；证据报告也关闭委派。恢复时沿用持久策略与已消耗计数，不补造旧任务中缺失的准入记录。

详细阈值、报告分页、覆盖和真实评测限制见[只读效率策略 v2](readonly-efficiency-v2.md)；父子交接与预算收尾见[任务分析链路](task-analysis-repair.md)。

源码入口：[计划](../internal/engine/plan.go)、[委派](../internal/engine/delegation.go)、[子任务工具](../internal/tools/subagent.go)、[ReAct 与 RunSub](../internal/engine/loop.go)。
