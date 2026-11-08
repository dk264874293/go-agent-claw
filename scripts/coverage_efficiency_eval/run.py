#!/usr/bin/env python3
"""覆盖效率 v2：独立批次，普通与严格各一对；只在 run-one 启动付费模型。"""
import importlib.util
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
COMMON = HERE / "common.py" if (HERE / "common.py").exists() else REPO / "scripts/write_efficiency_eval/run.py"
spec = importlib.util.spec_from_file_location("coverage_efficiency_common", COMMON)
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
r.PROFILE_VERSION = 2
r.ORDER = ["baseline-ordinary-1", "candidate-ordinary-1", "candidate-report-1", "baseline-report-1"]
r.LIMITATIONS = "普通和严格各一对，仅支持本任务小样本结论；b6ce为症状参考，旧停止批次不续跑；各组质量与Token分别判定。"
r.EFFICIENCY_GROUPS = ["ordinary", "report"]
GOAL_PREFIX = r.GOAL + ("默认生成精简报告：一项问题只展开一次，保留全部重要发现、版本条件、影响、建议和待核实项；"
                       "不要重复粘贴源文或大段代码，详情引用已有材料，不另生成长篇附录。指定来源为：\n")
r.strict_sample = lambda name: "-report-" in name


def harness_inputs():
    return {"run.py": Path(__file__), "common.py": COMMON,
            "checker.py": r.HERE / "checker.py", "helpers.py": r.HELPERS}


def build_goal(files):
    return GOAL_PREFIX + "\n".join(sorted(item["path"] for item in files))


def candidate_arguments(manifest):
    args = ["-read-page-bytes", "8000", "-output-style", "concise", "-require-full-read"]
    for item in manifest["files"]:
        args += ["-read-scope", item["path"]]
    return args


def validate_candidate_spec(spec, manifest):
    scope = spec.get("read_scope", {})
    if (spec.get("read_context_version") != 1 or spec.get("output_style") != "concise"
            or scope.get("version") != 1 or scope.get("name_glob") != "*" or scope.get("require_complete") is not True
            or scope.get("paths") != sorted(x["path"] for x in manifest["files"])):
        raise r.EvalError("候选实际读取范围、完整要求或输出偏好与冻结配置不符")


def validate_candidate_result(result, independent_coverage, manifest):
    reading = result.get("reading", {})
    declared = {f.get("path"): f for f in reading.get("files", [])}
    expected = {f["path"] for f in manifest["files"]}
    if (reading.get("scope_kind") != "declared" or not reading.get("scope_id") or not reading.get("scope_revision")
            or set(declared) != expected or reading.get("total_files") != 15
            or reading.get("status") == "complete" and not independent_coverage.get("complete")
            or reading.get("complete_files") != sum(bool(f.get("complete")) for f in declared.values())):
        raise r.EvalError("候选覆盖展示与独立回执不一致或缺少声明范围身份")
    independent = {f["path"]: f for f in independent_coverage["files"]}
    for path, file in declared.items():
        actual = independent[path]
        if file.get("complete") and (not actual["complete"] or file.get("version") != actual["version"] or file.get("total_bytes") != actual["total_bytes"]):
            raise r.EvalError("候选逐文件完整状态缺少实际回执支持")
    if result.get("status") == "NEEDS_VERIFICATION" and (reading.get("status") != "complete" or reading.get("complete_files") != 15 or reading.get("recorded_only") or reading.get("discovery_incomplete")):
        raise r.EvalError("候选正常交付未通过声明范围完整阅读检查")


r.harness_inputs = harness_inputs
r.build_goal = build_goal
r.candidate_arguments = candidate_arguments
r.validate_candidate_spec = validate_candidate_spec
r.validate_candidate_result = validate_candidate_result
_inspect_sample = r.inspect_sample


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    item = _inspect_sample(sample_root, manifest, session, returncode, sample_id)
    events = r.json_lines(Path(sample_root) / item["evidence"]["observations"])
    calls, last_finished, publish_calls = {}, None, set()
    for event in events:
        if event.get("task_id") != item["task_id"]:
            continue
        kind, payload, call_id = event["event_type"], event.get("payload", {}), event.get("model_call_id")
        if kind == "model_started":
            calls.setdefault(call_id, {}).update(payload)
        elif kind == "model_finished":
            calls.setdefault(call_id, {}).update(payload)
            last_finished = call_id
        elif kind == "tool_attempted" and payload.get("tool") in {"write_file", "write_report"} and last_finished:
            publish_calls.add(last_finished)
    metrics = item["metrics"]
    metrics["folded_reads"] = sum(p.get("folded_reads", 0) for p in calls.values())
    metrics["peak_unprocessed_read_count"] = max((p.get("unprocessed_read_count", 0) for p in calls.values()), default=0) if sample_id.startswith("candidate") else None
    metrics["folded_source_bytes"] = sum(p.get("folded_source_bytes", 0) for p in calls.values()) if sample_id.startswith("candidate") else None
    metrics["omitted_note_entries"] = sum(p.get("omitted_note_entries", 0) for p in calls.values()) if sample_id.startswith("candidate") else None
    metrics["publish_generation_seconds"] = sum(calls[c].get("duration_ms", 0) for c in publish_calls) / 1000 if publish_calls else None
    metrics["longest_model_seconds"] = max((p.get("duration_ms", 0) for p in calls.values()), default=0) / 1000
    # 从已经保存的任务状态统计笔记使用，不新增Provider、文件正文或原文日志。
    task_events = r.json_lines(Path(sample_root) / item["evidence"]["task_events"])
    states = [e["payload"] for e in task_events if e.get("kind") == "finished"]
    memory = states[-1].get("read_memory", {}) if states else {}
    metrics["note_agents"] = len(memory.get("notes", {}))
    metrics["processed_reads"] = sum(len(ids) for ids in memory.get("processed_reads", {}).values())
    metrics["summary_count"] = sum(len(summaries) for summaries in memory.get("summaries", {}).values())
    return item


r.inspect_sample = inspect_sample


def recheck_delivery(experiment, sample_id):
    """仅复算冻结样本的版本链，另存结果，不改变原评分和停止状态。"""
    root, manifest = r.checked_manifest(experiment, require_current=False)
    if sample_id not in r.ORDER or (root / "run.lock").exists():
        raise r.EvalError("样本无效或实验正在运行")
    sample = root / "samples" / sample_id
    target = sample / "delivery-recheck-v2.json"
    if target.exists():
        raise r.EvalError("离线复算证据已存在，禁止覆盖")
    item = r.read_json(sample / "result.json")
    protected = [root / "manifest.json", root / "state.json", sample / "result.json", sample / "review.json"]
    original = {str(p.relative_to(root)):r.digest(p) for p in protected if p.exists()}
    tasks = list((sample / "workspace/.claw/tasks").glob("*/events.jsonl"))
    if len(tasks) != 1:
        raise r.EvalError("无法确定唯一原任务记录")
    events = r.json_lines(tasks[0])
    finals = [e["payload"] for e in events if e["kind"] == "finished"]
    if len(finals) != 1 or finals[0].get("last_result", {}).get("run_id") != item.get("run_id"):
        raise r.EvalError("原任务运行身份无法核对")
    result = finals[0]["last_result"]
    artifact = sample / "workspace" / r.OUTPUT
    if artifact.is_symlink() or not artifact.is_file():
        raise r.EvalError("产物缺失或不是可核对的普通文件")
    passed = result.get("delivery", {}).get("history_complete") is True and r.receipt_chain_valid(result.get("delivery", {}).get("effects", []), r.OUTPUT, r.digest(artifact))
    evidence = {"schema_version":1,"sample_id":sample_id,"original_receipt_pass":item.get("receipt_pass"),
                "rechecked_receipt_pass":passed,"checker_sha256":r.digest(COMMON),"recorded_at":r.now(),
                "original_records":original,"agent_started":False,"paid_calls":0,
                "limitations":"只修复多次确认编辑的回执链判定；不修改原结果、语义评分、候选门槛或停止状态。"}
    evidence["original_records_unchanged"] = original == {name:r.digest(root/name) for name in original}
    r.write_json(target,evidence)
    return evidence


def selfcheck():
    import unittest
    suite = unittest.defaultTestLoader.discover(str(HERE), pattern="test_*.py")
    return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1


r.selfcheck = selfcheck
if __name__ == "__main__":
    if len(sys.argv)>1 and sys.argv[1]=="recheck-delivery":
        import argparse, json
        parser=argparse.ArgumentParser(description=recheck_delivery.__doc__)
        parser.add_argument("--experiment",required=True)
        parser.add_argument("--sample",choices=r.ORDER,required=True)
        args=parser.parse_args(sys.argv[2:])
        print(json.dumps(recheck_delivery(args.experiment,args.sample),ensure_ascii=False,indent=2))
    else:
        sys.exit(r.main())
