"""笔记与交付的独立有限症状回归；不运行模型、原文脚本或任意表达式。"""
from decimal import Decimal
import hashlib
import json
from pathlib import Path


DEFAULT_CASES = Path(__file__).with_name("delivery_cases.json")
if not DEFAULT_CASES.exists():
    DEFAULT_CASES = Path(__file__).resolve().parents[2] / "eval/fixtures/notes_delivery_v1/cases.json"


def load_cases(path=DEFAULT_CASES):
    cases = json.loads(Path(path).read_text(encoding="utf-8"))
    if cases.get("schema_version") != 1 or cases.get("course_code_executed") is not False or cases.get("provider_calls") != 0:
        raise ValueError("反例执行边界无效")
    inspect_cases(cases)
    return cases


def inspect_cases(cases):
    for source in cases["sources"]:
        if hashlib.sha256(source["content"].encode()).hexdigest() != source["sha256"]:
            raise ValueError("合成来源摘要变化")
    inventory = cases["inventory"]
    counts = {}
    for item in inventory:
        counts.setdefault(item["group"], {})
        group = counts[item["group"]]
        group[item["kind"]] = group.get(item["kind"], 0) + 1
    if counts != cases["expected_classification"]:
        raise ValueError("分类例外前提不一致")
    # 固定数据的乘除，不解释来源代码或模型给出的表达式。
    for formula in cases["formulas"]:
        denominator, maximum = Decimal(formula["denominator"]), Decimal(formula["maximum"])
        if denominator <= 0: raise ValueError("分母无效")
        for key in ("minimum_input", "maximum_input", "default_input"):
            value = Decimal(formula[key]) / denominator * maximum
            if value != Decimal(formula["expected_" + key]): raise ValueError("公式端点或默认值前提不一致")
    symptom = cases["sanitized_symptom"]
    if symptom["complete_historical_replay"] or symptom["artifact_version_matches"] is not True:
        raise ValueError("脱敏症状不能冒充完整历史回放")
    if symptom["final_link"] == symptom["confirmed_link"] or symptom["accepted_notes"] != 0 or symptom["folded_source_bytes"] != 0:
        raise ValueError("脱敏症状不再包含原问题")
    return {"environment_result": "pass", "semantic_result": "not_run", "provider_calls": 0}


def evaluate_claim(text, cases):
    inspect_cases(cases)
    labels = {claim["text"]: claim["expected"] for claim in cases["claims"]}
    return labels.get(text.strip(), "not_run")


def selfcheck(cases=None):
    cases = cases or load_cases()
    inspect_cases(cases)
    results = [evaluate_claim(claim["text"], cases) == claim["expected"] for claim in cases["claims"]]
    results.append(evaluate_claim("未标注的新报告结论", cases) == "not_run")
    return {"selfcheck_result": "pass" if all(results) else "fail", "semantic_result": "not_run", "checks": len(results), "provider_calls": 0}
