# 只读效率真实对照

本目录只使用 Python 标准库。`selfcheck` 通过本地假 CLI 检查协议，不访问模型；只有 `run-one` 显式启动冻结的真实 CLI。

```bash
python3 scripts/efficiency_eval/run.py selfcheck
python3 scripts/efficiency_eval/run.py init \
  --baseline-bin /绝对路径/实施前冻结CLI \
  --candidate-bin /绝对路径/候选CLI \
  --source-workspace /绝对路径/ai-knowbase \
  --output /绝对路径/本次实验
python3 scripts/efficiency_eval/run.py run-one --experiment /绝对路径/本次实验
```

父进程负责从项目 `.env` 选择有效的密钥、网关和代理，作为进程环境传入。脚本不读取或归档 `.env`，不保存环境变量清单。冻结文件只包含15份 Markdown、两份可执行文件及摘要；每个样本复制独立工作区并创建新会话。不传 `-analysis` 或 `-delegation`，因此基线沿用原行为，候选使用其默认 `auto`。

manifest 逐组冻结有效配置：基线允许旧版缺少委派字段，候选必须在任务状态和唯一 `run_configured` 中同时证明 `delegation=auto`，且分析模式关闭。候选遗漏策略或观测不一致会作为协议错误停止实验，不能仅凭启动命令推断模式生效。

每次 `run-one` 只运行下一个样本，顺序是 baseline-1、candidate-1、candidate-2、baseline-2、baseline-3、candidate-3。两组统一使用 `deepseek-v4.1-flash-1017`，最多60模型、120工具、30轮、600秒、300000上下文字节。最多6次样本、360次模型准入。没有自动重试；基础设施、协议错误或未确认运行会停止继续执行，既有完成样本不会重跑。

试跑一对后，候选须已答复、范围合规、全部15文件的同版本字节覆盖完整、无未决动作，并且最多12模型／40工具。之后需要人工分别评审两组：

```bash
python3 scripts/efficiency_eval/run.py review --experiment /绝对路径/本次实验 \
  --sample baseline-1 --scores 2,2,2,2,2 --major-errors 0 --notes '独立核对记录'
python3 scripts/efficiency_eval/run.py review --experiment /绝对路径/本次实验 \
  --sample candidate-1 --scores 2,2,2,2,2 --major-errors 0 --notes '独立核对记录'
```

分数顺序为覆盖、引用定位、结论成立、版本条件、重要问题；每项0–2分。候选须至少8分、零重大错误、不低于基线，才能扩展。后续候选也须先完成评审再继续启动样本。评分由人工输入，脚本不会调用评审模型，也不能把结构校验当成结论正确。

```bash
python3 scripts/efficiency_eval/run.py summarize --experiment /绝对路径/本次实验
```

终态从唯一任务事件取得，用量按唯一 `model_call_id` 去重，独立计算原文覆盖与文件范围。完整事件支持快照缺失或陈旧的情况。未知 Token 或费用保留 `null`，同时报告已知部分；失败样本的消耗也保留。最终要求候选调用中位数最多12模型／40工具，总 Token 中位数较同批基线下降30%，质量与覆盖不退步。本工具的小样本结果不能推广为其他任务的收益。
