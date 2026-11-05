import json
import re
import sys
from pathlib import Path


def check(condition, message):
    if not condition:
        raise ValueError(message)


def verify(workspace, response):
    pristine = Path(__file__).resolve().parent / "workspace"
    expected_files = {p.relative_to(pristine) for p in pristine.rglob("*") if p.is_file()}
    actual_files = {
        p.relative_to(workspace) for p in (workspace / "evidence").rglob("*") if p.is_file()
    }
    check(actual_files == expected_files, "证据文件集合发生变化")
    for path in expected_files:
        check((workspace / path).read_bytes() == (pristine / path).read_bytes(), f"证据已修改: {path}")

    raw = response.read_text(encoding="utf-8").strip()
    fenced = re.fullmatch(r"```(?:json)?\s*(.*?)\s*```", raw, re.S | re.I)
    data = json.loads(fenced[1] if fenced else raw)
    cancel = data["cancel"]
    resume = data["resume"]
    deadline = data["deadline"]

    def read_rows(name):
        return [json.loads(row) for row in (pristine / "evidence" / name).read_text().splitlines()]

    start, replay, timed = read_rows("launcher.jsonl")
    signal_rows = read_rows("signal-events.jsonl")
    result = signal_rows[7]["action"]
    cancelled, recovered = signal_rows[8], signal_rows[10]
    expired = read_rows("deadline-events.jsonl")[3]
    _, resume_trace, _ = read_rows("traces.jsonl")

    def fields(got, expected, section):
        for field, value in expected.items():
            check(type(got[field]) is type(value) and got[field] == value, f"{section}.{field} 与证据不符")

    fields(cancel, {
        "run_id": start["run_id"],
        "exit_code": start["exit_code"],
        "stop_code": cancelled["result"]["stop_code"],
        "action_id": result["action_id"],
        "action_status": result["status"],
        "action_effect": result["effect"],
        "started": start["marker_observed"] and start["sent_after_marker"],
        "verified": cancelled["verification_recorded"],
    }, "cancel")
    fields(resume, {
        "run_id": replay["run_id"],
        "exit_code": replay["exit_code"],
        "status": recovered["result"]["status"],
        "model_calls_delta": recovered["usage"]["model_calls"] - cancelled["usage"]["model_calls"],
        "tool_calls_delta": recovered["usage"]["tool_calls"] - cancelled["usage"]["tool_calls"],
        "replayed": "Action" in resume_trace["direct_spans"],
    }, "resume")
    fields(deadline, {
        "run_id": timed["run_id"],
        "exit_code": timed["exit_code"],
        "status": expired["result"]["status"],
        "stop_code": expired["result"]["stop_code"],
        "tool_calls": expired["usage"]["tool_calls"],
    }, "deadline")
    decision = resume["decision"]
    check(isinstance(decision, str), "resume.decision 应为文字")
    check(
        re.search(r"未知|不明|不确定|未确认|无法确认|尚未核实|UNKNOWN", decision, re.I)
        and re.search(r"人工|外部|权威|管理员|操作员|人员", decision)
        and re.search(r"核[实对查]|查[询证看]|对[账照]|确认|验证|核验|检查", decision)
        and not re.search(r"可以(?:直接|立即)(?:重放|重试|重跑)|无需(?:人工|核实|验证)", decision),
        "继续条件须说明效果未知并先核实实际效果",
    )

    expected = {
        "marker": ("evidence/verification-started.txt", 1),
        "action": ("evidence/signal-events.jsonl", 8),
        "resume": ("evidence/signal-events.jsonl", 11),
        "deadline": ("evidence/deadline-events.jsonl", 4),
    }
    approved = set()
    for reference in data["citations"]:
        path = reference["path"].removeprefix("./")
        line = reference["line"]
        check(type(line) is int and Path(path) in expected_files, "引用路径或行号无效")
        text_lines = (pristine / path).read_text().splitlines()
        check(1 <= line <= len(text_lines), "引用超出文件")
        claim = reference["claim"]
        if (path, line) == expected.get(claim):
            approved.add(claim)
    check(approved == set(expected), f"缺少直接证据: {sorted(set(expected) - approved)}")


if __name__ == "__main__":
    try:
        verify(Path(sys.argv[1]), Path(sys.argv[2]))
    except (IndexError, KeyError, AttributeError, TypeError, ValueError, OSError) as exc:
        sys.exit(f"R02 未通过：{exc}")
    print("R02 的运行关联、状态、未知效果、恢复计数和四组引用通过")
