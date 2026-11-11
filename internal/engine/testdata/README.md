# 引擎合成回归夹具

这两个目录是离线测试的输入，需要随源码一起发布：

| 目录 | 来源与用途 | 完整性检查 |
| --- | --- | --- |
| `task_analysis_v1` | 保留历史读取的文件拓扑与长度，名称、正文和报告重新合成；noise 中的 PNG、PDF 和同名二进制也都是合成文件，不能执行 | `manifest.json`、noise 清单中的字节长度与 SHA-256，由对应 Go 测试核对 |
| `analysis_quality_v1` | 在同一合成拓扑上补充默认值、版本和测试命令反例，以及固定工具轨迹 | manifest、oracle、trajectory 和准确性回放测试 |

2026-11-11 清点：task_analysis_v1 的 README、manifest、reports、15份正文及5份 noise 文件；analysis_quality_v1 的 README、manifest、oracle、trajectory 及15份正文，共42个输入文件。macOS 的 `.DS_Store` 不属于夹具，继续忽略。各目录 README 记录更细的脱敏和合成声明，历史来源标识仅用于追踪结构来源，不包含凭据或原业务正文。

这些夹具验证工具、消息、覆盖、证据和预算机制，不测量真实模型的成功率或费用。正文含按字节截断或填充保留的行尾空白，`.gitattributes` 禁止夹具换行转换，并对正文关闭行尾空白检查；不能为了排版改变已冻结的字节和摘要。不要把运行产生的任务、日志、缓存或真实资料放入这些输入目录。新增夹具目录须明确用途、来源、完整性检查，并显式更新 Git 准入规则。
