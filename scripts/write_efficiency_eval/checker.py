#!/usr/bin/env python3
"""独立只读机械检查；不调用模型、不执行课程代码，不承担通用语义判定。"""
import argparse
import hashlib
import json
from pathlib import Path
import re

OUTPUT = "优化汇总.md"

def inspect(workspace, files):
    root = Path(workspace).resolve(strict=True)
    checks, issues, citations = {}, [], []
    wanted = {x["path"]: x for x in files}
    actual = {}
    for path in root.rglob("*"):
        name = path.relative_to(root).as_posix()
        if ".claw" in path.relative_to(root).parts:
            continue
        if path.is_symlink():
            issues.append("禁止的符号链接: " + name)
        elif path.is_file():
            actual[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    checks["source_versions"] = all(actual.get(name) == item["sha256"] for name, item in wanted.items())
    checks["only_declared_output"] = set(actual) == set(wanted) | {OUTPUT} and not issues
    output = root / OUTPUT
    text = ""
    if output.is_file() and not output.is_symlink():
        try:
            text = output.read_text(encoding="utf-8")
        except UnicodeError:
            issues.append("产物不是有效UTF-8")
    checks["substantial_document"] = len(text.strip()) >= 300
    checks["topic_structure"] = all(any(term in text for term in options) for options in [
        ["建议", "优化"], ["版本", "V1", "V2"], ["默认", "score", ".get("], ["待", "未验证", "未运行"], ["JSON", "Schema", "字段", "契约"]])
    # 扫描所有形如 .md:行 的引用；未知路径不能因三条真引用而被静默忽略。
    pattern = r"\.md(?:`)?:(\d+)(?:[-–](\d+))?"
    line_cache = {}
    for match in re.finditer(pattern, text):
        path_end = match.start() + 3
        candidates = []
        # 优先匹配完整冻结路径，文件名本身可以有逗号、括号和多个.md。
        # /等路径字符不能成为假前缀，把unknown/真实路径伪装成已知文件。
        for name in wanted:
            escaped = re.sub(r"([\\<>\[\]`*_])", r"\\\1", name)
            for shown in {name, escaped, "./" + name, "./" + escaped}:
                start = path_end - len(shown)
                if start >= 0 and text[start:path_end] == shown and (start == 0 or text[start-1].isspace() or text[start-1] in "`(<[\"'：:=*、，,;"):
                    if name not in candidates:
                        candidates.append(name)
        if len(candidates) == 1:
            shown = candidates[0]
        else:
            prefix = text[:path_end]
            boundary = max((prefix.rfind(char) for char in "\n`<>\"'();；,，、"), default=-1)
            shown = prefix[boundary+1:].strip()
            shown = re.sub(r"^(?:[-*+]\s+|[0-9]+[.)]\s+)", "", shown)
            shown = re.sub(r"^(?:引用|证据|来源|出处)[：:]\s*", "", shown)
        first, last = int(match[1]), int(match[2] or match[1])
        known = len(candidates) == 1
        if known and shown not in line_cache:
            line_cache[shown] = (root / shown).read_text(encoding="utf-8").splitlines()
        valid = known and 1 <= first <= last <= len(line_cache[shown])
        item = {"path": shown, "start_line": first, "end_line": last, "known_file": known, "valid": valid}
        if item not in citations:
            citations.append(item)
    checks["located_citations"] = len({x["path"] for x in citations if x["valid"]}) >= 3
    checks["citation_ranges"] = all(x["valid"] for x in citations)
    # 该历史错配有真实反例，不能因找不到文件而静默忽略。
    checks["no_ellipsis_path"] = not bool(re.search(r"(?:\.\.\.|…)[^\n`]*\.md:[0-9]", text))
    for name, passed in checks.items():
        if not passed:
            issues.append(name)
    return {"schema_version": 1, "artifact_result": "pass" if all(checks.values()) else "fail",
            "scope_result": "pass" if checks["source_versions"] and checks["only_declared_output"] else "fail",
            "semantic_result": "not_run", "checks": checks, "citations": citations, "issues": issues,
            "limitations": "只验证文件、格式、主题提示及引用行范围；不能证明引文支撑结论、版本迁移或默认行为解释正确。"}

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--workspace", required=True); p.add_argument("--manifest", required=True)
    args = p.parse_args()
    files = json.loads(Path(args.manifest).read_text())["files"]
    print(json.dumps(inspect(args.workspace, files), ensure_ascii=False))

if __name__ == "__main__":
    main()
