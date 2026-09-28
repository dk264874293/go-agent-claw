# M2 开发 spec：取消、输出边界与会话调度

状态：已实现并通过无网络自动化验收（2026-09-28）。对应开发计划 S1-05、S1-06。

## 运行与队列

- Session 提供按登记顺序的运行票据；等待可被 context 取消，释放与取消均推进后续票据。历史锁不跨网络请求或工具执行持有。
- Engine 的 Submit 在取得票据后追加用户消息，再执行整次 Run；Start 在返回前登记票据，以供飞书事件入口异步执行。旧 Run 仅用于已准备历史的调用方，仍取得同一个 Session 运行票据。
- 总超时覆盖排队时间。等待取消的任务不追加消息、不调用模型；不同 Session 可并发。Reminder 状态属于单次 Run。
- 所有入口通过 Submit/Start 提交用户消息；账单私有化，通过加锁快照读取。Session 对消息的嵌套工具参数、Usage 和结果元数据做深拷贝。

## 工具执行

- Options 新增 ToolTimeout（默认 30 秒）和 MaxConcurrentTools（默认 4）；零值使用默认值，负值拒绝。主循环和子任务共用调度逻辑，子任务继承父级执行选项和 context。
- 只有工具显式声明可并发只读时才进入并发批次；连续只读调用最多启动配置数量的 worker。写工具、Shell、子任务和未知工具形成串行屏障；前一批完成后才执行后续调用。结果仍按模型原顺序回写，未开始的取消调用也产生对应错误 Observation。
- Registry 保留 BaseTool 接口，通过可选的详细结果接口取得 Truncated、TimedOut、Cancelled 元数据；元数据随内部 Observation 保留，文本同时说明截断。工具超时包含中间件等待。自定义工具必须合作响应 context，禁止靠遗弃 goroutine 伪造退出。
- Bash 在 Linux/macOS 使用独立进程组；取消时终止组内进程，退出时清理同组后台进程。输出持续排空但只保留前 8000 字节，WaitDelay 防止遗留管道无限阻塞。主动脱离进程组的程序需要后续隔离后端治理。
- read_file 用 os.Root 约束读取路径，只接受常规文件，最多读取 8001 字节以检测截断，避免 FIFO 阻塞。其他文件写工具的统一权限边界仍按阶段 2 交付。
- 审批等待受工具/运行 context 控制；取消后清理登记，重复审批不阻塞。飞书 Reporter 使用传入的 context 和有限网络超时。

## 验收

- 大文件和 Shell 成功/失败大输出均有界，UTF-8 截断合法且元数据正确；越界读取、符号链接逃逸和 FIFO 被拒绝。
- 取消运行后 2 秒内收回本地受控测试的 Shell 进程、同组后代及等待 goroutine；正常 Shell 退出也不遗留同组后台进程。
- 可并发读任务不超过配置上限，读批次、写操作和后续读取保持屏障顺序；主/子任务遵循相同规则。
- 同 Session 任务按登记顺序追加和运行；等待取消不污染历史，其他 Session 不受阻塞。覆盖飞书实际提交入口。
- 审批取消、重复解决、账单并发快照和 Session 深拷贝通过测试及 race 检查。
- 执行 go test ./cmd/... ./internal/...、go vet、go build 与相关 race 测试；不调用真实模型或飞书 API。

## 验收记录

- macOS 开发环境通过 `go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、`go build ./cmd/... ./internal/...`、`go test -race ./internal/context ./internal/engine ./internal/tools ./internal/observability ./internal/feishu ./cmd/claw`。
- 会话队列、Engine、Shell/文件工具和飞书并发路径另以 `go test -race -count=10 ./internal/context ./internal/engine ./internal/tools ./internal/feishu` 重复验收。重复执行曾暴露测试复用全局 Session ID，修正测试隔离后通过。
- `GOOS=linux GOARCH=amd64 go build ./cmd/... ./internal/...` 通过；Linux 上的进程组运行行为仍需在目标环境验证。真实 LLM 和飞书 API 未调用。
- `bash` 仍能访问宿主机进程权限允许的路径；主动脱离进程组、不响应 context 的自定义工具及阻塞的文件系统 I/O 超出本里程碑治理范围。Token/费用预算和 Trace 唯一性属于 M3。
