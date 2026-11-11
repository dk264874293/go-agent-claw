<!--
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-17 11:38:50
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-17 13:38:17
 * @FilePath: /go-agent-claw/AGENTS.md
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
-->
# AGENTS.md

## 项目

go-agent-claw 是一个 Go 编写的 ReAct 风格 AI 智能体引擎（Think → Act → Observe 循环），作为学习项目逐步复刻 Claude-Code 式的能力：会话、上下文压缩、子智能体、计划模式、链路追踪与成本统计。代码注释、日志信息和面向用户的字符串均使用中文——请保持这一风格。

`CLAUDE.md` 是指向本文件的引用，无需单独维护。

## 常用命令

```bash
go build -o claw ./cmd/claw        # 构建 CLI
go run ./cmd/claw                  # 直接运行（不带 -prompt 进入交互对话模式 REPL，带 -prompt 单次执行后退出）
go build ./cmd/... ./internal/...  # 构建模块内所有包
go run ./cmd/learn -scenario react # 无密钥教学回放，使用正式引擎与临时工作区
go run ./cmd/bench                 # 基准测试 runner（会真实调用 LLM API，产生费用）
```

- **不要使用 `go build ./...`**：会因 `workspace/main.go` 是空草稿文件而失败。`workspace/` 是智能体的演示/测试工作目录（未纳入 git），不属于模块。
- 测试：`go test ./cmd/... ./internal/...`。共享预算、账本、存储和审批改动用 `go test -race ./internal/...` 验证。
- 干净检出：`python3 -B scripts/check_clean_checkout.py`（Python 3.12+），仅导出 HEAD；未提交候选须显式 `--overlay`。`internal/engine/testdata` 中两套合成夹具是测试输入，应随源码保留。课程入口与机制保证边界见 `docs/learning/README.md`、`docs/mechanism-boundaries.md`。
- CLI、Bench 和独立飞书服务默认读取 `DEEPSEEK_API_KEY`，使用 `deepseek-v4.1-flash-1017`。自动选择 `gpt-*` 模型时读取 `OPENAI_API_KEY`，`glm-*` 读取 `ZHIPU_API_KEY`，飞书服务配置见 README。
- REPL 内置斜杠命令：`/help` `/exit` `/cost` `/status` `/clear` `/verify` `/resume`。设置验收和清空对话在本地处理，`/resume` 恢复持久任务。输入独占一行的 `"""` 进入多行输入模式，再一行 `"""` 结束提交。

## 架构

```
cmd/claw/main.go            — CLI 入口：装配 provider → CostTracker → registry → engine
cmd/bench/main.go           — 基准测试 runner（SetupScript / TaskPrompt / ValidateScript 测试用例）
internal/schema/            — 共享类型：Message、Role、ToolCall、ToolResult、ToolDefinition
internal/provider/          — LLMProvider 接口 + OpenAI 兼容 (openai.go) 与
                              Anthropic 兼容 (claude.go) 两套实现
internal/engine/loop.go     — AgentEngine：ReAct 主循环 + 子智能体的 RunSub
internal/engine/            — 调度、预算、验收、恢复、子智能体、Reporter
internal/context/           — Session（历史 + 工作记忆）、Compactor、RecoveryManager、
                              PromptComposer（系统提示词、计划模式）、技能加载器
internal/tools/             — BaseTool 接口、支持中间件的 Registry、
                              read/write/edit_file/bash 工具、子智能体工具
internal/observability/     — CostTracker（包裹 provider，为 Session 计费）、Span 链路追踪，
                              导出到 <workDir>/.claw/traces
internal/taskstore/         — 任务日志、检查点、证据、工作区锁
internal/feishu/            — 群聊队列、去重、审批（cmd/agentops）
```

### 分层规则

- 依赖向内流动：`schema` 不依赖任何包；`engine` 编排 `provider`、`tools`、`context`、`observability`。
- **WorkDir 挂在 Session 上**。工具按 ExecutionScope 准入，文件操作使用工作区根句柄。
- 工具结果以携带 `ToolCallID` 的 `RoleUser` 消息回传给模型（没有独立的 tool 角色）。
- 工具参数是 `json.RawMessage`；各工具惰性反序列化自己的参数。

## 关键机制与坑

- **工作记忆**：新普通任务默认 `read_context_version=2`；显式ReadFeatures实验使用策略4，来源视图/局部核对/结论短提示独立控制。严格分析/报告/质量仍策略2，历史0/1/2/3不升级（读取记忆存储格式仍1），未处理正文不能先被20条窗口丢弃；只有完整成功交付、模型声明已处理且有有效笔记后才能折叠。历史普通任务沿用最近20条消息及 `Compactor.CompactWithin`，目标单独注入。改动截断逻辑时必须保住 ToolCall↔ToolResult 配对——配对断裂会导致 LLM API 返回 400 Bad Request；智谱 GLM 还要求首条非 system 消息必须是普通 user 消息（否则报 400 code=1214）。`GetWorkingMemory` 用两档策略处理窗口滑动：回退补数 ≤3 条时向前补齐到普通 user 消息块边界；超过 3 条则保持窗口大小，剔除头部孤儿 tool 结果并注入合成 user 前缀。
- **笔记载体 v3**：普通v2及策略3在read_file/read_lines/write_file/edit_file的顶层可选`_work_notes`提交短增量，缺失/无效仅软拒收；实际执行、审批、动作指纹及Pending使用剥离元数据的副本。原Session不改，Responses只同步清理匹配function_call参数中的已消费载体，opaque和未知项保留。暂缓来源不能折叠；无额外摘要/评审模型调用。说明见`docs/work-notes-efficiency-v3.md`。
- **笔记采纳与交付 v1**：普通v2候选区分先前交付与本请求首次完整交付，最小摘要无需findings；请求成功后才能接受处理声明。采纳率按model_call_id和响应机会计算，不用批内逐工具missing作分母。终答只对完整相对名精确匹配的已确认产物本地链接做纯展示降级，原FinalText不改，业务JSON不处理；参见`docs/notes-delivery-v1.md`。
- **范围与报告**：`read_scope` 描述资料要求，不扩大权限。普通覆盖合并当前发现及读取，仅显式 `require_complete` 在交付时强制完整；目录须由正常发现回执解析。报告的范围判断绑定 `scope_id/scope_revision`，`scope_dependencies` 保存未摘录的必读来源版本。恢复不改原设置或补造读取资格。新任务 `output_style=concise`，详细模式显式选择；笔记提示不保证模型实际提交笔记，降耗必须用真实Token核对。接口与新实验见 `docs/coverage-efficiency-v2.md`。
- **并发工具执行**：连续的独立只读调用限量并发，写入和 Bash 作为顺序屏障。同一工作区由 taskstore 独占锁保护。
- **包名怪癖**：`internal/eval/benchmark.go` 位于 `internal/eval/` 目录却声明 `package observability`。导入路径是 `internal/eval`，但引用时写作 `observability.*`（见 `cmd/bench/main.go`）。
- **错误自愈**：工具报错会经过 `RecoveryManager.AnalyzeAndInject`，在模型看到输出前追加救援提示。
- **请求诊断**：入口在加载配置后解析 `CLAW_REQUEST_DIAGNOSTICS`，默认 `metadata`；`on_error` 必须显式选择，只在本地独立目录保存失败请求及同组上一成功请求的脱敏副本。参数依据 SDK 实际序列化结果，区分 absent/null/0/false/unknown。`internal/requestdiag` 不依赖 engine/provider/tools/observe；诊断故障不改任务终态或预算。禁止把诊断身份映射为普通证据 ID、通过观测台暴露正文、从完整错误文本猜分类、将 HTTP 2xx 当成有效模型结果，或为诊断增加真实调用与重试。说明见 `docs/request-diagnostics-v1.md`。
- **模型入口**：CLI、Bench 和 AgentOps 通过 `provider.ResolveModelConfig` 与 `provider.NewConfiguredProvider` 选择模型和渠道，默认模型为 Deepseek 的 `deepseek-v4.1-flash-1017`。自动选择时 `deepseek*` 使用 Deepseek、`glm-*` 使用智谱，其余使用 OpenAI；显式 `-provider` 可覆盖渠道。真实对照评测继续显式使用 `gpt-6.1-sol`。
- **执行模式**：CLI 默认 `workspace`，允许工作区内读写；可用 `-mode readonly` 显式只读，可信本机命令须用 `-mode trusted` 启用。子智能体只使用只读工具。
- **显式质量模式**：`-quality` 默认关闭，仅支持分析或报告，禁止规划、额外思考、验收命令及子任务。`verify_findings` 唯一草稿关闭探索，受限算术／JSON核验、最多一次风险复核及一次作者修订均用原共享预算；新语义或新检查不能继承旧复核。质量任务状态版本2，关闭交付前处理的普通任务仍1，读取记忆存储仍1；恢复和Inspection必须失败关闭。独立短请求不注入普通历史，不改原Session与Responses opaque项。内部通过不代表语义验收；质量task artifacts不通过观测台暴露，可信产物另走回执。详见`docs/quality-first-v1.md`。
- **普通交付前处理v1**：`-delivery-review off|on`默认off，仅普通readonly+answer/workspace+change，禁analysis/report/quality/plan/thinking。开启任务状态版本3，旧任务不升级；文档处置沿_work_notes，wait_for只用源字节范围，补齐并成功交付仅变ready、不自动processed。首个可准入交付边界按需整批退回一次，正常扣工具，原子预扣一轮/一模型，下次根请求消费票据不重扣；恢复不重发。聚焦轮可用当前可见工具，一次强退回不保证总模型只+1；ReadFeatures独立。未解决项单独展示，不改自由FinalText或业务JSON、不称质量通过。观测台仅开放已提交来源及可信产物，候选/未分类材料失败关闭。见`docs/delivery-review-v1.md`。
- 引擎构造签名为 `NewAgentEngine(provider, registry, EngineOptions)`。通过 `Run(ctx, session, TaskSpec, reporter)` 执行，调用方从 `RunResult` 获取任务终态。

## 新增东西

- **新工具**：在 `internal/tools/` 实现 `BaseTool`（Name/Definition/Execute），在 `cmd/claw/main.go` 通过 `registry.Register(...)` 注册。注册表会识别该名称，但可见性、权限、只读/变更分类、效果账本与恢复仍需接线；不得将注册当作全部完成。完整清单见 `docs/extending-tools.md`。
- **新 Provider**：在 `internal/provider/` 实现 `LLMProvider.Generate(ctx, messages, tools)`，加一个读环境变量的构造函数，在 `cmd/claw/main.go` 接线。
- **工具拦截**：通过 `registry.Use(...)` 添加 `MiddlewareFunc`——中间件在执行前运行，可以带理由拒绝调用，模型必须读该理由。

- **来源复用策略3**：read_evidence源视图有独立EID/动作，复用原artifact切片、不继承processed；generic输出不升级来源。check_evidence为普通策略3只读工具，正常扣共享预算，无质量草稿或专用模型阶段。同批写入/Bash拒绝，下一响应采用核对结果。完整回执先登记再落盘，仅摘要对观测台开放；历史及Bash后同SHA不恢复依据。详见`docs/source-reuse-v1.md`。

- **独立读取实验v2**：三项默认off：`-source-views/-local-checks/-claim-focus off|on`；任一on使用策略4+完整ReadFeatures v1，旧二进制必须拒绝恢复。CLI恢复禁止显式新flags；历史3沿原组合，不自动回退或升级。来源取回用evidence_id精确片段及其游标；局部核对用稳定file_ref组合当前已交付的同版本来源。所有next_actions只是建议，后续读取正常扣预算。普通任务仍自由交付，不新增语义发布门禁。见`docs/read-features-v2.md`。
