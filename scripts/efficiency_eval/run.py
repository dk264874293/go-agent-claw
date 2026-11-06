#!/usr/bin/env python3
"""只读效率的自由轨迹对照；仅 run-one 会显式启动真实 CLI。"""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import statistics
import subprocess
import sys
import time
import uuid

ORDER = ["baseline-1", "candidate-1", "candidate-2", "baseline-2", "baseline-3", "candidate-3"]
GOAL = "读取并分析当前项目文件夹中的md文档"
SETTINGS = {"model": "deepseek-v4.1-flash-1017", "provider": "deepseek", "max_model_calls": 60,
            "max_tool_calls": 120, "max_turns": 30, "timeout_seconds": 600, "context_bytes": 300000}
SCORE_NAMES = ["coverage", "citation", "correctness", "conditions", "important_findings"]
EXPECTED_CONFIG = {"baseline": {"analysis": False, "delegation": None},
                   "candidate": {"analysis": False, "delegation": "auto"}}


class EvalError(Exception):
    pass


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name(path.name + ".tmp-" + uuid.uuid4().hex)
    with temp.open("w", encoding="utf-8") as stream:
        os.chmod(temp, 0o600)
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    temp.replace(path)


def now():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def file_list(root, only_markdown=False):
    result = []
    for directory, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = sorted(d for d in dirs if d not in {".claw", ".git"} and not Path(directory, d).is_symlink())
        for name in sorted(files):
            path = Path(directory, name)
            if path.is_symlink():
                if only_markdown:
                    continue
                raise EvalError("样本工作区出现符号链接，不能当作合规普通文件")
            if only_markdown and not name.endswith(".md"):
                continue
            if path.is_file():
                result.append({"path": path.relative_to(root).as_posix(), "bytes": path.stat().st_size, "sha256": digest(path)})
    return sorted(result, key=lambda item: item["path"])


def fixture_digest(files):
    return hashlib.sha256(json.dumps(files, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


def init_experiment(output, baseline_bin, candidate_bin, source_workspace):
    output = Path(output).absolute()
    if output.exists() and any(output.iterdir()):
        raise EvalError("实验目录非空，禁止覆盖既有实验")
    source_workspace = Path(source_workspace).resolve(strict=True)
    files = file_list(source_workspace, only_markdown=True)
    if len(files) != 15:
        raise EvalError("本轮真实对照必须冻结15份Markdown，实际发现%d份" % len(files))
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    manifest = {"schema_version": 1, "experiment_id": uuid.uuid4().hex, "created_at": now(),
                "goal": GOAL, "settings": SETTINGS, "order": ORDER, "max_samples": 6,
                "max_model_admissions": 360, "source_workspace": str(source_workspace),
                "files": files, "fixture_digest": fixture_digest(files), "binaries": {},
                "per_arm_expected_config": EXPECTED_CONFIG,
                "environment": {"python": platform.python_version(), "platform": platform.platform(),
                                "machine": platform.machine()}}
    for arm, source in [("baseline", baseline_bin), ("candidate", candidate_bin)]:
        target = output / "binaries" / arm
        target.parent.mkdir(mode=0o700, exist_ok=True)
        shutil.copyfile(Path(source).resolve(strict=True), target)
        target.chmod(0o700)
        manifest["binaries"][arm] = {"path": "binaries/" + arm, "sha256": digest(target)}
    for file in files:
        target = output / "fixture" / file["path"]
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        shutil.copyfile(source_workspace / file["path"], target)
        target.chmod(0o600)
    write_json(output / "manifest.json", manifest)
    write_json(output / "state.json", {"schema_version": 1, "samples": {}, "stopped": False})
    return manifest


def checked_manifest(root):
    root = Path(root).resolve(strict=True)
    manifest = read_json(root / "manifest.json")
    if manifest.get("schema_version") != 1 or manifest.get("order") != ORDER or manifest.get("settings") != SETTINGS or manifest.get("per_arm_expected_config") != EXPECTED_CONFIG:
        raise EvalError("实验契约或预算参数变化，拒绝继续")
    current = file_list(root / "fixture")
    if current != manifest["files"] or fixture_digest(current) != manifest["fixture_digest"]:
        raise EvalError("冻结夹具摘要变化，拒绝继续")
    for item in manifest["binaries"].values():
        if digest(root / item["path"]) != item["sha256"]:
            raise EvalError("冻结二进制摘要变化，拒绝继续")
    return root, manifest


def json_lines(path):
    raw = Path(path).read_bytes()
    if raw and not raw.endswith(b"\n"):
        raise EvalError("进程结束后记录仍为半行")
    events = []
    for index, line in enumerate(raw.splitlines(), 1):
        if not line.strip():
            raise EvalError("完整日志中存在空行")
        item = json.loads(line)
        if item.get("sequence") != index:
            raise EvalError("记录序号缺口或重复")
        events.append(item)
    return events


def coverage(events, files):
    result = []
    receipts = []
    for event in events:
        if event["kind"] == "action_result":
            read = event["payload"].get("last_action", {}).get("result", {})
            if read.get("status") == "SUCCEEDED" and read.get("read"):
                receipts.append(read["read"])
    for file in files:
        ranges = []
        for read in receipts:
            if read["path"] != file["path"] or read["version"] != file["sha256"]:
                continue
            a, b, total = read["byte_start"], read["byte_end"], read["total_bytes"]
            if not 0 <= a <= b <= total or total != file["bytes"]:
                raise EvalError("结构化读取回执的范围或版本无效")
            ranges.append((a, b))
        cursor, gaps = 0, []
        for a, b in sorted(ranges):
            if a > cursor:
                gaps.append([cursor, a])
            cursor = max(cursor, b)
        if cursor < file["bytes"]:
            gaps.append([cursor, file["bytes"]])
        result.append({"path": file["path"], "version": file["sha256"], "total_bytes": file["bytes"],
                       "complete": bool(ranges) and not gaps, "missing": gaps})
    return {"complete": all(item["complete"] for item in result), "files": result}


def usage_metrics(observations, task_id, expected_calls):
    started, finished, usage = {}, {}, {}
    for event in observations:
        if event.get("task_id") != task_id:
            continue
        call_id = event.get("model_call_id")
        kind = event["event_type"]
        if kind not in {"model_started", "model_finished", "model_usage"}:
            continue
        if not call_id:
            raise EvalError("模型事件缺少model_call_id")
        target = {"model_started": started, "model_finished": finished, "model_usage": usage}[kind]
        if call_id in target and target[call_id] != event["payload"]:
            raise EvalError("同一模型调用出现相互矛盾的用量或结果")
        target[call_id] = event["payload"]
    if set(started) != set(finished) or len(started) != expected_calls:
        raise EvalError("模型调用计数或开始/结束事件不匹配")
    if not set(usage).issubset(started):
        raise EvalError("模型用量没有匹配的请求身份")
    unknown, unknown_cost = 0, 0
    input_tokens = output_tokens = 0
    known_cost = 0.0
    for call_id in started:
        item = usage.get(call_id, {})
        a, b = item.get("input_tokens"), item.get("output_tokens")
        if a is None or b is None:
            unknown += 1
        elif type(a) is not int or type(b) is not int or a < 0 or b < 0:
            raise EvalError("模型Token用量无效")
        else:
            input_tokens += a
            output_tokens += b
        if item.get("cost") is None:
            unknown_cost += 1
        else:
            if not isinstance(item["cost"], (int, float)) or isinstance(item["cost"], bool) or not math.isfinite(item["cost"]) or item["cost"] < 0:
                raise EvalError("模型费用字段无效")
            known_cost += item["cost"]
    return {"input_tokens": input_tokens if not unknown else None,
            "output_tokens": output_tokens if not unknown else None,
            "total_tokens": input_tokens + output_tokens if not unknown else None,
            "known_input_tokens": input_tokens, "known_output_tokens": output_tokens,
            "unknown_usage_calls": unknown, "cost": known_cost if not unknown_cost else None,
            "known_cost": known_cost, "unknown_cost_calls": unknown_cost,
            "provider_errors": sum(bool(item.get("error_class")) for item in finished.values())}


def inspect_sample(sample_root, manifest, session, returncode, expected_arm=None):
    tasks = list((sample_root / "workspace" / ".claw" / "tasks").glob("*/events.jsonl"))
    if len(tasks) != 1:
        raise EvalError("无法确定唯一任务记录")
    events = json_lines(tasks[0])
    if any(event.get("version") != 1 for event in events):
        raise EvalError("不支持的任务事件版本")
    final = [event["payload"] for event in events if event["kind"] == "finished"]
    if len(final) != 1:
        raise EvalError("缺少唯一有效终态")
    state = final[0]
    result = state.get("last_result", {})
    task_id = tasks[0].parent.name
    if state.get("version") != 1 or state.get("session_id") != session or state.get("spec", {}).get("id") != task_id or result.get("task_id") != task_id or not result.get("run_id"):
        raise EvalError("任务、会话或运行身份不匹配")
    if state["spec"].get("goal") != manifest["goal"] or state["spec"].get("analysis", False):
        raise EvalError("任务题面或分析模式不符合本批约定")
    if state["spec"].get("kind") != "answer" or state["spec"].get("mode") != "readonly":
        raise EvalError("任务类型或执行权限不符合本批约定")
    for name, value in [("MaxModelCalls", 60), ("MaxToolCalls", 120), ("MaxTurns", 30), ("Timeout", 600000000000)]:
        if state.get("limits", {}).get(name) != value:
            raise EvalError("实际任务预算与冻结配置不一致")
    if any(state.get("usage", {}).get(name) != result.get("usage", {}).get(name) for name in ["model_calls", "tool_calls", "turns"]):
        raise EvalError("任务状态与返回结果的用量不一致")
    if result.get("status") == "ANSWERED" and not result.get("final_text", "").strip():
        raise EvalError("ANSWERED缺少最终答复")
    expected_exit = {"ANSWERED": 0, "VERIFIED": 0, "NEEDS_VERIFICATION": 2, "BLOCKED": 2,
                     "BUDGET_EXHAUSTED": 3, "CANCELLED": 130, "FAILED": 1}.get(result.get("status"))
    if expected_exit is None or expected_exit != returncode:
        raise EvalError("持久终态与进程退出码不一致")
    observation_files = list((sample_root / "observations").glob("*/events.jsonl"))
    if len(observation_files) != 1:
        raise EvalError("缺少唯一观测流")
    observed = json_lines(observation_files[0])
    if any(event.get("schema_version") != 1 for event in observed):
        raise EvalError("不支持的观测事件版本")
    if any(event["event_type"] == "observation_gap" or event.get("payload", {}).get("incomplete") for event in observed):
        raise EvalError("观测记录存在缺口")
    configured = [event for event in observed if event["event_type"] == "run_configured" and event.get("task_id") == task_id]
    if expected_arm is not None:
        if expected_arm not in EXPECTED_CONFIG or manifest.get("per_arm_expected_config", {}).get(expected_arm) != EXPECTED_CONFIG[expected_arm]:
            raise EvalError("缺少该组冻结的有效配置约定")
        expected = EXPECTED_CONFIG[expected_arm]
        expected_delegation = expected["delegation"]
        actual_delegation = state["spec"].get("delegation") or None
        if actual_delegation != expected_delegation:
            raise EvalError("%s任务没有实际采用冻结的委派策略%s" % (expected_arm, expected_delegation))
        if len(configured) != 1:
            raise EvalError("缺少唯一run_configured，无法证明本次有效配置")
        actual = configured[0]["payload"]
        # Go字段omitempty时，analysis缺失按schema v1的false解释，不能解释成开启。
        if actual.get("analysis", False) is not False or (actual.get("delegation") or None) != expected_delegation:
            raise EvalError("run_configured的分析模式或委派策略与冻结组别不一致")
    returned = [e for e in observed if e["event_type"] == "run_returned" and e.get("task_id") == task_id]
    if len(returned) != 1 or returned[0]["payload"].get("result_run_id") != result["run_id"]:
        raise EvalError("入口返回身份与任务终态不一致")
    if returned[0]["payload"].get("status") != result["status"]:
        raise EvalError("入口返回状态与持久终态不一致")
    metrics = usage_metrics(observed, task_id, result["usage"]["model_calls"])
    metrics.update({"model_calls": result["usage"]["model_calls"], "tool_calls": result["usage"]["tool_calls"],
                    "turns": result["usage"]["turns"], "elapsed_seconds": result["usage"]["elapsed"] / 1e9})
    if not 0 <= metrics["model_calls"] <= 60 or not 0 <= metrics["tool_calls"] <= 120 or not 0 <= metrics["turns"] <= 30:
        raise EvalError("样本实际计数超出冻结预算")
    after = file_list(sample_root / "workspace")
    write_json(sample_root / "files-after.json", after)
    scope_pass = after == manifest["files"]
    read = coverage(events, manifest["files"])
    return {"task_id": task_id, "run_id": result["run_id"], "status": result["status"],
            "stop_code": result.get("stop_code"), "reason": result.get("reason"), "exit_code": returncode,
            "final_text": result.get("final_text", ""), "metrics": metrics, "coverage": read,
            "scope_pass": scope_pass, "protocol_valid": True,
            "effective_config": {"analysis": state["spec"].get("analysis", False),
                                 "delegation": state["spec"].get("delegation"),
                                 "observed_delegation": configured[0]["payload"].get("delegation") if len(configured) == 1 else None},
            "pending_actions": len(state.get("pending", {})),
            "evidence": {"task_events": str(tasks[0].relative_to(sample_root)),
                         "observations": str(observation_files[0].relative_to(sample_root))}}


def sanitize(text, environ):
    for key, value in environ.items():
        if len(value) >= 4 and any(token in key.upper() for token in ["KEY", "TOKEN", "SECRET", "PASSWORD"]):
            text = text.replace(value, "[REDACTED]")
    return re.sub(r"(https?://)[^/@\s]+@", r"\1[REDACTED]@", text)


def sanitize_value(value, environ):
    if isinstance(value, str):
        return sanitize(value, environ)
    if isinstance(value, dict):
        return {key: sanitize_value(item, environ) for key, item in value.items()}
    if isinstance(value, list):
        return [sanitize_value(item, environ) for item in value]
    return value


def review_ok(review):
    return review and sum(review["scores"].values()) >= 8 and review["major_errors"] == 0


def transport_digest(environ):
    # 只保存网关/代理配置的摘要，不保存URL正文或任何API key。
    names = ["DEEPSEEK_BASE_URL", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"]
    return hashlib.sha256(json.dumps({name: environ.get(name) for name in names}, sort_keys=True).encode()).hexdigest()


def candidate_gate(sample):
    metrics = sample.get("metrics", {})
    return sample.get("protocol_valid") and sample.get("status") == "ANSWERED" and sample.get("scope_pass") and sample.get("coverage", {}).get("complete") and sample.get("pending_actions") == 0 and metrics.get("model_calls", 61) <= 12 and metrics.get("tool_calls", 121) <= 40


def expansion_reason(state):
    samples = state["samples"]
    if "candidate-1" not in samples:
        return None
    candidate, baseline = samples["candidate-1"], samples.get("baseline-1", {})
    if not candidate_gate(candidate):
        return "候选试跑未满足完整交付、覆盖或12模型/40工具门槛，停止扩展"
    cr, br = candidate.get("review"), baseline.get("review")
    if not cr or not br:
        return "试跑两组均须完成独立质量评审后才能扩展"
    if not review_ok(cr) or sum(cr["scores"].values()) < sum(br["scores"].values()):
        return "候选试跑质量未通过或较基线退步，停止扩展"
    for sample_id, item in samples.items():
        if sample_id.startswith("candidate-") and sample_id != "candidate-1":
            review = item.get("review")
            if not review:
                return "须先评审已完成的候选样本"
            if not review_ok(review):
                return "候选后续样本存在质量失败，停止扩展"
            paired = samples.get(sample_id.replace("candidate-", "baseline-"), {}).get("review")
            if paired and sum(review["scores"].values()) < sum(paired["scores"].values()):
                return "候选后续样本质量较同对基线退步，停止扩展"
    return None


def run_one(experiment):
    root, manifest = checked_manifest(experiment)
    state = read_json(root / "state.json")
    if state.get("stopped"):
        raise EvalError("本实验已停止：" + state.get("stop_reason", "未知"))
    if any(item.get("state") != "completed" for item in state["samples"].values()):
        raise EvalError("存在未确认或未完成样本，禁止重跑")
    next_id = next((sample for sample in ORDER if sample not in state["samples"]), None)
    if next_id is None:
        raise EvalError("6个样本均已完成，不再运行")
    if not os.environ.get("DEEPSEEK_API_KEY", "").strip():
        state["stopped"], state["stop_reason"] = True, "凭据预检失败，未启动模型；本实验停止，不自动重试"
        write_json(root / "state.json", state)
        raise EvalError(state["stop_reason"])
    if next_id not in ORDER[:2]:
        reason = expansion_reason(state)
        if reason:
            raise EvalError(reason)
    lock = root / "run.lock"
    try:
        fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
    except FileExistsError:
        raise EvalError("实验有运行锁或未确认运行，禁止并发或重跑")
    if read_json(root / "state.json") != state:
        lock.unlink(missing_ok=True)
        raise EvalError("实验状态在准入前已变化，本次不启动样本")
    try:
        current_transport = transport_digest(os.environ)
        if state.get("transport_env_digest") and state["transport_env_digest"] != current_transport:
            raise EvalError("网关或代理配置摘要变化，拒绝继续同批实验")
        state["transport_env_digest"] = current_transport
        arm = next_id.split("-")[0]
        sample_root = root / "samples" / next_id
        workspace = sample_root / "workspace"
        shutil.copytree(root / "fixture", workspace)
        session = "efficiency-" + uuid.uuid4().hex
        command = [str(root / manifest["binaries"][arm]["path"]), "-dir", str(workspace), "-session", session,
                   "-mode", "readonly", "-task-kind", "answer", "-model", SETTINGS["model"],
                   "-provider", SETTINGS["provider"], "-max-model-calls", "60", "-max-tool-calls", "120",
                   "-max-turns", "30", "-timeout", "600s", "-context-bytes", "300000", "-prompt", manifest["goal"]]
        environ = os.environ.copy()
        environ["CLAW_OBSERVE"] = "on"
        environ["CLAW_OBSERVE_DIR"] = str(sample_root / "observations")
        item = {"state": "running", "sample_id": next_id, "arm": arm, "session_id": session,
                "started_at": now(), "binary_sha256": manifest["binaries"][arm]["sha256"],
                "fixture_digest": manifest["fixture_digest"], "command": command}
        state["samples"][next_id] = item
        write_json(root / "state.json", state)
        write_json(sample_root / "command.json", {"argv": command, "cwd": str(workspace),
                                                   "environment_versions": manifest["environment"]})
        write_json(sample_root / "files-before.json", manifest["files"])
        print("启动 " + next_id + "，预算60模型/120工具；不自动重试", flush=True)
        proc = subprocess.Popen(command, cwd=workspace, env=environ, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        item["process_id"] = proc.pid
        write_json(root / "state.json", state)
        try:
            out, err = proc.communicate(timeout=660)
        except subprocess.TimeoutExpired:
            proc.send_signal(signal.SIGINT)
            try:
                out, err = proc.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                out, err = proc.communicate()
            item["infrastructure_error"] = "运行超时，终态未可靠确认"
        (sample_root / "stdout.log").write_text(sanitize(out.decode("utf-8", errors="replace"), environ))
        (sample_root / "stderr.log").write_text(sanitize(err.decode("utf-8", errors="replace"), environ))
        (sample_root / "stdout.log").chmod(0o600)
        (sample_root / "stderr.log").chmod(0o600)
        item["process_exit_code"] = proc.returncode
        try:
            item.update(inspect_sample(sample_root, manifest, session, proc.returncode, expected_arm=arm))
        except (EvalError, OSError, ValueError, KeyError, TypeError) as error:
            item["protocol_valid"] = False
            item["infrastructure_error"] = sanitize(str(error), environ)
        if item.get("metrics", {}).get("provider_errors") or item.get("status") == "FAILED":
            item["infrastructure_error"] = "模型请求、运行环境或关键记录失败；保留样本，不重试"
        item["state"], item["finished_at"] = "completed", now()
        item = sanitize_value(item, environ)
        state["samples"][next_id] = item
        if item.get("infrastructure_error"):
            state["stopped"], state["stop_reason"] = True, item["infrastructure_error"]
        elif item.get("status") == "CANCELLED" or not item.get("scope_pass"):
            state["stopped"], state["stop_reason"] = True, "样本已取消或文件范围不合规，停止继续执行"
        write_json(sample_root / "result.json", item)
        write_json(root / "state.json", state)
        print(json.dumps({key: item.get(key) for key in ["sample_id", "status", "metrics", "protocol_valid", "infrastructure_error"]}, ensure_ascii=False))
        return item
    except BaseException as error:
        # 运行已登记时不能自动把它删除后重跑；后续 run-one 会拒绝未确认状态。
        item = state["samples"].setdefault(next_id, {"sample_id": next_id})
        item["state"] = "unconfirmed"
        item["infrastructure_error"] = sanitize(str(error), os.environ)
        state["stopped"], state["stop_reason"] = True, "样本运行中断或基础设施失败，结果未确认，不自动重试"
        try:
            write_json(root / "state.json", state)
        except OSError:
            pass
        raise
    finally:
        lock.unlink(missing_ok=True)


def record_review(experiment, sample_id, scores, major_errors, notes):
    root, _ = checked_manifest(experiment)
    lock = root / "run.lock"
    try:
        fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
    except FileExistsError:
        raise EvalError("样本正在运行，不能并发修改评分")
    try:
        return record_review_locked(root, sample_id, scores, major_errors, notes)
    finally:
        lock.unlink(missing_ok=True)


def record_review_locked(root, sample_id, scores, major_errors, notes):
    state = read_json(root / "state.json")
    item = state["samples"].get(sample_id)
    if not item or item.get("state") != "completed":
        raise EvalError("只能评审已完成样本")
    if len(scores) != 5 or any(score not in {0, 1, 2} for score in scores) or major_errors < 0:
        raise EvalError("五项评分必须分别为0、1或2，重大错误数须非负")
    item["review"] = {"scores": dict(zip(SCORE_NAMES, scores)), "major_errors": major_errors,
                      "notes": sanitize(notes, os.environ), "reviewed_at": now(), "method": "independent_manual"}
    write_json(root / "samples" / sample_id / "result.json", item)
    write_json(root / "state.json", state)
    return item["review"]


def summarize(experiment):
    root, manifest = checked_manifest(experiment)
    state = read_json(root / "state.json")
    groups = {}
    for arm in ["baseline", "candidate"]:
        items = [item for key, item in state["samples"].items() if key.startswith(arm + "-")]
        metrics = {}
        for key in ["model_calls", "tool_calls", "input_tokens", "output_tokens", "total_tokens", "cost"]:
            values = [item.get("metrics", {}).get(key) for item in items]
            metrics[key + "_median"] = statistics.median(values) if values and all(value is not None for value in values) else None
        groups[arm] = {"samples": len(items), "metrics": metrics}
    a, b = groups["baseline"]["metrics"]["total_tokens_median"], groups["candidate"]["metrics"]["total_tokens_median"]
    reduction = 1 - b / a if a and b is not None else None
    quality = all(review_ok(item.get("review")) for key, item in state["samples"].items() if key.startswith("candidate-"))
    for number in [1, 2, 3]:
        ca, ba = state["samples"].get("candidate-" + str(number)), state["samples"].get("baseline-" + str(number))
        if ca and ba:
            quality = quality and bool(ca.get("review")) and bool(ba.get("review"))
            if ca.get("review") and ba.get("review"):
                quality = quality and sum(ca["review"]["scores"].values()) >= sum(ba["review"]["scores"].values())
    candidate = groups["candidate"]["metrics"]
    completed = len(state["samples"]) == 6 and all(item.get("state") == "completed" for item in state["samples"].values())
    valid = completed and all(item.get("protocol_valid") and item.get("scope_pass") and item.get("coverage", {}).get("complete") and item.get("status") == "ANSWERED" for item in state["samples"].values())
    passed = valid and quality and reduction is not None and reduction >= .30 and candidate["model_calls_median"] <= 12 and candidate["tool_calls_median"] <= 40
    quality_known = bool(state["samples"]) and all(item.get("review") for item in state["samples"].values())
    report = {"experiment_id": manifest["experiment_id"], "groups": groups, "total_token_median_reduction": reduction,
              "quality_pass": bool(quality) if quality_known else None, "completed": completed, "passed": bool(passed),
              "stopped": state.get("stopped", False), "stop_reason": state.get("stop_reason"),
              "expansion_status": expansion_reason(state), "samples": state["samples"],
              "limitations": "小样本自由轨迹对照；费用未知保持null；离线字节测试不是实际Token收益。"}
    write_json(root / "summary.json", report)
    return report


def selfcheck():
    import unittest
    suite = unittest.defaultTestLoader.discover(str(Path(__file__).parent), pattern="test_*.py")
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    return 0 if result.wasSuccessful() else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("selfcheck")
    init = sub.add_parser("init")
    for name in ["baseline-bin", "candidate-bin", "source-workspace", "output"]:
        init.add_argument("--" + name, required=True)
    for name in ["run-one", "review", "summarize"]:
        child = sub.add_parser(name)
        child.add_argument("--experiment", required=True)
        if name == "review":
            child.add_argument("--sample", required=True, choices=ORDER)
            child.add_argument("--scores", required=True, help="覆盖、引用、成立、版本条件、重要问题，逗号分隔五个0..2")
            child.add_argument("--major-errors", type=int, required=True)
            child.add_argument("--notes", default="")
    args = parser.parse_args(argv)
    try:
        if args.command == "selfcheck":
            return selfcheck()
        if args.command == "init":
            value = init_experiment(args.output, args.baseline_bin, args.candidate_bin, args.source_workspace)
        elif args.command == "run-one":
            run_one(args.experiment)
            return 0
        elif args.command == "review":
            value = record_review(args.experiment, args.sample, [int(value) for value in args.scores.split(",")], args.major_errors, args.notes)
        else:
            value = summarize(args.experiment)
        print(json.dumps(value, ensure_ascii=False, indent=2))
        return 0
    except (EvalError, OSError, ValueError, KeyError) as error:
        print("评测停止：" + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
