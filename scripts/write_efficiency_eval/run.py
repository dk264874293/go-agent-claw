#!/usr/bin/env python3
"""写入效率对照：只有 run-one 显式启动真实 CLI，其余命令离线执行。"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import statistics
import subprocess
import sys
import uuid

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
HELPERS = HERE / "helpers.py" if (HERE / "helpers.py").is_file() else REPO / "scripts/efficiency_eval/run.py"
_spec = importlib.util.spec_from_file_location("readonly_efficiency_helpers", HELPERS)
h = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(h)
EvalError = h.EvalError
for _name in ["read_json", "write_json", "digest", "now", "file_list", "fixture_digest", "json_lines", "coverage", "usage_metrics", "sanitize", "sanitize_value", "transport_digest"]:
    globals()[_name] = getattr(h, _name)
ORDER = ["baseline-ordinary-1", "candidate-ordinary-1", "candidate-ordinary-2", "baseline-ordinary-2", "baseline-report-1", "candidate-report-1"]
SETTINGS = dict(h.SETTINGS, read_page_bytes=8000)
OUTPUT = "优化汇总.md"
GOAL = ("完整阅读当前工作区的15份Markdown，在当前目录仅新建优化汇总.md，不修改原始资料。"
        "汇总重要问题、版本背景、适用条件、优化建议和待实测事项；核对V1/V2迁移、可选字段及默认值、现有测试命令。"
        "区分材料事实、分析推断、建议和待核实。重要结论须提供完整相对文件路径:起止行，不能省略文件名；跨文档判断同时引用两端。"
        "没有执行脚本时不能把静态风险写成已证实的运行失败。不要为了补行号全文重复读取。")
SCORES = ["coverage", "citation", "correctness", "conditions", "important_findings"]
EXIT = {"ANSWERED": 0, "VERIFIED": 0, "NEEDS_VERIFICATION": 2, "BLOCKED": 2, "BUDGET_EXHAUSTED": 3, "CANCELLED": 130, "FAILED": 1}
PROFILE_VERSION = 1
LIMITATIONS = "普通2对、严格报告1对分组；6fab仅症状参考；引用机械校验与语义评审分别报告。"
EFFICIENCY_GROUPS = ["ordinary"]


def strict_sample(sample_id):
    return sample_id == "candidate-report-1"


def candidate_arguments(manifest):
    return ["-read-page-bytes", "8000"]


def validate_candidate_spec(spec, manifest):
    if spec.get("read_context_version") != 1:
        raise EvalError("候选未实际启用通用读取记忆")


def validate_candidate_result(result, independent_coverage, manifest):
    pass


def receipt_chain_valid(effects, path, final_version):
    """确认创建后允许多次编辑；动作回执顺序必须形成完整版本链。"""
    cursor, seen, actions = "missing", False, set()
    for effect in effects:
        file = effect.get("file") or {}
        if file.get("path") != path:
            continue
        action = effect.get("action_id")
        after = file.get("after_version", "")
        if (not action or action in actions or effect.get("effect") != "CONFIRMED"
                or effect.get("status") not in (None, "", "SUCCEEDED")
                or file.get("before_version") != cursor or len(after) != 64
                or any(c not in "0123456789abcdef" for c in after)):
            return False
        actions.add(action)
        cursor, seen = after, True
    return seen and cursor == final_version


def harness_inputs():
    return {"run.py": Path(__file__), "checker.py": HERE / "checker.py", "helpers.py": HELPERS}


def build_goal(files):
    return GOAL


def init_experiment(output, baseline_bin, candidate_bin, source_workspace=None):
    root = Path(output).absolute()
    if root.exists() and any(root.iterdir()):
        raise EvalError("实验目录非空，禁止覆盖")
    source = Path(source_workspace or REPO / "eval/fixtures/write_efficiency_v1/workspace").resolve(strict=True)
    files = file_list(source, only_markdown=True)
    if len(files) != 15 or OUTPUT in {x["path"] for x in files}:
        raise EvalError("必须使用不含汇总产物的15份Markdown冻结夹具")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    manifest = {"schema_version": PROFILE_VERSION, "experiment_id": uuid.uuid4().hex, "created_at": now(), "goal": build_goal(files),
                "settings": SETTINGS, "order": ORDER, "max_samples": len(ORDER), "max_model_admissions": 60*len(ORDER),
                "files": files, "fixture_digest": fixture_digest(files), "binaries": {}, "harness": {},
                "environment": {"python": platform.python_version(), "platform": platform.platform(), "machine": platform.machine()},
                "environment_names": sorted(k for k in os.environ if k.upper().endswith(("API_KEY", "BASE_URL", "PROXY"))),
                "limitations": LIMITATIONS}
    for arm, path in [("baseline", baseline_bin), ("candidate", candidate_bin)]:
        target = root / "binaries" / arm; target.parent.mkdir(exist_ok=True, mode=0o700)
        shutil.copyfile(Path(path).resolve(strict=True), target); target.chmod(0o700)
        manifest["binaries"][arm] = {"path": "binaries/" + arm, "sha256": digest(target)}
    # 只归档经过枚举的Markdown，不能把源目录其他材料带进评测。
    for item in files:
        target = root / "fixture" / item["path"]; target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        shutil.copyfile(source / item["path"], target); target.chmod(0o600)
    for name, path in harness_inputs().items():
        target = root / "harness" / name; target.parent.mkdir(exist_ok=True, mode=0o700)
        shutil.copyfile(path, target); target.chmod(0o600)
        manifest["harness"][name] = digest(target)
    write_json(root / "manifest.json", manifest)
    write_json(root / "state.json", {"schema_version": 1, "samples": {}, "stopped": False})
    return manifest


def checked_manifest(experiment, *, require_current=True):
    root = Path(experiment).resolve(strict=True); m = read_json(root / "manifest.json")
    if m.get("schema_version") != PROFILE_VERSION or m.get("settings") != SETTINGS or m.get("order") != ORDER or m.get("goal") != build_goal(m.get("files", [])):
        raise EvalError("预算、题面或实验契约变化，拒绝继续")
    if m.get("max_samples") != len(ORDER) or m.get("max_model_admissions") != 60*len(ORDER) or set(m.get("binaries", {})) != {"baseline", "candidate"}:
        raise EvalError("批次数量或二进制组别契约变化")
    if file_list(root / "fixture") != m["files"] or fixture_digest(m["files"]) != m["fixture_digest"]:
        raise EvalError("冻结夹具摘要变化")
    for arm, item in m["binaries"].items():
        if item["path"] != "binaries/" + arm or digest(root / item["path"]) != item["sha256"]:
            raise EvalError("冻结二进制变化")
    for name, current in harness_inputs().items():
        if m["harness"].get(name) != digest(root / "harness" / name) or require_current and digest(current) != m["harness"].get(name):
            raise EvalError("checker或runner输入变化，拒绝续跑")
    return root, m


def account_home():
    """保护边界取OS账户记录，不能信任Agent运行时改写的HOME。"""
    import pwd
    try:
        path = Path(pwd.getpwuid(os.getuid()).pw_dir)
        if not path.is_absolute() or not path.is_dir():
            raise EvalError("OS账户HOME目录无效，拒绝放宽checker隔离")
        return path.resolve(strict=True)
    except (KeyError, OSError) as error:
        raise EvalError("无法确定OS账户HOME，拒绝放宽checker隔离") from error


def checker_context(root):
    root = Path(root).resolve(strict=True)
    scratch = root / "checker-runtime"
    for name in ["home", "tmp"]:
        (scratch / name).mkdir(parents=True, exist_ok=True, mode=0o700)
    env = {"PATH": os.defpath, "HOME": str(scratch / "home"), "TMPDIR": str(scratch / "tmp"), "PYTHONDONTWRITEBYTECODE": "1"}
    def sub(path):
        return "(subpath " + json.dumps(str(Path(path).resolve())) + ")"
    real_home = account_home()
    profile = "(version 1)(allow default)(deny network*)(deny file-write*)(deny file-read* " + sub(real_home) + ")"
    profile += "(allow file-read* " + sub(root) + " " + sub(Path(sys.base_prefix)) + ")(allow file-write* (literal \"/dev/null\"))"
    # pathlib.resolve需逐级读取祖先元数据；只放行这些字面目录的metadata，
    # 不能用subpath放开HOME、.claw或其他父目录的数据读取。
    # root在/tmp等HOME外部时，也只允许检查真实HOME目录本身的metadata。
    ancestors = " ".join("(literal " + json.dumps(str(path)) + ")" for path in sorted(set(root.parents) | {real_home}))
    profile += "(allow file-read-metadata " + ancestors + ")"
    return scratch, env, profile


def preflight(root):
    root = Path(root).resolve(strict=True)
    if sys.platform != "darwin" or not Path("/usr/bin/sandbox-exec").is_file():
        raise EvalError("付费运行要求macOS只读、禁网checker沙箱；预检失败不启动模型")
    scratch, env, profile = checker_context(root)
    # 在真实HOME祖先树验证metadata可用，目录数据、无关文件、写入和网络仍被禁止。
    real_home = account_home()
    protected_parent = root.parent if root.is_relative_to(real_home) else real_home
    sentinel = protected_parent / (".checker-denied-" + uuid.uuid4().hex)
    sentinel.write_text("仅用于验证沙箱拒绝读取的临时材料"); sentinel.chmod(0o600)
    try:
        code = ("import os,socket,sys;from pathlib import Path\n"
                "assert not any('KEY' in k or 'TOKEN' in k for k in os.environ)\n"
                "Path(sys.argv[1]).resolve(strict=True);os.stat(sys.argv[2])\n"
                "def denied(fn):\n"
                " try:fn()\n except PermissionError:return\n"
                " raise AssertionError('sandbox operation unexpectedly allowed')\n"
                "s=socket.socket();denied(lambda:s.connect(('127.0.0.1',9)))\n"
                "denied(lambda:open(sys.argv[3],'rb').read())\n"
                "denied(lambda:os.listdir(sys.argv[2]))\n"
                "denied(lambda:open(sys.argv[4],'w').write('blocked'))\n")
        command = ["/usr/bin/sandbox-exec", "-p", profile, sys.executable, "-c", code,
                   str(root), str(protected_parent), str(sentinel), str(scratch / "denied-write")]
        proc = subprocess.run(command, cwd=scratch, env=env, capture_output=True, timeout=10)
        write_json(scratch / "isolation-preflight.json", {"returncode":proc.returncode,"profile_sha256":hashlib.sha256(profile.encode()).hexdigest(),"network":"denied","ancestor_data":"denied","ancestor_metadata":"literal-only","credentials":"absent"})
        if proc.returncode:
            raise EvalError("checker网络、凭据、祖先数据或写入隔离预检失败：" + proc.stderr.decode(errors="replace")[-1200:])
    finally:
        sentinel.unlink(missing_ok=True)
    # 必须走真实冻结checker、夹具复制和Path.resolve路径，不以socket探测替代。
    manifest = read_json(Path(root) / "manifest.json")
    probe = scratch / ("preflight-" + uuid.uuid4().hex)
    shutil.copytree(Path(root) / "fixture", probe / "workspace")
    text = "# 沙箱预检文档\n版本、默认score及JSON契约优化建议均待实测。\n" * 20
    for item in manifest["files"]: text += "引用：" + item["path"] + ":1\n"
    (probe / "workspace" / OUTPUT).write_text(text)
    checked = run_checker(Path(root), probe, manifest)
    if checked.get("artifact_result") != "pass" or checked.get("scope_result") != "pass":
        raise EvalError("冻结checker实际夹具预检未通过")


def run_checker(root, sample_root, manifest, *, prefix="checker"):
    scratch, env, profile = checker_context(root)
    stage = scratch / ("check-" + uuid.uuid4().hex)
    shutil.copytree(sample_root / "workspace", stage, ignore=shutil.ignore_patterns(".claw"), symlinks=True)
    command = ["/usr/bin/sandbox-exec", "-p", profile, sys.executable, str(root / "harness/checker.py"), "--workspace", str(stage), "--manifest", str(root / "manifest.json")]
    proc = subprocess.run(command, cwd=stage, env=env, capture_output=True, timeout=30)
    command_name = "checker-command.json" if prefix == "checker" else prefix + ".command.json"
    write_json(sample_root / command_name, {"argv": command, "environment_names": sorted(env), "network": "denied", "writes": "denied", "profile":profile})
    for kind, data in [("stdout",proc.stdout),("stderr",proc.stderr)]:
        path = sample_root / (prefix + "." + kind + ".log"); path.write_bytes(data); path.chmod(0o600)
    if proc.returncode:
        raise EvalError("独立checker环境失败，非产物失败")
    result = json.loads(proc.stdout)
    write_json(sample_root / (prefix + ".json"), result)
    return result


def recheck_original(experiment, sample_id, label="checker-repaired"):
    """只重检保存样本，保留原错误和stopped状态，绝不启动Agent或续跑批次。"""
    import re
    if sample_id not in ORDER or not re.fullmatch(r"checker-repaired(?:-[A-Za-z0-9_-]+)?",label):
        raise EvalError("样本或离线重检标签无效")
    root, manifest = checked_manifest(experiment, require_current=False)
    if (root / "run.lock").exists():
        raise EvalError("实验运行中，不能执行离线重检")
    sample = root / "samples" / sample_id
    if not (sample / "result.json").is_file() or not (sample / "workspace").is_dir():
        raise EvalError("没有已保存的原样本")
    if any(sample.glob(label + ".*")):
        raise EvalError("离线重检证据已存在，请使用不同--label保留每次记录")
    protected = [root / "manifest.json",root / "state.json",sample / "result.json"]
    protected += [p for p in sample.glob("checker*") if p.is_file() and not p.name.startswith("checker-repaired")]
    original = {str(p.relative_to(root)):digest(p) for p in protected if p.is_file()}
    before_workspace = file_list(sample / "workspace")
    result, failure = None, None
    try:
        result = run_checker(root,sample,manifest,prefix=label)
        return result
    except (EvalError,OSError,ValueError,subprocess.SubprocessError) as error:
        failure = str(error)
        raise
    finally:
        after = {name:digest(root/name) for name in original}
        unchanged = after == original and file_list(sample/"workspace") == before_workspace
        evidence = {"schema_version":1,"kind":"offline_checker_repair","recorded_at":now(),"sample_id":sample_id,
                    "original_records":original,"original_records_unchanged":unchanged,
                    "checker_sha256":manifest["harness"]["checker.py"],"repair_runner_sha256":digest(__file__),
                    "uses_original_frozen_checker":True,"agent_started":False,"paid_calls":0,
                    "network":"denied","writes":"denied","result":result,"error":failure,
                    "limitations":"本记录只修复checker环境验证，不清除原基础设施失败、不恢复批次或补造语义评分。"}
        command_path = sample / (label + ".command.json")
        if command_path.exists(): evidence["profile_sha256"] = hashlib.sha256(read_json(command_path)["profile"].encode()).hexdigest()
        write_json(sample/(label+".evidence.json"),evidence)
        if not unchanged: raise EvalError("离线重检意外改变原记录或工作区")


def recover_metrics(sample_root):
    """协议失败仍保留可核对的现场用量；无法确认的量保持未知。"""
    usage = {}
    task_paths = list((sample_root / "workspace/.claw/tasks").glob("*/events.jsonl"))
    for path in task_paths:
        for line in path.read_bytes().splitlines():
            try:
                event = json.loads(line)
                found = event.get("payload", {}).get("usage", {})
                if found and event.get("version") == 1:
                    usage = found
            except (ValueError, TypeError, AttributeError):
                continue
    result = {k:usage.get(k) for k in ["model_calls", "tool_calls", "turns"]}
    result.update(input_tokens=None, output_tokens=None, total_tokens=None, cost=None, usage_recovered=True)
    result["elapsed_seconds"] = usage.get("elapsed", 0) / 1e9 if usage else None
    # 评分或协议检查失败不代表已记录Token未知。仅在唯一任务和完整观测可核对时恢复。
    flows = list((sample_root / "observations").glob("*/events.jsonl"))
    if len(task_paths) == 1 and len(flows) == 1 and type(usage.get("model_calls")) is int:
        try:
            observations = json_lines(flows[0])
            result.update(usage_metrics(observations, task_paths[0].parent.name, usage["model_calls"]))
        except (EvalError, OSError, ValueError, KeyError, TypeError, AttributeError):
            pass
    return result


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    tasks = list((sample_root / "workspace/.claw/tasks").glob("*/events.jsonl"))
    if len(tasks) != 1:
        raise EvalError("无法确定唯一任务记录")
    events = json_lines(tasks[0]); finals = [x["payload"] for x in events if x["kind"] == "finished"]
    if any(e.get("version") != 1 for e in events) or len(finals) != 1:
        raise EvalError("任务日志版本或终态无效")
    s = finals[0]; result = s.get("last_result", {}); spec = s.get("spec", {}); task_id = tasks[0].parent.name
    if s.get("session_id") != session or spec.get("id") != task_id or result.get("task_id") != task_id or not result.get("run_id"):
        raise EvalError("任务/会话/运行身份不匹配")
    if spec.get("goal") != manifest["goal"] or spec.get("mode") != "workspace" or spec.get("kind") != "change" or spec.get("analysis", False):
        raise EvalError("题面或执行模式与冻结配置不符")
    if spec.get("read_page_bytes", 8000) != 8000:
        raise EvalError("主实验读取档位不为8000")
    strict = strict_sample(sample_id)
    if bool(spec.get("report")) != strict:
        raise EvalError("实际报告开关与样本分组不符")
    if sample_id.startswith("candidate"):
        validate_candidate_spec(spec, manifest)
    if s.get("limits") != {"MaxModelCalls":60,"MaxToolCalls":120,"MaxTurns":30,"Timeout":600000000000}:
        raise EvalError("实际预算与冻结配置不符")
    for k in ["model_calls", "tool_calls", "turns"]:
        if s.get("usage", {}).get(k) != result.get("usage", {}).get(k):
            raise EvalError("状态与结果用量不一致")
    if EXIT.get(result.get("status")) != returncode:
        raise EvalError("持久终态与进程退出码不符")
    flows = list((sample_root / "observations").glob("*/events.jsonl"))
    if len(flows) != 1:
        raise EvalError("缺少唯一观测流")
    observed = json_lines(flows[0])
    if any(x.get("schema_version") != 1 or x["event_type"] == "observation_gap" or x.get("payload", {}).get("incomplete") for x in observed):
        raise EvalError("观测记录损坏或有缺口")
    returned = [x["payload"] for x in observed if x["event_type"] == "run_returned" and x.get("task_id") == task_id]
    if len(returned) != 1 or returned[0].get("result_run_id") != result["run_id"] or returned[0].get("status") != result["status"]:
        raise EvalError("入口返回与持久结果不一致")
    finalized = [x for x in observed if x["event_type"] == "run_finalized" and x.get("task_id") == task_id]
    return_events = [x for x in observed if x["event_type"] == "run_returned" and x.get("task_id") == task_id]
    if len(finalized) != 1 or finalized[0]["payload"].get("result_run_id") != result["run_id"] or finalized[0]["payload"].get("status") != result["status"] or finalized[0]["sequence"] >= return_events[0]["sequence"]:
        raise EvalError("缺少Run完整收尾后返回的生命周期证据")
    if any(x["payload"].get("model") != SETTINGS["model"] for x in observed if x["event_type"] == "model_started" and x.get("task_id") == task_id):
        raise EvalError("实际模型与冻结模型不一致")
    metrics = usage_metrics(observed, task_id, result["usage"]["model_calls"])
    metrics.update({k:result["usage"][k] for k in ["model_calls","tool_calls","turns"]})
    metrics["elapsed_seconds"] = result["usage"]["elapsed"] / 1e9
    if not 0 <= metrics["model_calls"] <= 60 or not 0 <= metrics["tool_calls"] <= 120 or not 0 <= metrics["turns"] <= 30:
        raise EvalError("实际调用突破冻结预算")
    wire = [x["payload"].get("request_encoded_bytes") for x in observed if x["event_type"] == "model_request_encoded" and x.get("task_id") == task_id]
    metrics["http_attempts"] = len(wire); metrics["encoded_request_bytes"] = sum(wire) if wire and all(type(n) is int and n >= 0 for n in wire) else None
    after = file_list(sample_root / "workspace"); write_json(sample_root / "files-after.json", after)
    sources = {x["path"]:x for x in after if x["path"] != OUTPUT}
    target = next((x for x in after if x["path"] == OUTPUT), None)
    scope = sorted(sources.values(),key=lambda x:x["path"]) == manifest["files"] and target is not None
    effects = result.get("delivery", {}).get("effects", [])
    receipt_ok = bool(target and result.get("delivery", {}).get("history_complete") is True and receipt_chain_valid(effects, OUTPUT, target["sha256"]))
    report_check = "not_run"
    if strict:
        rep, delivered = s.get("report", {}), result.get("report", {})
        expected = {"report_id":rep.get("report_id"),"output_version":target["sha256"] if target else None,"reference_result":rep.get("reference_result"),"semantic_result":"not_run"}
        report_check = "pass" if rep.get("status") == "published" and rep.get("reference_result") in {"pass","partial"} and rep.get("semantic_result") == "not_run" and all(delivered.get(k)==v for k,v in expected.items()) and delivered.get("target_path") == OUTPUT and delivered.get("origin_run_id") == result["run_id"] else "fail"
    independent_coverage = coverage(events, manifest["files"])
    if sample_id.startswith("candidate"):
        validate_candidate_result(result, independent_coverage, manifest)
    return {"task_id":task_id,"run_id":result["run_id"],"status":result["status"],"stop_code":result.get("stop_code"),"reason":result.get("reason"),"exit_code":returncode,"final_text":result.get("final_text", ""),
            "protocol_valid":True,"scope_pass":scope,"receipt_pass":receipt_ok,"coverage":independent_coverage,"pending_actions":len(s.get("pending",{})),"metrics":metrics,"report_check":report_check,
            "evidence":{"task_events":str(tasks[0].relative_to(sample_root)),"observations":str(flows[0].relative_to(sample_root)),"artifact":"workspace/"+OUTPUT}}


def mechanical_ok(item):
    return item.get("protocol_valid") and item.get("scope_pass") and item.get("receipt_pass") and item.get("coverage",{}).get("complete") and item.get("status")=="NEEDS_VERIFICATION" and item.get("exit_code")==2 and item.get("pending_actions")==0 and item.get("checker",{}).get("artifact_result")=="pass" and item.get("report_check") in {"pass","not_run"}


def review_ok(review):
    return bool(review and sum(review["scores"].values())>=8 and review["major_errors"]==0)


def candidate_gate(item):
    return mechanical_ok(item) and item.get("metrics",{}).get("model_calls",61)<=12 and item.get("metrics",{}).get("tool_calls",121)<=40


def expansion_reason(state):
    items = state["samples"]
    ca, ba = items.get("candidate-ordinary-1"), items.get("baseline-ordinary-1")
    if not ca: return None
    if not candidate_gate(ca): return "候选首对未满足机械验收及12模型/40工具门槛，停止扩样"
    if not ca.get("review") or not ba.get("review"): return "两组首对语义评审未完成，禁止扩样"
    if not review_ok(ca["review"]) or sum(ca["review"]["scores"].values()) < sum(ba["review"]["scores"].values()): return "候选首对语义质量不合格或低于基线，停止扩样"
    for name,item in items.items():
        if name.startswith("candidate") and name!="candidate-ordinary-1" and item.get("review") and not review_ok(item["review"]): return "后续候选语义质量失败，停止扩样"
    return None


def run_one(experiment):
    root,m = checked_manifest(experiment); state=read_json(root/"state.json")
    if state.get("stopped"): raise EvalError("实验已停止："+state.get("stop_reason","未知"))
    if any(x.get("state")!="completed" for x in state["samples"].values()): raise EvalError("存在未确认样本，禁止重跑")
    name=next((x for x in ORDER if x not in state["samples"]),None)
    if not name: raise EvalError("全部样本已完成")
    if name not in ORDER[:2] and expansion_reason(state): raise EvalError(expansion_reason(state))
    lock=root/"run.lock"
    try: fd=os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd)
    except FileExistsError: raise EvalError("存在运行锁，禁止并发或重跑")
    try:
        if read_json(root/"state.json")!=state: raise EvalError("准入前状态已变化")
        try:
            preflight(root)
            if not os.environ.get("DEEPSEEK_API_KEY", "").strip(): raise EvalError("凭据预检失败，模型未启动")
            td=transport_digest(os.environ)
            if state.get("transport_env_digest") and td!=state["transport_env_digest"]: raise EvalError("网关或代理配置变化")
            state["transport_env_digest"]=td
        except (EvalError,OSError,subprocess.SubprocessError) as e:
            state.update(stopped=True,stop_reason=str(e));write_json(root/"state.json",state);raise
        arm,group,_=name.split("-"); sample=root/"samples"/name;work=sample/"workspace";shutil.copytree(root/"fixture",work)
        session="write-eval-"+uuid.uuid4().hex
        cmd=[str(root/m["binaries"][arm]["path"]),"-dir",str(work),"-session",session,"-mode","workspace","-task-kind","change","-model",SETTINGS["model"],"-provider","deepseek","-max-model-calls","60","-max-tool-calls","120","-max-turns","30","-timeout","600s","-context-bytes","300000","-prompt",m["goal"]]
        if arm=="candidate": cmd += candidate_arguments(m)
        if strict_sample(name): cmd += ["-report",OUTPUT]
        env=os.environ.copy();env.update(CLAW_OBSERVE="on",CLAW_OBSERVE_DIR=str(sample/"observations"))
        item={"state":"running","sample_id":name,"arm":arm,"group":group,"session_id":session,"started_at":now(),"binary_sha256":m["binaries"][arm]["sha256"],"fixture_digest":m["fixture_digest"],"command":cmd}
        state["samples"][name]=item;write_json(root/"state.json",state)
        write_json(sample/"command.json",{"argv":cmd,"cwd":str(work),"environment_names":sorted(k for k in env if any(x in k.upper() for x in ["KEY","TOKEN","PROXY","BASE_URL"]))})
        write_json(sample/"files-before.json",m["files"])
        print("启动 "+name+"；不自动重试",flush=True)
        try:
            proc=subprocess.Popen(cmd,cwd=work,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
            item["process_id"]=proc.pid;write_json(root/"state.json",state)
            try: out,err=proc.communicate(timeout=660)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid,signal.SIGINT)
                try: out,err=proc.communicate(timeout=10)
                except subprocess.TimeoutExpired: os.killpg(proc.pid,signal.SIGKILL);out,err=proc.communicate()
                item["infrastructure_error"]="运行超时"
            (sample/"stdout.log").write_text(sanitize(out.decode("utf-8",errors="replace"),env));(sample/"stderr.log").write_text(sanitize(err.decode("utf-8",errors="replace"),env))
            item["process_exit_code"]=proc.returncode
            try: item.update(inspect_sample(sample,m,session,proc.returncode,name))
            except (EvalError,OSError,ValueError,KeyError,TypeError) as e: item.update(protocol_valid=False,infrastructure_error=str(e),metrics=recover_metrics(sample))
            try: item["checker"]=run_checker(root,sample,m)
            except (EvalError,OSError,ValueError,subprocess.SubprocessError) as e: item.update(checker={"artifact_result":"error","scope_result":"error","semantic_result":"not_run"},infrastructure_error=str(e))
            if item.get("metrics",{}).get("provider_errors") or item.get("status")=="FAILED": item["infrastructure_error"]="模型请求或关键运行环境失败，不重试"
            item.update(state="completed",finished_at=now()); item=sanitize_value(item,env);state["samples"][name]=item
            if item.get("infrastructure_error") or not item.get("scope_pass") or item.get("status")=="CANCELLED": state.update(stopped=True,stop_reason=item.get("infrastructure_error") or "范围违规或取消")
            elif arm == "candidate" and not candidate_gate(item): state.update(stopped=True,stop_reason="候选未满足完整交付及12模型/40工具门槛，停止批次")
            for path in [sample/"stdout.log",sample/"stderr.log",sample/"checker.stdout.log",sample/"checker.stderr.log"]:
                if path.exists(): path.chmod(0o600)
            write_json(sample/"result.json",item);write_json(root/"state.json",state);return item
        except BaseException as e:
            item.update(state="unconfirmed",infrastructure_error=sanitize(str(e),env),metrics=recover_metrics(sample));state.update(stopped=True,stop_reason="运行中断或未确认，不自动重试")
            write_json(sample/"result.json",item);write_json(root/"state.json",state);raise
    finally: lock.unlink(missing_ok=True)


def record_review(experiment,sample_id,review_path):
    root,m=checked_manifest(experiment); lock=root/"run.lock"
    try: fd=os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd)
    except FileExistsError: raise EvalError("运行中不可修改评审")
    try:
        state=read_json(root/"state.json");item=state["samples"].get(sample_id)
        if not item or item.get("state")!="completed": raise EvalError("只能评审已完成样本")
        review=read_json(review_path)
        if not str(review.get("reviewer","")).strip() or review.get("method") not in {"human","source_audit_agent"}: raise EvalError("必须提供真实评审身份和方式，不得将Agent评审冒充独立人工")
        if set(review.get("scores",{}))!=set(SCORES) or any(type(x)is not int or x not in {0,1,2} for x in review["scores"].values()) or type(review.get("major_errors"))is not int or review["major_errors"]<0: raise EvalError("评分或重大错误数无效")
        if set(review.get("score_basis", {})) != set(SCORES) or any(not isinstance(v, str) or not v.strip() for v in review["score_basis"].values()): raise EvalError("五项分数均须提供具体评分依据")
        if not isinstance(review.get("findings"),list) or not review["findings"]: raise EvalError("须逐条记录结论、判断和原文依据")
        for f in review["findings"]:
            if not str(f.get("claim","")).strip() or f.get("verdict") not in {"supported","incorrect","unverified"} or not f.get("evidence"): raise EvalError("结论评审缺少判断或原文依据")
            for ref in f["evidence"]:
                source=root/"fixture"/ref.get("path","")
                if ref.get("path") not in {x["path"] for x in m["files"]} or type(ref.get("start_line"))is not int or type(ref.get("end_line"))is not int: raise EvalError("评审引用路径或行号无效")
                lines=source.read_text().splitlines();a,b=ref["start_line"],ref["end_line"]
                if not 1<=a<=b<=len(lines) or not ref.get("quote") or ref["quote"] not in "\n".join(lines[a-1:b]): raise EvalError("评审摘录不在声明的源文件行范围")
        review=dict(review,reviewed_at=now(),semantic_result="pass" if review_ok(review) else "fail")
        item["review"]=sanitize_value(review,os.environ)
        for name, candidate in state["samples"].items():
            if not name.startswith("candidate") or not candidate.get("review"): continue
            baseline = state["samples"].get(name.replace("candidate-", "baseline-", 1), {})
            bad = not review_ok(candidate["review"])
            if baseline.get("review"): bad = bad or sum(candidate["review"]["scores"].values()) < sum(baseline["review"]["scores"].values())
            if bad: state.update(stopped=True,stop_reason="候选语义质量不合格或低于同对基线，停止批次")
        write_json(root/"samples"/sample_id/"review.json",item["review"]);write_json(root/"samples"/sample_id/"result.json",item);write_json(root/"state.json",state);return item["review"]
    finally: lock.unlink(missing_ok=True)


def summarize(experiment):
    # 只读重建使用冻结协议及逐例记录；当前工作源码更新不应阻止历史汇总。
    # 续跑仍由默认require_current=True检查，冻结harness本身始终核对摘要。
    root,m=checked_manifest(experiment,require_current=False);samples={}
    # 从独立逐例归档重建，不信赖可重建索引state.json里的评分副本。
    for name in ORDER:
        path=root/"samples"/name/"result.json"
        if path.exists(): samples[name]=read_json(path)
    groups={}
    for group in ["ordinary","report"]:
        groups[group]={}
        for arm in ["baseline","candidate"]:
            items=[x for name,x in samples.items() if name.startswith(arm+"-"+group+"-")]
            metrics={}
            for k in ["model_calls","tool_calls","input_tokens","output_tokens","total_tokens","cost"]:
                vals=[x.get("metrics",{}).get(k) for x in items];metrics[k+"_median"]=statistics.median(vals) if vals and all(v is not None for v in vals) else None
            groups[group][arm]={"samples":len(items),"metrics":metrics}
        a=groups[group]["baseline"]["metrics"]["total_tokens_median"];b=groups[group]["candidate"]["metrics"]["total_tokens_median"]
        groups[group]["token_median_reduction"]=1-b/a if a and b is not None else None
    completed=len(samples)==len(ORDER) and all(x.get("state")=="completed" for x in samples.values())
    quality=True;quality_known=completed
    for name,item in samples.items():
        if not item.get("review"): quality_known=False
        if name.startswith("candidate"):
            baseline=samples.get(name.replace("candidate-","baseline-",1),{})
            cr,br=item.get("review"),baseline.get("review")
            quality=quality and review_ok(cr) and bool(br) and sum(cr["scores"].values())>=sum(br["scores"].values())
    reduction=groups["ordinary"]["token_median_reduction"]
    valid = all(x.get("protocol_valid") and not x.get("infrastructure_error") for x in samples.values())
    passed=completed and valid and quality and all(candidate_gate(x) for n,x in samples.items() if n.startswith("candidate")) and all(groups[g]["token_median_reduction"] is not None and groups[g]["token_median_reduction"]>=.30 for g in EFFICIENCY_GROUPS)
    index = read_json(root/"state.json") if (root/"state.json").exists() else {}
    report={"experiment_id":m["experiment_id"],"completed":completed,"passed":bool(passed),"quality_result":("pass" if quality else "fail") if quality_known else "not_run","groups":groups,"samples":samples,
            "total_model_admissions":sum(x.get("metrics",{}).get("model_calls") or 0 for x in samples.values()) if all(x.get("metrics",{}).get("model_calls") is not None for x in samples.values()) else None,
            "known_model_admissions":sum(x.get("metrics",{}).get("model_calls") or 0 for x in samples.values()),
            "unknown_model_admission_samples":sum(x.get("metrics",{}).get("model_calls") is None for x in samples.values()),
            "stopped":index.get("stopped",False),"stop_reason":index.get("stop_reason"),"expansion_status":expansion_reason({"samples":samples}),"limitations":m["limitations"]}
    write_json(root/"summary.json",report);return report


def selfcheck():
    import unittest
    suite=unittest.defaultTestLoader.discover(str(HERE),pattern="test_*.py")
    return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1


def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest="command",required=True);sub.add_parser("selfcheck")
    q=sub.add_parser("init")
    for name in ["output","baseline-bin","candidate-bin"]:q.add_argument("--"+name,required=True)
    q.add_argument("--source-workspace")
    for command in ["run-one","review","summarize","recheck-original"]:
        q=sub.add_parser(command);q.add_argument("--experiment",required=True)
        if command=="review":q.add_argument("--sample",required=True,choices=ORDER);q.add_argument("--review-file",required=True)
        if command=="recheck-original":q.add_argument("--sample",required=True,choices=ORDER);q.add_argument("--label",default="checker-repaired")
    args=p.parse_args(argv)
    try:
        if args.command=="selfcheck":return selfcheck()
        if args.command=="init":v=init_experiment(args.output,args.baseline_bin,args.candidate_bin,args.source_workspace)
        elif args.command=="run-one":v=run_one(args.experiment)
        elif args.command=="review":v=record_review(args.experiment,args.sample,args.review_file)
        elif args.command=="recheck-original":v=recheck_original(args.experiment,args.sample,args.label)
        else:v=summarize(args.experiment)
        print(json.dumps(v,ensure_ascii=False,indent=2));return 0
    except (EvalError,OSError,ValueError,KeyError,subprocess.SubprocessError) as e:print("评测停止："+str(e),file=sys.stderr);return 2

if __name__=="__main__":sys.exit(main())
