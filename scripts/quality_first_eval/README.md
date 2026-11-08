# 质量优先评测

独立 `quality_first_v1` profile，只新增文件，不改变旧 profile 排序或停止状态。使用说明、冻结要求、审查契约见 [质量优先真实对照](../../docs/quality-first-evaluation.md)。

```bash
python3 scripts/quality_first_eval/run.py selfcheck
python3 scripts/quality_first_eval/run.py --help
```

仅 `run-one` 会启动真实模型。selfcheck 使用假 CLI、有限标注和隔离检查，并通过真实 CLI/SDK 与本机 HTTP 脚本服务核对事件兼容性，零付费请求。
