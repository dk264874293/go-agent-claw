[返回首页](../README.md) · [文档导航](README.md)

# 工具与执行权限

模型只负责提出调用；实际可见性和执行权限由工具 Registry、ExecutionScope 与引擎控制。

## 三种执行模式

| 模式 | 能力 | 适合场景 |
| --- | --- | --- |
| `readonly` | 读取、目录发现、搜索和受限只读子任务 | 阅读代码、总结材料 |
| `workspace`（CLI 默认） | 增加工作区内文件创建与修改 | 写文档、改代码；不运行本机命令 |
| `trusted` | 增加 Bash 和用户验收命令 | 在可信项目中执行构建、测试 |

```bash
./claw -mode readonly -prompt "只阅读 README.md，概括使用方式"
mkdir -p workspace
./claw -dir ./workspace -prompt "创建 notes.md，介绍 ReAct 循环"
./claw -mode trusted -prompt "修正 README 中的错别字" -verify 'git diff --check'
```

前两种模式的文件访问限于工作区，工具路径须为相对路径，上级跳转和越界会被拒绝。trusted 中的 Bash 以当前用户权限执行，不能将它理解为操作系统沙箱。子智能体始终使用只读工具。

## 内置工具

| 工具 | 作用 |
| --- | --- |
| `list_dir` / `find_files` | 列目录、递归发现文件；发现回执不等于读过正文 |
| `search_text` | 搜索正文并定位内容 |
| `read_file` | 按源字节分页读取，可显示行号 |
| `read_lines` | 按包含两端的行号读取，默认编号 |
| `write_file` / `edit_file` | 新建或修改文件，结合读取版本防止覆盖意外变化 |
| `bash` | trusted 下运行命令 |
| `spawn_subagent` | 发起有局部范围的只读子任务 |
| `read_report` / `read_evidence` | 获取受权限与版本约束的报告或材料 |
| `write_report` | 仅在证据报告任务中发布指定新报告 |
| `check_evidence` | 开启局部核对后检查已有来源的受限算术或明确 JSON |
| `verify_findings` | 仅质量任务中登记结构化草稿并进入核验阶段 |

工具按任务模式提供，注册过的工具也不一定对当前模型可见。

## 阅读和写入的约定

`read_file` 的 `offset` / `limit` 使用字节；`read_lines` 的 `start_line` / `end_line` 从 1 开始。行号不能直接当字节偏移。读取结果包含文件版本、实际范围和续页信息；默认页长为 8000 字节，可通过 `-read-page-bytes 16000` 选择更大档位。

读取示意参数由模型提交，无需在终端直接执行：

```json
{"path":"README.md","offset":0,"limit":8000,"line_numbers":true}
```

```json
{"path":"README.md","start_line":1,"end_line":20}
```

已有文件发生变化时，旧读取版本不再能支持原来的写入或证据复用。工具报错会经过 RecoveryManager 补充纠正提示，再交回模型；是否继续仍受原预算约束。

独立只读调用可限量并发，写入和 Bash 顺序执行。一次被拒绝的工具尝试仍计入预算，不会因为“没有真正读到内容”而免费。

## 扩展一个工具

实现 `BaseTool` 的 `Name`、`Definition`、`Execute`，在对应 Registry 注册；按用途配置权限与中间件。具体步骤见[开发指南](development.md)。

源码入口：[工具接口与 Registry](../internal/tools/registry.go)、[执行范围](../internal/tools/scope.go)、[文件操作](../internal/tools/files.go)、[引擎 Registry 装配](../internal/engine/execution.go)。
