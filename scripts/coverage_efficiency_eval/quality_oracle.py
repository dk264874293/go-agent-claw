#!/usr/bin/env python3
"""覆盖与报告质量v2的有限完整文本标注；不执行模型、课程代码或宿主。

复用v1的完整claim匹配器，另核对v2的合成场景前提。
固定标注回归不是通用语义判断，也不是完整历史轨迹回放。
"""
import argparse
from decimal import Decimal
import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_ORACLE = ROOT / "eval/fixtures/coverage_quality_v2/semantic_oracle.json"
DEFAULT_WORKSPACE = ROOT / "eval/fixtures/write_efficiency_v1/workspace"
_base_path = Path(__file__).with_name("semantic_oracle.py")
if not _base_path.is_file():
    _base_path = ROOT / "scripts/write_efficiency_eval/semantic_oracle.py"
_spec = importlib.util.spec_from_file_location("write_efficiency_finite_oracle_v1", _base_path)
_base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_base)
LIMITATIONS = "有限完整文本标注；未知表述not_run；脱敏缺页是症状夹具，不是完整历史回放；标注者为source_audit_agent，非独立人工评审。"


def validate_assumptions(oracle):
    """只校验固定场景的机械前提，不能把这些前提当成真实Agent的新回执。"""
    if oracle.get("fixture_version") not in (2, 3):
        raise ValueError("未知质量夹具版本")
    annotation = oracle.get("annotation", {})
    if annotation.get("method") != "source_audit_agent" or annotation.get("independent_human_review") is not False or annotation.get("course_code_executed") is not False or annotation.get("provider_calls") != 0:
        raise ValueError("质量标注身份或执行边界无效")
    sources = {source["id"]: source for source in oracle["sources"]}
    identities = set()
    for assumption in oracle["assumptions"]:
        identity = assumption["id"]
        if identity in identities:
            raise ValueError("场景前提身份重复")
        identities.add(identity)
        kind = assumption["kind"]
        if kind == "synthetic_reading_coverage":
            source = sources[assumption["source_id"]]
            if assumption["source_sha256"] != source["sha256"] or assumption["complete_historical_replay"] is not False:
                raise ValueError("缺页前提来源或历史回放标注错误")
            total = assumption["total_bytes"]
            if type(total) is not int or total < 0:
                raise ValueError("缺页前提总长度无效")
            ranges = assumption["read_ranges"]
            for interval in ranges:
                if any(type(interval[k]) is not int for k in ("start", "end")) or not 0 <= interval["start"] <= interval["end"] <= total:
                    raise ValueError("缺页前提读取范围无效")
            cursor, missing = 0, []
            for interval in sorted(ranges, key=lambda x: (x["start"], x["end"])):
                if interval["start"] > cursor:
                    missing.append({"start": cursor, "end": interval["start"]})
                cursor = max(cursor, interval["end"])
            if cursor < total:
                missing.append({"start": cursor, "end": total})
            missing_bytes = sum(x["end"] - x["start"] for x in missing)
            if missing != assumption["expected_missing"] or missing_bytes != assumption["expected_missing_bytes"]:
                raise ValueError("缺页前提的区间或字节数不一致")
        elif kind == "static_review_only":
            if any(assumption.get(k) is not False for k in ("host_executed", "course_code_executed", "screenshots_reviewed")):
                raise ValueError("静态Hook场景不能冒充执行或截图验证")
        elif kind == "static_input":
            data = assumption["input"]
            if "score" in data or assumption.get("course_code_executed") is not False:
                raise ValueError("静默偏差场景必须没有顶层score且未执行课程代码")
            denominator = Decimal(str(assumption["denominator"]))
            maximum = Decimal(str(assumption["max_score"]))
            current = Decimal(str(assumption["default_score"])) / denominator * maximum
            nested = Decimal(str(data["analysis"]["relevance_score"])) / denominator * maximum
            if current != Decimal(str(assumption["expected_current_mapped"])) or nested != Decimal(str(assumption["expected_nested_mapped"])) or current <= nested:
                raise ValueError("静默偏高场景的算术前提错误")
        elif kind == "static_score_components" and oracle.get("fixture_version") == 3:
            # 固定五维常量相加，不解析或执行来源中的Python/任意表达式。
            if assumption.get("course_code_executed") is not False:
                raise ValueError("静态分数不能冒充课程执行")
            if set(assumption["components"]) != {"summary", "depth", "format", "tags", "hollow"}:
                raise ValueError("静态评分维度不完整")
            frozen = {
                "static_good_score": ({"summary": "25", "depth": "22.5", "format": "20", "tags": "15", "hollow": "15"}, "97.5"),
                "static_bad_score": ({"summary": "5", "depth": "12.5", "format": "12", "tags": "0", "hollow": "0"}, "29.5"),
            }
            if identity not in frozen or (assumption["components"], assumption["expected_total"]) != frozen[identity]:
                raise ValueError("未知或改变的冻结评分前提")
            values = [Decimal(str(v)) for v in assumption["components"].values()]
            if any(not v.is_finite() or v < 0 for v in values) or sum(values) != Decimal(str(assumption["expected_total"])):
                raise ValueError("静态分数算术前提错误")
        else:
            raise ValueError("未知质量场景前提")
    for case in oracle["cases"]:
        if not set(case.get("assumption_ids", [])) <= identities:
            raise ValueError("质量用例依赖未知场景前提")


def load_oracle(path=DEFAULT_ORACLE):
    oracle = _base.load_oracle(path)
    validate_assumptions(oracle)
    return oracle


def inspect_evidence(oracle, workspace=DEFAULT_WORKSPACE):
    result = _base.inspect_evidence(oracle, workspace)
    try:
        validate_assumptions(oracle)
        root = Path(workspace).resolve(strict=True)
        sources = {source["id"]: source for source in oracle["sources"]}
        for assumption in oracle["assumptions"]:
            if assumption["kind"] == "synthetic_reading_coverage":
                source = sources[assumption["source_id"]]
                if _base._safe_source(root, source["path"]).stat().st_size != assumption["total_bytes"]:
                    raise ValueError("缺页前提总长与冻结来源实际字节数不符")
    except (OSError, ValueError, KeyError, TypeError, ArithmeticError) as exc:
        result["issues"].append(str(exc))
        result["environment_result"] = "error"
    return result


def evaluate_claims(claims, oracle=None, workspace=DEFAULT_WORKSPACE):
    if oracle is None:
        oracle = load_oracle()
    checked = inspect_evidence(oracle, workspace)
    if checked["environment_result"] != "pass":
        return {"schema_version": 1, "checker_result": "pass", "environment_result": "error", "semantic_result": "not_run", "claims": [], "oracle_evidence": checked, "limitations": LIMITATIONS}
    result = _base.evaluate_claims(claims, oracle, workspace)
    result.update(annotation=oracle["annotation"], assumptions_validated=True, complete_historical_replay=False, limitations=LIMITATIONS)
    return result


def selfcheck(oracle=None, workspace=DEFAULT_WORKSPACE):
    if oracle is None:
        oracle = load_oracle()
    checked = inspect_evidence(oracle, workspace)
    checks = []
    for case in oracle["cases"]:
        for claim in case["claims"]:
            result = evaluate_claims([{"text": claim["text"]}], oracle, workspace)
            checks.append({"annotation_id": claim["id"], "expected": claim["expected_result"], "actual": result["semantic_result"]})
    result = evaluate_claims([{"text": "未标注的真实报告结论"}], oracle, workspace)
    checks.append({"annotation_id": "unknown_not_run", "expected": "not_run", "actual": result["semantic_result"]})
    passed = checked["environment_result"] == "pass" and all(x["expected"] == x["actual"] for x in checks)
    return {"schema_version": 1, "checker_result": "pass" if passed else "error", "environment_result": checked["environment_result"], "selfcheck_result": "pass" if passed else "fail", "semantic_result": "not_run", "checks": checks, "annotation": oracle["annotation"], "complete_historical_replay": False, "limitations": LIMITATIONS}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("selfcheck", "check"))
    parser.add_argument("--oracle", type=Path, default=DEFAULT_ORACLE)
    parser.add_argument("--workspace", type=Path, default=DEFAULT_WORKSPACE)
    parser.add_argument("--claims", type=Path)
    args = parser.parse_args(argv)
    try:
        oracle = load_oracle(args.oracle)
        if args.command == "selfcheck":
            result = selfcheck(oracle, args.workspace)
            code = 0 if result["selfcheck_result"] == "pass" else 2
        else:
            if args.claims is None:
                raise ValueError("check必须指定完整claim输入，不自动判断任意报告")
            claims = json.loads(args.claims.read_text(encoding="utf-8"))
            result = evaluate_claims(claims["claims"], oracle, args.workspace)
            code = 2 if result["checker_result"] == "error" or result["environment_result"] != "pass" else {"pass": 0, "fail": 1, "not_run": 3}[result["semantic_result"]]
    except (OSError, ValueError, KeyError, TypeError, ArithmeticError) as exc:
        result = {"schema_version": 1, "checker_result": "error", "semantic_result": "not_run", "issues": [str(exc)], "limitations": LIMITATIONS}
        code = 2
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
