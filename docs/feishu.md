[返回首页](../README.md) · [文档导航](README.md)

# 飞书群聊入口

`cmd/agentops` 是独立的飞书 Webhook 服务，复用引擎、工具和终报机制。适合学习如何把本地 Agent 接入消息队列、群聊授权、事件去重和动作审批。CLI 首次体验无需配置飞书。

## 环境变量

在启动目录的 `.env` 中启用 [env.export](../env.export) 的飞书部分：

| 变量 | 作用 |
| --- | --- |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | 应用身份与密钥 |
| `FEISHU_VERIFY_TOKEN` / `FEISHU_ENCRYPT_KEY` | 事件校验与加密配置，服务要求均有值 |
| `FEISHU_AGENTOPS_CONFIG` | 群聊绑定 JSON 文件路径 |
| `FEISHU_LISTEN_ADDR` | 默认 `127.0.0.1:48080` |
| `CLAW_PROVIDER` / `CLAW_MODEL` | 可选渠道与模型，默认 DeepSeek |
| 对应渠道的 API Key / Base URL | 与 CLI 共用[模型配置](configuration.md) |

`.env` 优先于同名进程环境变量。更改后重启服务。此服务不提供 CLI 的 `-auto` 入口。

## 配置群聊绑定

例如保存为本地 `agentops.json`。所有 ID 和路径都需要替换为实际值；工作区先创建，事件台账目录必须与工作区互不包含。

```json
{
  "event_ledger": "/绝对路径/事件台账",
  "chats": {
    "oc_群聊ID": {
      "work_dir": "/绝对路径/项目工作区",
      "allowed_users": ["ou_发起者"],
      "mode": "readonly"
    }
  }
}
```

首次可以使用 readonly，只允许指定成员提交读取任务。需要写入时，将对应群聊改为 workspace，并添加 `approvers`；执行本机命令与验收还须 trusted：

```json
{
  "work_dir": "/绝对路径/项目工作区",
  "allowed_users": ["ou_发起者"],
  "approvers": ["ou_审批者"],
  "mode": "trusted",
  "verify_command": "go test ./cmd/... ./internal/..."
}
```

上面第二段是替换 `chats` 中一个群聊绑定的值，不能作为整个配置文件。验收命令应适合实际工作区。群聊文本不能改变服务端配置的权限、工作区或验收命令。

## 启动与事件接入

```bash
# .env 中已设置 FEISHU_AGENTOPS_CONFIG 和所需密钥
go run ./cmd/agentops
```

服务接受 `POST /webhook/event`，默认只监听本机 `127.0.0.1:48080`。需要在飞书应用中配置能转发到该服务的事件回调地址和机器人消息相关权限 / 订阅；本仓库的本机监听地址本身不能直接供飞书服务访问。具体回调连通方式按你的部署环境设置。

同群任务按序提交，事件台账用于去重。文件写入和本机命令逐次审批，审批绑定群聊、人员、动作和版本。用户消息不能把审批票据复用到其他动作。

群聊还支持服务端的 `analysis`、`delegation`、`read_page_bytes`、`read_scope`、`output_style`、`read_features` 和请求诊断配置，分别沿用相应能力的约束。配置解析拒绝未知字段，具体字段见 [ChatBinding](../internal/feishu/config.go)。质量模式和交付前处理没有对应的群聊启动字段。

源码入口：[服务入口](../cmd/agentops/main.go)、[配置](../internal/feishu/config.go)、[队列与机器人](../internal/feishu/bot.go)、[审批](../internal/feishu/approval.go)、[事件台账](../internal/feishu/ledger.go)。
