#!/usr/bin/env python3
"""三个固定反例的有限语义 oracle；不联网、不执行课程代码、不调用 Provider。

只对冻结标注中的完整 claim 文本判 pass/fail。未知措辞必须 not_run。
它不验证任意报告，不替代引用机械检查、真实模型质量或独立人工评审。
"""
import argparse
from decimal import Decimal
import hashlib
import json
from pathlib import Path, PurePosixPath


FIXTURE_ROOT = Path(__file__).resolve().parents[2] / "eval/fixtures/write_efficiency_v1"
DEFAULT_ORACLE = FIXTURE_ROOT / "semantic_oracle.json"
DEFAULT_WORKSPACE = FIXTURE_ROOT / "workspace"
LIMITATION = "有限固定标注回归；未知自然语言not_run，不代表真实模型质量或独立人工评审。"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def _safe_source(root, relative):
    path = PurePosixPath(relative)
    if path.is_absolute() or not path.parts or any(p in ("..", ".") for p in path.parts):
        raise ValueError("oracle来源路径无效")
    target = root.joinpath(*path.parts)
    if target.is_symlink() or not target.resolve().is_relative_to(root):
        raise ValueError("oracle来源路径越界或为符号链接")
    return target


def load_oracle(path=DEFAULT_ORACLE):
    oracle = json.loads(Path(path).read_text(encoding="utf-8"))
    if not isinstance(oracle, dict) or oracle.get("schema_version") != 1 or oracle.get("kind") != "finite_labeled_semantic_oracle":
        raise ValueError("未知有限语义oracle版本或种类")
    evidence_ids, source_ids, claim_ids, texts = set(), set(), set(), set()
    for source in oracle["sources"]:
        if source["id"] in source_ids or len(source["sha256"]) != 64:
            raise ValueError("来源身份或摘要无效")
        source_ids.add(source["id"])
        for evidence in source["evidence"]:
            if evidence["id"] in evidence_ids or not 1 <= evidence["start_line"] <= evidence["end_line"]:
                raise ValueError("证据身份或行范围无效")
            evidence_ids.add(evidence["id"])
            if digest(evidence["quote"].encode("utf-8")) != evidence["quote_sha256"]:
                raise ValueError("固定摘录摘要不符")
    for case in oracle["cases"]:
        if not case["evidence_ids"] or not set(case["evidence_ids"]) <= evidence_ids:
            raise ValueError("标注用例引用未知原文证据")
        for claim in case["claims"]:
            text = claim["text"].strip()
            if not text or text in texts or claim["id"] in claim_ids or claim["expected_result"] not in ("pass", "fail"):
                raise ValueError("固定claim重复、为空或判定无效")
            texts.add(text)
            claim_ids.add(claim["id"])
        if "derived_value" in case:
            value = case["derived_value"]
            actual = Decimal(str(value["default_score"])) / Decimal(str(value["denominator"])) * Decimal(str(value["max_score"]))
            if actual != Decimal(str(value["expected_mapped"])):
                raise ValueError("固定默认值算术标注不一致")
    return oracle


def inspect_evidence(oracle, workspace=DEFAULT_WORKSPACE):
    """核对标注依赖的冻结原文；不把待评claim的引用合法性当语义正确。"""
    checks, issues = [], []
    try:
        root = Path(workspace).resolve(strict=True)
        for source in oracle["sources"]:
            path = _safe_source(root, source["path"])
            if path.stat().st_size > 1024 * 1024:
                raise ValueError("oracle来源超过1MiB上限")
            raw = path.read_bytes()
            current = digest(raw)
            if current != source["sha256"]:
                issues.append("来源版本不符：" + source["path"])
                checks.append({"source_id": source["id"], "version_result": "error"})
                continue
            lines = raw.decode("utf-8").splitlines()
            for evidence in source["evidence"]:
                start, end = evidence["start_line"], evidence["end_line"]
                actual = "\n".join(lines[start - 1:end])
                valid = end <= len(lines) and actual == evidence["quote"] and digest(actual.encode("utf-8")) == evidence["quote_sha256"]
                checks.append({"source_id": source["id"], "evidence_id": evidence["id"], "version_result": "pass", "quote_result": "pass" if valid else "error"})
                if not valid:
                    issues.append("固定行号或摘录不符：" + evidence["id"])
    except (OSError, ValueError, KeyError, UnicodeError, TypeError) as exc:
        issues.append(str(exc))
    return {"environment_result": "pass" if not issues else "error", "checks": checks, "issues": issues}


def evaluate_claims(claims, oracle, workspace=DEFAULT_WORKSPACE):
    """只匹配明示完整标注；claim_id、JSON格式及输入引用不能授予通过。"""
    evidence = inspect_evidence(oracle, workspace)
    result = {"schema_version": 1, "oracle_kind": "finite_labeled_semantic_oracle", "checker_result": "pass",
              "environment_result": evidence["environment_result"], "semantic_result": "not_run", "claims": [],
              "oracle_evidence": evidence, "limitations": LIMITATION}
    if not isinstance(claims, list) or any(not isinstance(c, dict) or not isinstance(c.get("text"), str) for c in claims):
        result["checker_result"] = "error"
        result["issues"] = ["claims必须是包含text字符串的对象数组"]
        return result
    if evidence["environment_result"] != "pass":
        return result
    annotations = {}
    for case in oracle["cases"]:
        for claim in case["claims"]:
            annotations[claim["text"].strip()] = (case, claim)
    for item in claims:
        text = item["text"].strip()
        known = annotations.get(text)
        if known is None:
            result["claims"].append({"text": text, "semantic_result": "not_run", "reason": "没有该完整表述的固定标注，未做通用语义判断"})
        else:
            case, annotation = known
            result["claims"].append({"text": text, "case_id": case["id"], "annotation_id": annotation["id"],
                                     "semantic_result": annotation["expected_result"], "reason": annotation["reason"],
                                     "evidence_ids": case["evidence_ids"]})
    outcomes = [c["semantic_result"] for c in result["claims"]]
    result["known_claims"] = sum(o != "not_run" for o in outcomes)
    result["unknown_claims"] = outcomes.count("not_run")
    if "fail" in outcomes:
        result["semantic_result"] = "fail"
    elif outcomes and all(o == "pass" for o in outcomes):
        result["semantic_result"] = "pass"
    # 有一条未知时，已知部分通过不能冒充整批结论通过。
    return result


def selfcheck(oracle, workspace=DEFAULT_WORKSPACE):
    checks = []
    environment = inspect_evidence(oracle, workspace)
    for case in oracle["cases"]:
        for claim in case["claims"]:
            actual = evaluate_claims([{"text": claim["text"]}], oracle, workspace)
            checks.append({"annotation_id": claim["id"], "expected": claim["expected_result"], "actual": actual["semantic_result"]})
    unknown = evaluate_claims([{"text": "这是未被标注的全新表述"}], oracle, workspace)
    checks.append({"annotation_id": "unknown_must_not_run", "expected": "not_run", "actual": unknown["semantic_result"]})
    passed = environment["environment_result"] == "pass" and all(c["expected"] == c["actual"] for c in checks)
    return {"schema_version": 1, "checker_result": "pass" if passed else "error", "environment_result": environment["environment_result"],
            "semantic_result": "not_run", "selfcheck_result": "pass" if passed else "fail", "checks": checks, "limitations": LIMITATION}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("selfcheck", "check"))
    parser.add_argument("--oracle", type=Path, default=DEFAULT_ORACLE)
    parser.add_argument("--workspace", type=Path, default=DEFAULT_WORKSPACE)
    parser.add_argument("--claims", type=Path, help='check输入：{"claims":[{"text":"完整已标注或未知结论"}]}')
    args = parser.parse_args(argv)
    try:
        oracle = load_oracle(args.oracle)
        if args.command == "selfcheck":
            result = selfcheck(oracle, args.workspace)
            code = 0 if result["selfcheck_result"] == "pass" else 2
        else:
            if args.claims is None:
                raise ValueError("check必须显式指定--claims，不自动判定任意Markdown报告")
            payload = json.loads(args.claims.read_text(encoding="utf-8"))
            if not isinstance(payload, dict):
                raise ValueError("claim输入必须是包含claims数组的JSON对象")
            result = evaluate_claims(payload.get("claims"), oracle, args.workspace)
            code = 2 if result["checker_result"] == "error" or result["environment_result"] != "pass" else {"pass": 0, "fail": 1, "not_run": 3}[result["semantic_result"]]
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, ArithmeticError) as exc:
        result = {"schema_version": 1, "checker_result": "error", "semantic_result": "not_run", "issues": [str(exc)], "limitations": LIMITATION}
        code = 2
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
