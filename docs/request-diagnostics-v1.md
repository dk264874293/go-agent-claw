# 模型请求记录与失败诊断 v1

本功能记录实际 SDK 序列化后的请求参数及输入来源，帮助区分参数错误、内容拒绝、认证失败、网络问题和无效响应。它是旁路诊断，不增加模型调用、重试或工具调用，不改变共享预算、任务账本和验收结果。

## 启用与关闭

```bash
# 默认：只记录参数和结构，不保存请求正文
./claw -dir /项目路径 -prompt "任务目标"

# 当前启动显式允许：失败时保存本地脱敏请求及同组的上一成功请求
./claw -request-diagnostics on_error -dir /项目路径 -prompt "任务目标"

# 自定义本地诊断根目录
./claw -request-diagnostics on_error -request-diagnostics-dir /绝对路径/diagnostics

# 完全关闭这条诊断链
./claw -request-diagnostics off
```

环境变量为 `CLAW_REQUEST_DIAGNOSTICS=off|metadata|on_error`、`CLAW_REQUEST_DIAGNOSTICS_DIR=/绝对路径/diagnostics`。入口在加载 `.env` 和运行时默认值后解析配置。CLI 参数或飞书绑定、Bench 用例的显式值优先于环境变量。未配置时默认为 `metadata`；如果同时设置 `CLAW_OBSERVE=off` 且没有明确选择诊断模式，则默认为 `off`。明确选择 `metadata` 或 `on_error` 时可独立运行，不要求开启观测台记录。

诊断策略属于本次启动配置，不写入 `TaskSpec`，不随任务恢复自动沿用过去的正文授权。同一 REPL 中的任务及恢复使用本次启动的选择。普通最终答复、工具参数或模型输出不能打开诊断。

Bench 用例和飞书工作区绑定支持 `request_diagnostics`、`request_diagnostics_dir` 两个可选字段；飞书不增加远端读取正文命令。模式不合法时，入口在模型及工具执行前拒绝配置。直接调用引擎而没有显式 `StartRequestDiagnostics` 上下文时，不会创建诊断文件。

## 默认保存内容

独立标准库包 `internal/requestdiag` 提供上下文身份、HTTP 采集、参数清单、脱敏和异步存储。Provider 的采集点位于 SDK 序列化之后，使用 `GetBody` 取得有界副本，不消费发送中的请求体，也不读取响应流。三个协议均接线：Chat Completions、Responses、Anthropic Messages。

每个 HTTP 尝试关联本次诊断身份、观测 attempt、任务、Agent 和 `model_call_id`。一个逻辑模型调用与其 HTTP 尝试分别计数；诊断不生成用量事件。渠道来自实际 Provider 构造配置，不从模型前缀猜测。

保存的白名单包括：

- 实际模型、协议、端点的尝试内指纹、请求字节数、HTTP 尝试序号、SDK 重试计数（有证据时）。
- `temperature`、`top_p`、Token 上限、`stream`、`tool_choice`、`parallel_tool_calls`、输出格式、`store` 和推理配置的安全摘要。
- 参数状态 `absent / null / value / unknown`，分别表示未发送、显式 null、实际值、无法确认；实际 `0`、`false` 不当作默认或未知。
- 输入项 JSON 路径、角色、类型、长度、指纹，以及可确认的动作、证据、版本和字节区间。折叠标记与原文分别标记，不能用证据身份把未发送原文冒充为实际输入。
- 工具名称及 Schema 指纹；不会在元数据中保存工具描述、参数正文或完整 Schema。
- HTTP 状态、服务 request ID 及来源、安全错误摘要、可空 code/type/param 字段状态。
- 当前输入与此前成功请求的结构差异。差异只帮助缩小范围，不说明哪个字段或片段导致失败。

指纹使用单次诊断运行的随机 HMAC 密钥，不能跨运行比较或用作内容还原。完整输入来源清单位于本地 manifest；常驻观测流仅接收有界白名单摘要。来源未知、SDK 原始项无法唯一映射或元数据省略时明确保留未知或省略数量。

“未发送”不说明模型服务使用了什么默认值。SDK、客户端和后端的默认配置不可混为一谈；记录只证明本次实际送出的参数。构建记录只包含 Go、VCS 和两个 SDK 的版本白名单，不保存完整构建参数或链接配置。

## `on_error` 的正文边界

成功请求的脱敏副本只在内存中保留。Provider 已返回有效消息且调用没有被取消后，才更新“上一成功”候选；HTTP 2xx 本身不代表成功。每次调用开始时冻结当时可用的上一成功快照，按同一尝试、Agent、实际渠道、协议、端点和模型匹配，不借用其他 Agent 或稍后完成的请求。

请求失败时，才尝试将当前脱敏请求和该快照一起保存。最近成功请求没有可用副本时，明确标明不可用，不用更早请求冒充。`metadata` 模式从不保存请求正文；`off` 模式不创建诊断目录或文件。

诊断不保存请求 Header、Cookie、完整 URL、账号凭据或响应正文。已知环境密钥及常见凭据形式会被遮蔽，推理/思考块、签名和加密思考字段会省略。**业务正文仍可能包含无法自动识别的敏感信息**，因此正文需要显式选择 `on_error`，且只保存在本机受限目录。

脱敏和省略会改变请求字节；这些文件不是原始请求，也不能作为精确重放材料。完整性、脱敏状态、原始大小、保存大小、上一成功是否可用、丢弃原因分别记录。

## 存储与故障隔离

默认目录为 `~/.claw/request-diagnostics/`，不依赖工作区是否可写，也不占用任务执行锁：

```text
request-diagnostics/
  .quota.lock
  <capture_attempt_id>/
    <bundle_id>/
      manifest.json
      request.redacted.json              # 仅 on_error 失败且捕获成功
      previous-success.redacted.json     # 仅存在同组可用前次快照时
```

诊断目录权限 `0700`，文件 `0600`。子目录、文件名均由服务端生成；根目录和子目录核对文件身份，文件读写限制在根句柄内，不追随诊断文件的符号链接。不要将诊断目录设置为共享可写目录。

| 资源 | 默认上限 |
| --- | --- |
| 单个原始请求及保存正文 | 各 1 MiB |
| 本地元数据清单 | 64 KiB，按完整输入项省略 |
| 单次尝试的在途、缓存及队列材料 | 8 MiB |
| 异步保存队列 | 16 项 |
| 单次尝试落盘总量 | 16 MiB |
| 整个诊断目录 | 256 MiB |
| 入口退出刷新 | 与观测日志共享最多 1 秒 |

目录配额由独立非阻塞锁保护，遗留 `.pending-*` 也计入占用；不等待执行锁，不自动删除历史文件。满额、锁忙、超大请求、队列满、不可写、超时或采集失败只产生受限警告和记录缺口，不取消任务、不更改 `RunResult`。失败的诊断保存不影响原有 taskstore 存储错误语义；任务关键记录失败仍为 `FAILED/storage_error`。

达到单次落盘上限时为元数据保留空间。无法保存正文时不落盘残缺 JSON。进程强杀可能没有最终诊断状态，不能把缺失当作“没有失败”。只有确认相关进程已退出后才手动清理整个尝试目录或遗留临时目录；网页不提供删除按钮。

## 错误类别与用户交付

`RequestFailure` 保留 `errors.Is/As` 的原始 cause，但面向用户和持久日志只显示白名单摘要，不复制 SDK 的完整错误字符串。结构化错误优先读取 SDK 的有界 JSON，再兼容嵌套或顶层错误信封；两者都无法解析时保留未知。

| 类别 | 含义 |
| --- | --- |
| `model_content_rejected` | 明确内容策略 code/type，或已确认的 400/403 内容拒绝消息 |
| `model_bad_request` | 其他 HTTP 400 |
| `model_authentication_failed` | HTTP 401 |
| `model_permission_denied` | 其他 HTTP 403 |
| `model_rate_limited` | HTTP 429 |
| `model_service_error` | HTTP 5xx |
| `model_transport_error` | 没有有效 HTTP 响应的传输错误 |
| `model_response_invalid` | HTTP 2xx 但 SDK 解析或响应结构无效 |
| `model_api_error` | 其他 API 错误 |

取消、任务期限、请求超时和关键存储失败继续采用既有优先规则。通用 `upstream_error` 不足以认定内容拒绝。请求参数清单也不能证明某个参数就是失败根因，不主动重试、删除材料或放宽内容约束。

CLI、飞书和观测台沿用统一终报。内容拒绝仍是 `FAILED`，已确认的文件回执和产物单独交付，不变成成功答复或验收通过。观测台展示参数状态、渠道协议、结构差异、采集质量和本地诊断身份，**不提供正文读取或下载接口**，诊断身份不能作为工具证据 ID 使用。

## 兼容与验证

不修改既有 taskstore 日志、任务记录或历史结果；旧记录缺少参数和诊断信息时显示未知。任务 `b1de857bf15e9224c7f683a4d704d7a9` 没有留存完整失败请求，不能追溯补造原参数或具体触发片段。脱敏夹具仅复现“确认写入之后内容拒绝”的症状，同时测试 SDK 解包前后两种错误信封，不宣称已知历史原始信封。

验证使用 `httptest`、脚本 Provider、临时文件及故障注入，覆盖三协议实际请求字节一致、presence、来源、并发前次关联、2xx 无效响应、限额、路径、退出、存储失败和 Monitor 只读隔离。本轮不新增付费请求。
