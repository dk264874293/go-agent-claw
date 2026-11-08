#!/usr/bin/env python3
"""工作笔记v3普通写入对照；只有run-one启动模型，其余命令完全离线。"""
import argparse
from contextlib import contextmanager
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import random
import re
import shutil
import statistics
import sys
import uuid

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
COMMON = HERE / "common.py" if (HERE / "common.py").is_file() else REPO / "scripts/write_efficiency_eval/run.py"
_spec = importlib.util.spec_from_file_location("work_notes_common", COMMON)
r = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(r)
EvalError = r.EvalError
ORACLE = HERE / "oracle.json" if (HERE / "oracle.json").is_file() else REPO / "eval/fixtures/work_notes_v3/semantic_oracle.json"
RUBRIC = HERE / "rubric.json" if (HERE / "rubric.json").is_file() else REPO / "eval/fixtures/work_notes_v3/rubric.json"
DEFAULT_ORDER = ["baseline-ordinary-1", "candidate-ordinary-1", "candidate-ordinary-2", "baseline-ordinary-2", "baseline-ordinary-3", "candidate-ordinary-3"]
PROFILE = "work_notes_v3"
PROFILES = {"work_notes_v3": DEFAULT_ORDER, "notes_delivery_v1": DEFAULT_ORDER[:2]}
QUALITY_CHECKS = ("classification_exceptions", "formula_endpoints", "local_delivery_links")
r.PROFILE_VERSION = 3
r.ORDER = list(DEFAULT_ORDER)
r.GOAL = ("递归阅读当前工作区的全部Markdown资料，仅新建优化汇总.md，不修改原始资料。"
          "生成精简汇总，保留重要问题、版本背景、适用条件、优化建议和待实测事项；"
          "核对数值、默认值及版本的明显矛盾，区分原文转述、静态推导、建议与已实测事实。"
          "重要结论给出完整相对路径:起止行，跨文档判断同时引用两端。每项问题只展开一次，避免长篇代码和重复摘录。"
          "未执行脚本时，不把静态风险或推导写成已证实的运行结果；不要为了补行号全文重复读取。")
r.LIMITATIONS = "普通workspace/change三对；未知费用null；有限oracle不替代匿名语义评审；未执行课程代码，未评not_run。"
r.EFFICIENCY_GROUPS = ["ordinary"]
r.strict_sample = lambda name: False
r.build_goal = lambda files: r.GOAL
r.candidate_arguments = lambda manifest: ["-read-page-bytes", "8000"]


def configure_profile(profile):
    global PROFILE
    if profile not in PROFILES: raise EvalError("未知工作笔记评测profile")
    PROFILE, r.ORDER = profile, list(PROFILES[profile])
    r.LIMITATIONS = ("普通workspace/change独立双样本；最多120模型准入，不扩样或重试；未知费用null；有限反例不替代匿名语义评审。"
                     if profile == "notes_delivery_v1" else "普通workspace/change三对；未知费用null；有限oracle不替代匿名语义评审；未执行课程代码，未评not_run。")


def harness_inputs():
    inputs = {"run.py": Path(__file__), "common.py": COMMON, "checker.py": r.HERE / "checker.py", "helpers.py": r.HELPERS,
            "quality_oracle.py": HERE / "quality_oracle.py" if (HERE / "quality_oracle.py").exists() else REPO / "scripts/coverage_efficiency_eval/quality_oracle.py",
            "semantic_oracle.py": HERE / "semantic_oracle.py" if (HERE / "semantic_oracle.py").exists() else REPO / "scripts/write_efficiency_eval/semantic_oracle.py",
            "oracle.json": ORACLE, "rubric.json": RUBRIC}
    if PROFILE == "notes_delivery_v1":
        inputs.update({"delivery_cases.py": HERE / "delivery_cases.py",
                       "delivery_cases.json": HERE / "delivery_cases.json" if (HERE / "delivery_cases.json").exists() else REPO / "eval/fixtures/notes_delivery_v1/cases.json"})
    return inputs


r.harness_inputs = harness_inputs
_base_init, _base_checked, _base_inspect = r.init_experiment, r.checked_manifest, r.inspect_sample
_base_run = r.run_one


def init_experiment(output, baseline_bin, candidate_bin, source_workspace=None, profile="work_notes_v3"):
    configure_profile(profile)
    result = _base_init(output, baseline_bin, candidate_bin, source_workspace)
    # manifest在工作区外，只用于独立评分，绝不出现在题面或工具参数。
    result["profile"] = profile
    result["credentials"] = {"source": "project_dotenv_runtime_only", "snapshot": "bind_first_run", "persist_values": False}
    result["project_dotenv"] = str(REPO / ".env")
    r.write_json(Path(output) / "manifest.json", result)
    r.write_json(Path(output) / "manifest-seal.json", {"sha256": r.digest(Path(output) / "manifest.json")})
    return result


def checked_manifest(experiment, *, require_current=True):
    configure_profile(r.read_json(Path(experiment) / "manifest.json").get("profile"))
    root, manifest = _base_checked(experiment, require_current=require_current)
    if r.read_json(root / "manifest-seal.json") != {"sha256": r.digest(root / "manifest.json")}:
        raise EvalError("冻结manifest摘要变化")
    if manifest.get("profile") != PROFILE or manifest.get("credentials") != {"source": "project_dotenv_runtime_only", "snapshot": "bind_first_run", "persist_values": False}:
        raise EvalError("工作笔记评测契约变化")
    if not isinstance(manifest.get("project_dotenv"), str) or not Path(manifest["project_dotenv"]).is_absolute() or Path(manifest["project_dotenv"]).name != ".env":
        raise EvalError("项目.env运行时来源不符")
    return root, manifest


def validate_candidate_spec(spec, manifest):
    if spec.get("read_context_version") != 2:
        raise EvalError("候选未实际启用读取记忆v2")


r.validate_candidate_spec = validate_candidate_spec
r.init_experiment, r.checked_manifest = init_experiment, checked_manifest


def derive_metrics(observations, task_events, task_id, candidate, file_count):
    events = [e for e in observations if e.get("task_id") == task_id]
    calls, latest, publications = {}, None, set()
    tools, calls_before, found_read = [], 0, False
    for event in events:
        kind, payload, identity = event["event_type"], event.get("payload", {}), event.get("model_call_id")
        if kind == "model_started":
            calls.setdefault(identity, {}).update(payload)
            if not found_read: calls_before += 1
        elif kind == "model_finished":
            calls.setdefault(identity, {}).update(payload); latest = identity
        elif kind == "tool_attempted":
            tool = payload.get("tool", ""); tools.append(tool)
            if tool in {"read_file", "read_lines"}: found_read = True
            if tool in {"write_file", "edit_file", "write_report"} and latest: publications.add(latest)
    first_find = tools.index("find_files") if "find_files" in tools else None
    finished = [e["payload"] for e in task_events if e["kind"] == "finished"]
    memory = finished[-1].get("read_memory", {}) if finished else {}
    work_events = [e.get("payload", {}) for e in events if e["event_type"] == "work_notes_processed"]
    sums = lambda field: sum(p.get(field, 0) for p in work_events) if candidate else None
    accepted = sum(p.get("notes_accepted", 0) for p in work_events)
    duplicate_accepted = sum(p.get("notes_accepted", 0) for p in work_events if p.get("error_class") == "duplicate")
    duplicates = sum(p.get("error_class") == "duplicate" for p in work_events)
    rejected = sum(p.get("notes_rejected", 0) for p in work_events)
    carrier_denominator = accepted + rejected
    responses = {}
    for event in events:
        if event["event_type"] != "work_notes_response": continue
        identity, payload = event.get("model_call_id"), event.get("payload", {})
        if not identity: raise EvalError("工作笔记响应指标缺少model_call_id")
        if identity in responses and responses[identity] != payload: raise EvalError("同一模型响应的工作笔记指标不一致")
        responses[identity] = payload
    opportunities = sum(p.get("notes_opportunity") is True for p in responses.values())
    response_accepted = sum(p.get("notes_opportunity") is True and p.get("notes_response_accepted") is True
                            and p.get("error_class") == "accepted" for p in responses.values())
    processed_summaries = set()
    for agent, ids in memory.get("processed_reads", {}).items():
        for identity in ids:
            evidence = memory.get("evidence", {}).get(identity, {})
            if (memory.get("summaries", {}).get(agent, {}).get(identity, "").strip()
                    and evidence.get("receipt", {}).get("path") not in (None, r.OUTPUT)):
                processed_summaries.add(identity)
    metric = {"source_file_count": file_count, "model_calls_before_first_read": calls_before if found_read else None,
              "list_dir_before_find": tools[:first_find].count("list_dir") if first_find is not None else None,
              "find_calls": tools.count("find_files"), "list_dir_calls": tools.count("list_dir"),
              "publish_generation_seconds": sum(calls[c].get("duration_ms", 0) for c in publications) / 1000 if publications else None,
              "longest_model_seconds": max((p.get("duration_ms", 0) for p in calls.values()), default=0) / 1000,
              "accepted_notes": accepted - duplicate_accepted if candidate else None, "accepted_carriers": accepted if candidate else None,
              "duplicate_carriers": duplicates if candidate else None, "rejected_carriers": rejected if candidate else None,
              "rejected_notes": sums("notes_rejected"), "missing_notes": sums("notes_missing"),
              "carrier_acceptance_denominator": carrier_denominator if candidate else None,
              "carrier_acceptance_rate": (accepted - duplicate_accepted) / carrier_denominator if candidate and carrier_denominator else None,
              "notes_response_count": len(responses) if responses else None,
              "notes_accepted_responses": response_accepted if responses else None,
              "notes_acceptance_denominator": opportunities if responses else None,
              "notes_acceptance_rate": response_accepted / opportunities if opportunities else None,
              "notes_response_processed_sources": sum(p.get("notes_processed_sources", 0) for p in responses.values()) if responses else None,
              "notes_response_deferred_sources": sum(p.get("notes_deferred_sources", 0) for p in responses.values()) if responses else None,
              "notes_response_candidates": sum(p.get("notes_candidates", 0) for p in responses.values()) if responses else None,
              "notes_response_submitted": sum(p.get("notes_submitted", 0) for p in responses.values()) if responses else None,
              "notes_response_rejected": sum(p.get("notes_response_rejected", 0) for p in responses.values()) if responses else None,
              "notes_response_duplicates": sum(p.get("notes_duplicates", 0) for p in responses.values()) if responses else None,
              "notes_carrier_bytes": sums("notes_carrier_bytes"),
              "notes_metadata_removed_bytes": sum(p.get("notes_metadata_removed_bytes", 0) for p in calls.values()) if candidate else None,
              "notes_metadata_retained_bytes": sum(p.get("notes_metadata_retained_bytes", 0) for p in calls.values()) if candidate else None,
              "folded_source_bytes": sum(p.get("folded_source_bytes", 0) for p in calls.values()) if candidate else None,
              "folded_reads": sum(p.get("folded_reads", 0) for p in calls.values()) if candidate else None,
              "processed_reads": sum(len(ids) for ids in memory.get("processed_reads", {}).values()),
              "summary_count": sum(len(items) for items in memory.get("summaries", {}).values()),
              "processed_summary_sources": len(processed_summaries)}
    return metric


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    item = _base_inspect(sample_root, manifest, session, returncode, sample_id)
    task_events = r.json_lines(Path(sample_root) / item["evidence"]["task_events"])
    state = [e["payload"] for e in task_events if e["kind"] == "finished"][-1]
    spec = state["spec"]
    expected = 2 if manifest.get("profile") == "notes_delivery_v1" or sample_id.startswith("candidate") else 1
    if spec.get("read_context_version") != expected:
        raise EvalError("实际读取记忆版本与冻结组别不符")
    if spec.get("analysis") or spec.get("plan") or spec.get("report") or spec.get("read_scope"):
        raise EvalError("普通自由发现组不能配置analysis/plan/report/read_scope")
    observed = r.json_lines(Path(sample_root) / item["evidence"]["observations"])
    item["metrics"].update(derive_metrics(observed, task_events, item["task_id"], expected == 2, len(manifest["files"])))
    item["metrics"]["cost"] = None
    return item


r.inspect_sample = inspect_sample
_first_pair_gate = r.candidate_gate
def notes_mechanism_ok(item):
    metrics = item.get("metrics", {})
    return metrics.get("processed_summary_sources", 0) >= 1 and metrics.get("folded_source_bytes", 0) > 0


def candidate_gate(item):
    passed = _first_pair_gate(item) if item.get("sample_id") == "candidate-ordinary-1" else r.mechanical_ok(item)
    if PROFILE == "notes_delivery_v1":
        passed = passed and notes_mechanism_ok(item)
    return passed


r.candidate_gate = candidate_gate
_base_review_ok = r.review_ok


def quality_ok(review):
    if not _base_review_ok(review): return False
    if PROFILE != "notes_delivery_v1": return True
    checks = review.get("quality_checks", {})
    return set(checks) == set(QUALITY_CHECKS) and all(c.get("verdict") in {"supported", "not_applicable"} for c in checks.values())


r.review_ok = quality_ok


def expansion_reason(state):
    samples = state["samples"]
    baseline, candidate = samples.get(r.ORDER[0]), samples.get(r.ORDER[1])
    if not candidate: return None
    if not r.mechanical_ok(baseline or {}) or not r.candidate_gate(candidate):
        return "首对完整交付或候选12模型/40工具门槛未通过，禁止扩样"
    if not baseline.get("review") or not candidate.get("review"):
        return "首对匿名语义评审未完成，禁止扩样"
    if not r.review_ok(baseline["review"]) or not r.review_ok(candidate["review"]) or sum(candidate["review"]["scores"].values()) < sum(baseline["review"]["scores"].values()):
        return "首对质量不足8分、存在重大错误或候选低于基线，禁止扩样"
    return None


r.expansion_reason = expansion_reason


def dotenv_snapshot(path):
    """只读取运行时项目.env中允许的Provider/传输项；不执行shell或变量展开。"""
    path = Path(path)
    raw = path.read_bytes()
    values = {}
    allowed = {"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}
    for line in raw.decode("utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#"): continue
        if line.startswith("export "): line = line[7:].lstrip()
        key, sep, value = line.partition("=")
        key, value = key.strip(), value.strip()
        if not sep or key not in allowed: continue
        if value[:1] in {"'", '"'}:
            quote = value[0]; end = value.find(quote, 1)
            if end < 0 or value[end+1:].strip() and not value[end+1:].lstrip().startswith("#"):
                raise EvalError("项目.env引号格式不支持；未启动模型")
            value = value[1:end]
        else: value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        values[key] = value
    if not values.get("DEEPSEEK_API_KEY", "").strip(): raise EvalError("项目.env缺少Deepseek凭据；未启动模型")
    return hashlib.sha256(raw).hexdigest(), values


@contextmanager
def runtime_environment(root, values, sample_id="offline"):
    scratch = root / "agent-runtime" / sample_id
    for name in ("home", "tmp"): (scratch / name).mkdir(parents=True, exist_ok=True, mode=0o700)
    env = {"PATH": os.defpath, "HOME": str(scratch / "home"), "TMPDIR": str(scratch / "tmp"), "LANG": "en_US.UTF-8", **values,
           "CLAW_REQUEST_DIAGNOSTICS": "metadata", "CLAW_REQUEST_DIAGNOSTICS_DIR": str(root / "samples" / sample_id / "diagnostics"), "PYTHONDONTWRITEBYTECODE": "1"}
    old = dict(os.environ)
    os.environ.clear(); os.environ.update(env)
    try: yield
    finally: os.environ.clear(); os.environ.update(old)


def check_history(root, manifest, *, state_required=True):
    """评审与逐例档案绑定；续跑不能依赖可随意改动的state评分副本。"""
    state = r.read_json(root / "state.json") if (root / "state.json").exists() else None
    samples = {}
    if state_required and state is None: raise EvalError("续跑缺少状态索引")
    for name in r.ORDER:
        path = root / "samples" / name / "result.json"
        if not path.exists(): continue
        item = r.read_json(path)
        if item.get("sample_id") != name: raise EvalError("逐例样本身份不符")
        if item.get("review"):
            review = r.read_json(path.with_name("review.json"))
            validate_review(root, manifest, name, review)
            if review != item["review"]: raise EvalError("逐例评审与档案不符")
        samples[name] = item
    if state_required and state is not None and state.get("samples") != samples: raise EvalError("状态索引与逐例历史不符，禁止续跑")
    return samples


def run_one(experiment):
    root, manifest = checked_manifest(experiment)
    check_history(root, manifest)
    if (root / "run.lock").exists(): raise EvalError("存在运行锁，禁止并发")
    state = r.read_json(root / "state.json")
    if state.get("stopped"): raise EvalError("实验已停止，不自动重试")
    next_name = next((n for n in r.ORDER if n not in state["samples"]), None)
    if next_name is None: raise EvalError("全部样本已完成")
    if next_name not in r.ORDER[:2] and expansion_reason(state): raise EvalError(expansion_reason(state))
    original_preflight = r.preflight
    def bound_preflight(path):
        original_preflight(path)
        snapshot, values = dotenv_snapshot(manifest["project_dotenv"])
        frozen = root / "runtime-environment.json"
        if frozen.exists() and r.read_json(frozen).get("dotenv_sha256") != snapshot:
            raise EvalError("运行时项目.env快照变化，禁止续跑")
        if not frozen.exists():
            r.write_json(frozen, {"dotenv_sha256": snapshot, "source": "project_dotenv", "values_persisted": False})
        os.environ.update(values)
    r.preflight = bound_preflight
    try:
        with runtime_environment(root, {}, next_name):
            result = _base_run(experiment)
    finally:
        r.preflight = original_preflight
    if manifest["profile"] == "notes_delivery_v1":
        state = r.read_json(root / "state.json")
        baseline = state["samples"].get(r.ORDER[0], {})
        reason = None
        if not r.mechanical_ok(result): reason = "独立双样本未通过完整交付，不扩样或重试"
        if next_name.startswith("candidate") and not within_baseline_budget(result, baseline):
            reason = "候选模型或工具调用超过同对基线，不扩样或重试"
        if reason:
            state.update(stopped=True, stop_reason=reason)
            r.write_json(root / "state.json", state)
    return result


def within_baseline_budget(candidate, baseline):
    for key in ("model_calls", "tool_calls"):
        current, old = candidate.get("metrics", {}).get(key), baseline.get("metrics", {}).get(key)
        if type(current) is not int or type(old) is not int or current > old: return False
    return True


@contextmanager
def experiment_lock(root):
    path = root / "run.lock"
    try: fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600); os.close(fd)
    except FileExistsError: raise EvalError("实验运行中，不能导出或修改评审")
    try: yield
    finally: path.unlink(missing_ok=True)


def artifact_bytes(root, sample_id):
    path = root / "samples" / sample_id / "workspace" / r.OUTPUT
    if path.is_symlink() or not path.is_file(): raise EvalError("评审产物缺失或不是普通文件")
    return path.read_bytes()


def source_evidence(root, manifest, ref):
    sources = {f["path"]: f for f in manifest["files"]}
    if not isinstance(ref, dict) or ref.get("path") not in sources: raise EvalError("评审证据不是冻结来源")
    source = sources[ref["path"]]
    if ref.get("source_sha256") != source["sha256"]: raise EvalError("评审来源SHA不匹配")
    a, b = ref.get("start_line"), ref.get("end_line")
    lines = (root / "fixture" / ref["path"]).read_text(encoding="utf-8").splitlines()
    if type(a) is not int or type(b) is not int or not 1 <= a <= b <= len(lines): raise EvalError("评审来源行号无效")
    if not isinstance(ref.get("quote"), str) or not ref["quote"].strip() or ref["quote"] not in "\n".join(lines[a-1:b]):
        raise EvalError("评审摘录不在真实源行范围")


def validate_review(root, manifest, sample_id, review):
    if not isinstance(review.get("reviewer"), str) or not review["reviewer"].strip() or review.get("method") not in {"human", "source_audit_agent"}:
        raise EvalError("必须填写真实评审者与human/source_audit_agent方式")
    if set(review.get("scores", {})) != set(r.SCORES) or any(type(v) is not int or v not in (0, 1, 2) for v in review["scores"].values()):
        raise EvalError("五项量表均须为0/1/2")
    if set(review.get("score_basis", {})) != set(r.SCORES): raise EvalError("五项量表均须提供实际依据")
    if type(review.get("major_errors")) is not int or review["major_errors"] < 0: raise EvalError("重大错误数无效")
    mapping = r.read_json(root / "review-map.json")
    binding = mapping.get(review.get("review_id"))
    artifact = artifact_bytes(root, sample_id)
    sha = hashlib.sha256(artifact).hexdigest()
    if binding != {"sample_id": sample_id, "artifact_sha256": sha} or review.get("artifact_sha256") != sha:
        raise EvalError("匿名评审身份或产物SHA未绑定到实际样本")
    if not isinstance(review.get("findings"), list) or not review["findings"] or any(not isinstance(f, dict) for f in review["findings"]):
        raise EvalError("须记录实际结论或rubric遗漏")
    identities = [finding.get("id") for finding in review["findings"]]
    if any(not isinstance(identity, str) or not identity.strip() for identity in identities) or len(set(identities)) != len(identities):
        raise EvalError("每个finding必须有唯一非空id")
    for basis in review["score_basis"].values():
        if (not isinstance(basis, dict) or not isinstance(basis.get("reason"), str) or not basis["reason"].strip()
                or not isinstance(basis.get("finding_ids"), list) or not basis["finding_ids"]
                or any(not isinstance(identity, str) for identity in basis["finding_ids"]) or not set(basis["finding_ids"]) <= set(identities)):
            raise EvalError("每项score_basis必须用reason及finding_ids绑定真实证据")
    major_ids = review.get("major_error_ids")
    if (not isinstance(major_ids, list) or any(not isinstance(identity, str) for identity in major_ids)
            or len(major_ids) != len(set(major_ids)) or len(major_ids) != review["major_errors"] or not set(major_ids) <= set(identities)):
        raise EvalError("major_errors必须与已绑定证据的major_error_ids一致")
    rubric = {x["id"]: x for x in r.read_json(root / "harness/rubric.json")["rubrics"]}
    oracle = r.read_json(root / "harness/oracle.json")
    evidence_ids = {e["id"]: (s, e) for s in oracle["sources"] for e in s["evidence"]}
    for finding in review["findings"]:
        kind = finding.get("kind")
        if kind == "claim":
            start, end, claim = finding.get("start_byte"), finding.get("end_byte"), finding.get("claim")
            if type(start) is not int or type(end) is not int or not 0 <= start < end <= len(artifact) or not isinstance(claim, str) or not claim.strip():
                raise EvalError("claim必须绑定真实UTF8字节范围")
            if artifact[start:end] != claim.encode("utf-8"): raise EvalError("claim文本与产物字节span不一致")
            if finding.get("verdict") not in {"supported", "incorrect", "unverified"}: raise EvalError("claim判断无效")
            if not set(finding.get("rubric_ids", [])) <= set(rubric): raise EvalError("claim关联未知rubric")
        elif kind == "omission":
            if any(k in finding for k in ("claim", "start_byte", "end_byte")): raise EvalError("遗漏不能伪造产物claim/span")
            if finding.get("rubric_id") not in rubric or not isinstance(finding.get("reason"), str) or not finding["reason"].strip():
                raise EvalError("遗漏必须绑定rubric_id并说明原因")
        else: raise EvalError("finding必须是claim或omission")
        if finding["id"] in major_ids and kind == "claim" and finding.get("verdict") != "incorrect":
            raise EvalError("重大错误ID须指向incorrect claim或rubric遗漏")
        if not isinstance(finding.get("evidence"), list) or not finding["evidence"]: raise EvalError("finding缺少冻结原文依据")
        for ref in finding["evidence"]: source_evidence(root, manifest, ref)
        if kind == "omission":
            expected = [evidence_ids[k] for k in rubric[finding["rubric_id"]]["evidence_ids"]]
            if not any(ref["path"] == s["path"] and ref["start_line"] <= e["end_line"] and ref["end_line"] >= e["start_line"] for ref in finding["evidence"] for s, e in expected):
                raise EvalError("遗漏依据没有定位到对应rubric来源")
    if manifest["profile"] == "notes_delivery_v1":
        checks = review.get("quality_checks", {})
        if set(checks) != set(QUALITY_CHECKS): raise EvalError("独立双样本须评审分类例外、公式端点与本地交付链接")
        for check in checks.values():
            if (not isinstance(check, dict) or check.get("verdict") not in {"supported", "incorrect", "not_applicable"}
                    or not isinstance(check.get("reason"), str) or not check["reason"].strip()
                    or not isinstance(check.get("finding_ids"), list) or not check["finding_ids"]
                    or not set(check["finding_ids"]) <= set(identities)):
                raise EvalError("专项质量检查须给出结论、理由及真实finding绑定")
    return review


def export_review(experiment, output):
    root, manifest = checked_manifest(experiment, require_current=False)
    target = Path(output).absolute()
    if target == root or target.is_relative_to(root): raise EvalError("匿名评审包必须导出到实验目录之外")
    if target.exists(): raise EvalError("匿名评审导出目标已存在，禁止覆盖")
    with experiment_lock(root):
        samples = check_history(root, manifest)
        names = [n for n, item in samples.items() if item.get("state") == "completed" and (root / "samples" / n / "workspace" / r.OUTPUT).is_file()]
        if not names: raise EvalError("没有可评审的已完成产物")
        mapping = r.read_json(root / "review-map.json") if (root / "review-map.json").exists() else {}
        entries = []
        for name in names:
            sha = hashlib.sha256(artifact_bytes(root, name)).hexdigest()
            existing = [(rid, value) for rid, value in mapping.items() if value["sample_id"] == name]
            if existing and existing[0][1]["artifact_sha256"] != sha: raise EvalError("已导出产物发生变化")
            rid = existing[0][0] if existing else uuid.uuid4().hex
            mapping[rid] = {"sample_id": name, "artifact_sha256": sha}
            entries.append((rid, name, sha))
        random.SystemRandom().shuffle(entries)
        target.mkdir(parents=True, mode=0o700)
        shutil.copytree(root / "fixture", target / "sources")
        shutil.copyfile(root / "harness/rubric.json", target / "rubric.json")
        shutil.copyfile(root / "harness/oracle.json", target / "finite-oracle.json")
        public = []
        for rid, name, sha in entries:
            (target / (rid + ".md")).write_bytes(artifact_bytes(root, name))
            template = {"review_id": rid, "artifact_sha256": sha, "reviewer": "", "method": "", "scores": dict.fromkeys(r.SCORES, None),
                        "score_basis": {score: {"reason": "", "finding_ids": []} for score in r.SCORES},
                        "major_errors": None, "major_error_ids": [], "findings": []}
            if manifest["profile"] == "notes_delivery_v1":
                template["quality_checks"] = {key: {"verdict": None, "reason": "", "finding_ids": []} for key in QUALITY_CHECKS}
            r.write_json(target / (rid + ".review.json"), template)
            public.append({"review_id": rid, "artifact_sha256": sha, "artifact": rid + ".md", "template": rid + ".review.json"})
        r.write_json(target / "index.json", {"schema_version": 1, "goal": manifest["goal"], "reviews": public, "semantic_result": "not_run",
                     "source_versions": [{"path": f["path"], "sha256": f["sha256"]} for f in manifest["files"]],
                     "instructions": "匿名审阅全部资料及产物。原文84.5/39.5转述可正确；未指出与静态推导97.5/29.5的冲突记numeric_discrepancy遗漏，不伪造claim。真实评审身份与method必填。"})
        r.write_json(root / "review-map.json", mapping)
    return {"output": str(target), "reviews": len(entries), "semantic_result": "not_run", "paid_calls": 0}


def record_review(experiment, review_path):
    root, manifest = checked_manifest(experiment)
    with experiment_lock(root):
        check_history(root, manifest)
        review = r.read_json(review_path)
        if not isinstance(review, dict): raise EvalError("评审必须是JSON对象")
        mapping = r.read_json(root / "review-map.json")
        binding = mapping.get(review.get("review_id"))
        if not binding: raise EvalError("未知匿名review_id，请先export-review")
        name = binding["sample_id"]
        state = r.read_json(root / "state.json"); item = state["samples"].get(name)
        if not item or item.get("state") != "completed": raise EvalError("只能评审已完成样本")
        if item.get("review") or (root / "samples" / name / "review.json").exists(): raise EvalError("已有评审不可覆盖")
        validate_review(root, manifest, name, review)
        review = dict(review, reviewed_at=r.now(), semantic_result="pass" if r.review_ok(review) else "fail")
        item["review"] = review
        for candidate_name, candidate in state["samples"].items():
            if not candidate_name.startswith("candidate") or not candidate.get("review"): continue
            baseline = state["samples"].get(candidate_name.replace("candidate-", "baseline-", 1), {})
            if not r.review_ok(candidate["review"]) or baseline.get("review") and sum(candidate["review"]["scores"].values()) < sum(baseline["review"]["scores"].values()):
                state.update(stopped=True, stop_reason="候选质量未通过或低于同对基线")
        r.write_json(root / "samples" / name / "review.json", review)
        r.write_json(root / "samples" / name / "result.json", item)
        r.write_json(root / "state.json", state)
        return review


def summarize(experiment):
    root, manifest = checked_manifest(experiment, require_current=False)
    samples = check_history(root, manifest, state_required=False)
    group = {}
    for arm in ("baseline", "candidate"):
        items = [item for name, item in samples.items() if name.startswith(arm + "-")]
        medians = {}
        for key in ("model_calls", "tool_calls", "input_tokens", "output_tokens", "total_tokens", "cost"):
            values = [item.get("metrics", {}).get(key) for item in items]
            medians[key + "_median"] = statistics.median(values) if values and all(v is not None for v in values) and key != "cost" else None
        group[arm] = {"samples": len(items), "metrics": medians}
    base, candidate = group["baseline"]["metrics"], group["candidate"]["metrics"]
    group["token_median_reduction"] = 1 - candidate["total_tokens_median"] / base["total_tokens_median"] if base["total_tokens_median"] and candidate["total_tokens_median"] is not None else None
    completed = len(samples) == len(r.ORDER) and all(s.get("state") == "completed" for s in samples.values())
    known = completed and all(s.get("review") for s in samples.values())
    quality = known and all(r.review_ok(s["review"]) for s in samples.values())
    if known:
        quality = quality and all(sum(samples[n]["review"]["scores"].values()) >= sum(samples[n.replace("candidate-", "baseline-", 1)]["review"]["scores"].values()) for n in r.ORDER if n.startswith("candidate"))
    valid = all(r.mechanical_ok(s) and not s.get("infrastructure_error") for s in samples.values())
    paired = manifest["profile"] == "notes_delivery_v1"
    efficient = (candidate["model_calls_median"] is not None and candidate["model_calls_median"] <= (12 if paired else 10)
                 and candidate["tool_calls_median"] is not None and candidate["tool_calls_median"] <= (40 if paired else 26)
                 and group["token_median_reduction"] is not None and group["token_median_reduction"] >= .30)
    notes_pass = None
    if paired:
        ca, ba = samples.get(r.ORDER[1], {}), samples.get(r.ORDER[0], {})
        notes_pass = bool(ca and notes_mechanism_ok(ca))
        efficient = efficient and candidate_gate(ca) and within_baseline_budget(ca, ba)
    admissions = [s.get("metrics", {}).get("model_calls") for s in samples.values()]
    state = r.read_json(root / "state.json") if (root / "state.json").exists() else {}
    report = {"experiment_id": manifest["experiment_id"], "completed": completed, "passed": bool(completed and valid and quality and efficient),
              "quality_result": ("pass" if quality else "fail") if known else "not_run",
              "efficiency_result": ("pass" if efficient else "fail") if completed else "not_run", "groups": {"ordinary": group}, "samples": samples,
              "total_model_admissions": sum(admissions) if all(v is not None for v in admissions) else None,
              "known_model_admissions": sum(v for v in admissions if v is not None), "unknown_model_admission_samples": admissions.count(None),
              "profile": manifest["profile"], "max_model_admissions": manifest["max_model_admissions"],
              "notes_gate_pass": notes_pass, "stopped": state.get("stopped", False), "stop_reason": state.get("stop_reason"),
              "expansion_status": "独立双样本禁止扩样" if paired else expansion_reason({"samples": samples}), "limitations": manifest["limitations"]}
    r.write_json(root / "summary.json", report)
    return report


def selfcheck():
    import unittest
    suite = unittest.defaultTestLoader.discover(str(HERE), pattern="test_*.py")
    return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True); sub.add_parser("selfcheck")
    p = sub.add_parser("init")
    for name in ("output", "baseline-bin", "candidate-bin"): p.add_argument("--" + name, required=True)
    p.add_argument("--source-workspace")
    p.add_argument("--profile", choices=tuple(PROFILES), default="work_notes_v3")
    for command in ("run-one", "export-review", "review", "summarize"):
        p = sub.add_parser(command); p.add_argument("--experiment", required=True)
        if command == "export-review": p.add_argument("--output", required=True)
        if command == "review": p.add_argument("--review-file", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "selfcheck": return selfcheck()
        if args.command == "init": result = init_experiment(args.output, args.baseline_bin, args.candidate_bin, args.source_workspace, args.profile)
        elif args.command == "run-one": result = run_one(args.experiment)
        elif args.command == "export-review": result = export_review(args.experiment, args.output)
        elif args.command == "review": result = record_review(args.experiment, args.review_file)
        else: result = summarize(args.experiment)
        print(json.dumps(result, ensure_ascii=False, indent=2)); return 0
    except (EvalError, OSError, ValueError, KeyError, TypeError, r.subprocess.SubprocessError) as error:
        print("评测停止：" + str(error), file=sys.stderr); return 2


if __name__ == "__main__":
    raise SystemExit(main())
