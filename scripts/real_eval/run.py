#!/usr/bin/env python3
"""真实任务双组评估。评分与引擎独立，所有结果可离线重算。"""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import uuid

REPO = Path(__file__).resolve().parents[2]
DEFAULT_SUITE = REPO / 'eval/real/v2'
FIRST = ('W02', 'W03')
REST = ('R01', 'R02', 'W01')
LEGACY_MODELS = {'c39798d4c332505abffad629621107d8e74a41020991e442419976b0dd66a3c0': 'gpt-6.1-sol'}
DIMENSIONS = ('artifact_result', 'scope_result', 'engine_result', 'environment_result', 'checker_result')


def read_json(path):
    return json.loads(Path(path).read_text())


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + '.tmp')
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    temp.replace(path)


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def tree_hashes(root):
    return {p.relative_to(root).as_posix(): digest(p) for p in sorted(Path(root).rglob('*'))
            if p.is_file() and '__pycache__' not in p.parts and p.suffix != '.pyc'}


def object_digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def snapshot(work):
    result = {}
    for p in sorted(Path(work).rglob('*')):
        rel = p.relative_to(work)
        if rel.parts[0] == '.claw':
            continue
        mode = p.lstat().st_mode
        if p.is_symlink():
            result[rel.as_posix()] = ['symlink', os.readlink(p)]
        elif stat.S_ISREG(mode):
            result[rel.as_posix()] = ['file', stat.S_IMODE(mode), digest(p)]
        elif stat.S_ISDIR(mode):
            result[rel.as_posix()] = ['dir', stat.S_IMODE(mode)]
        else:
            result[rel.as_posix()] = ['other', mode]
    return result


def scope_result(before, after, policy):
    changes = sorted(k for k in before.keys() | after.keys() if before.get(k) != after.get(k))
    forbidden = []
    for path in changes:
        operation = 'modify' if path in before and path in after else 'delete' if path in before else 'create_optional'
        # 新建文件的父目录仅作为容器，不能借此放宽其他文件或权限变化。
        parent_container = operation == 'create_optional' and after[path][0] == 'dir' and any(p.startswith(path + '/') for p in policy['create_optional'])
        if path not in policy[operation] and not parent_container:
            forbidden.append(path)
        elif path in after and after[path][0] != 'file' and not parent_container:
            forbidden.append(path)
    return changes, sorted(set(forbidden))


def prompt_for(suite, case):
    policy = case['write_policy']
    contract = '\n\n文件范围约束（验收使用同一规则）：\n'
    for key, label in [('modify', '允许修改既有文件'), ('create_optional', '允许新增文件'), ('delete', '允许删除文件')]:
        contract += label + '：' + ('、'.join(policy[key]) or '无') + '。\n'
    contract += '其他文件必须保持不变。引擎自己的 .claw 运行记录不参与文件范围评分。\n'
    return (suite / case['prompt']).read_text() + contract


def load_suite(path):
    path = Path(path).resolve()
    suite = read_json(path / 'suite.json')
    if suite.get('schema_version') != 2 or {c['id'] for c in suite['cases']} != set(FIRST + REST):
        raise ValueError('用例清单版本或集合错误')
    for c in suite['cases']:
        for key in ('prompt', 'fixture', 'oracle'):
            p = path / c[key]
            if not p.exists() or not p.resolve().is_relative_to(path):
                raise ValueError('用例资源缺失或越界')
        if c['repetitions'] != 3:
            raise ValueError('本轮固定每例重复三次')
    return path, suite


def clean(text, key=''):
    if key:
        text = text.replace(key, '[REDACTED]')
    text = re.sub(r'sk-[A-Za-z0-9*_-]{5,}', '[REDACTED]', text)
    return re.sub(r'(https?://)[^\s/@:]+:[^\s/@]+@', r'\1[REDACTED]@', text)


def redact_runtime(work, key):
    """进程退出后脱敏运行日志，JSON 中的状态与回执结构保持有效。"""
    for path in (Path(work) / '.claw').rglob('*'):
        if not path.is_file() or path.is_symlink():
            continue
        try:
            original = path.read_text()
        except UnicodeError:
            continue
        redacted = clean(original, key)
        if redacted != original:
            path.write_text(redacted)


def child(cmd, cwd, env, timeout):
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True, start_new_session=True)
    try:
        out, err = proc.communicate(timeout=timeout)
        return proc.returncode, out, err
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        out, err = proc.communicate()
        return 124, out, err + '\n外层进程超时，进程组已停止'


def sandbox_context(root, suite):
    root, suite = Path(root).resolve(), Path(suite).resolve()
    scratch = root / 'checker-runtime'
    for name in ('home', 'tmp', 'cache'):
        (scratch / name).mkdir(parents=True, exist_ok=True)
    mod = Path(subprocess.check_output(['go', 'env', 'GOMODCACHE'], text=True).strip()).resolve()
    env = {'PATH': os.environ.get('PATH', os.defpath), 'HOME': str(scratch / 'home'),
           'TMPDIR': str(scratch / 'tmp'), 'GOCACHE': str(scratch / 'cache'), 'GOMODCACHE': str(mod),
           'GOPROXY': 'off', 'GOSUMDB': 'off', 'GOVCS': '*:off', 'GOTOOLCHAIN': 'local',
           'GOENV': 'off', 'GOWORK': 'off', 'GOTELEMETRY': 'off', 'CGO_ENABLED': '0'}
    def paths(items):
        return ' '.join('(subpath ' + json.dumps(str(p)) + ')' for p in items)
    profile = ('(version 1)(allow default)(deny network*)(deny file-write*)'
               '(deny file-read* ' + paths([Path.home(), Path('/private/tmp')]) + ')'
               '(allow file-read* ' + paths([root, suite, mod]) + ')'
               '(allow file-write* ' + paths([scratch]) + ' (literal "/dev/null"))')
    return scratch, env, profile


def checker(root, suite, case, work, answer, evidence):
    scratch, env, profile = sandbox_context(root, suite)
    attempt = scratch / ('check-' + uuid.uuid4().hex)
    attempt.mkdir()
    stage = attempt / 'workspace'
    shutil.copytree(work, stage, symlinks=True, ignore=shutil.ignore_patterns('.claw'))
    shutil.copyfile(answer, attempt / 'answer.txt')
    shutil.copyfile(evidence, attempt / 'evidence.json')
    command = ['/usr/bin/sandbox-exec', '-p', profile, sys.executable, str(suite / case['oracle']),
               str(stage)]
    if case['id'].startswith('R'):
        command.extend([str(attempt / 'answer.txt'), str(attempt / 'evidence.json')])
    code, out, err = child(command, stage, env, 110)
    output = out + err
    infrastructure = code in (124, 125, 126, 127) or bool(re.search(
        r'sandbox-exec:|module lookup disabled|failed to initialize build cache|no such file or directory.*go|failed to download|无法定位缺陷注入点', output, re.I))
    return code, output, infrastructure


def preflight(root, suite):
    if sys.platform != 'darwin' or not Path('/usr/bin/sandbox-exec').is_file() or not shutil.which('go'):
        raise RuntimeError('当前真实评估要求 macOS sandbox-exec 与本地 Go 工具链')
    scratch, env, profile = sandbox_context(root, suite)
    code, out, err = child(['/usr/bin/sandbox-exec', '-p', profile, sys.executable, '-c',
        'import os,socket,sys; assert not any("API_KEY" in k for k in os.environ); s=socket.socket();\ntry:\n s.connect(("127.0.0.1",9))\nexcept PermissionError:\n sys.exit(0)\nexcept OSError:\n sys.exit(2)\nsys.exit(3)'], scratch, env, 10)
    if code != 0:
        raise RuntimeError('沙箱网络或凭据隔离预检失败：' + out + err)
    code, out, err = child(['/usr/bin/sandbox-exec', '-p', profile, 'go', 'list', '-mod=readonly', '-deps', './internal/provider'],
                           suite / 'cases/W02/workspace', env, 45)
    if code:
        raise RuntimeError('离线 Go 依赖预检失败：' + out + err)


def read_task(work, session_id):
    matches = []
    for task in sorted((work / '.claw/tasks').glob('*')):
        if not task.is_dir():
            continue
        state, sequence = None, 0
        snap = task / 'snapshot.json'
        if snap.exists():
            try:
                value = read_json(snap)
                if value['version'] == 1 and value['state']['version'] == 1:
                    state, sequence = value['state'], value['sequence']
            except (ValueError, KeyError, TypeError):
                pass
        events = []
        event_path = task / 'events.jsonl'
        if event_path.exists():
            lines = event_path.read_bytes().splitlines(keepends=True)
            previous = 0
            for index, line in enumerate(lines):
                if not line.endswith(b'\n') and index == len(lines) - 1:
                    break
                event = json.loads(line)
                if event['version'] != 1 or event['sequence'] != previous + 1:
                    raise ValueError('任务事件版本或序号无效')
                previous = event['sequence']
                events.append(event)
                if previous >= sequence and event['kind'] != 'elapsed':
                    value = event['payload']
                    if value.get('version') != 1:
                        raise ValueError('任务状态版本无效')
                    state, sequence = value, previous
        if state and state.get('session_id') == session_id:
            last = state.get('last_result')
            if not last or last.get('task_id') != task.name:
                raise ValueError('缺少完整任务终态')
            matches.append((task.name, state, events))
    if len(matches) != 1:
        raise ValueError('无法确定唯一的本次任务结果')
    return matches[0]


def read_evidence(events):
    reads = []
    for event in events:
        if event['kind'] != 'action_result':
            continue
        action = event['payload'].get('last_action') or {}
        result = action.get('result') or {}
        if action.get('tool') != 'read_file' or result.get('status') != 'SUCCEEDED':
            continue
        text = result.get('output', '')
        path = re.search(r'^文件: ([^\n]+)', text)
        version = re.search(r'^版本: ([0-9a-f]{64})$', text, re.M)
        window = re.search(r'^字节范围: \[(\d+), (\d+)[)）]', text, re.M)
        if path and version and window:
            reads.append({'path': path[1].removeprefix('./'), 'version': version[1], 'start': int(window[1]), 'end': int(window[2])})
    return {'reads': reads}


def finish_dimensions(result, case):
    labels = []
    if result['scope_result'] == 'fail':
        labels.append('scope_failure')
    if result.get('engine_status') == 'BUDGET_EXHAUSTED' or result.get('exit_code') == 124:
        labels.append('budget_exhausted')
    for key, label in [('artifact_result', 'artifact_failure'), ('engine_result', 'engine_failure'),
                       ('environment_result', 'environment_failure'), ('checker_result', 'checker_failure')]:
        if result[key] in ('fail', 'error'):
            labels.append(label)
    result['labels'] = labels
    result['combined_pass'] = all(result[k] == 'pass' for k in DIMENSIONS) and (
        not case['require_final_text'] or result.get('final_text_present', False)) and result.get('pending_action_count') == 0
    return result


def run_one(root, suite, config, case, arm, repeat, key):
    identifier = f'{case["id"]}-{repeat}-{arm}'
    run_dir = root / 'runs' / identifier
    if (run_dir / 'result.json').is_file():
        return read_json(run_dir / 'result.json')
    if run_dir.exists():
        # 没有终态的旧尝试保留；本次为独立样本尝试，仍受已记账总预算约束。
        raise RuntimeError(f'{identifier} 存在未完成尝试，禁止自动重试，请核实并创建新实验')
    run_dir.mkdir(parents=True)
    work = run_dir / 'workspace'
    shutil.copytree(suite / case['fixture'], work, symlinks=True)
    before = snapshot(work)
    write_json(run_dir / 'before.json', before)
    prompt = prompt_for(suite, case)
    (run_dir / 'prompt.txt').write_text(prompt)
    session = 'eval-' + uuid.uuid4().hex
    b = config['budget']
    command = [str(root / 'bin' / arm), '-dir', str(work), '-session', session,
               '-mode', case['mode'], '-task-kind', case['task_kind'], '-max-model-calls', str(b['max_model_calls']),
               '-max-tool-calls', str(b['max_tool_calls']), '-max-turns', str(b['max_turns']),
               '-timeout', str(b['timeout_s']) + 's', '-context-bytes', str(b['context_bytes']), '-prompt', prompt]
    if config['model_control'][arm]['method'] == 'cli_flag':
        command.extend(['-model', config['model']])
    write_json(run_dir / 'command.json', command)
    started = datetime.now(timezone.utc).isoformat()
    start = time.monotonic()
    env = dict(os.environ, OPENAI_API_KEY=key, OPENAI_BASE_URL='https://api.openai.com/v1/')
    # 工作目录为无 .env 的实验根，CLI 的 dotenv 不会覆盖已提供的测试凭据。
    code, out, err = child(command, root, env, b['timeout_s'] + 10)
    elapsed = int((time.monotonic() - start) * 1000)
    out, err = clean(out, key), clean(err, key)
    redact_runtime(work, key)
    (run_dir / 'stdout.txt').write_text(out)
    (run_dir / 'stderr.txt').write_text(err)
    after = snapshot(work)
    write_json(run_dir / 'after.json', after)
    changes, forbidden = scope_result(before, after, case['write_policy'])
    result = {'schema_version': 2, 'experiment_id': root.name, 'suite_digest': config['suite_digest'],
              'case_id': case['id'], 'repeat': repeat, 'arm': arm, 'session_id': session,
              'binary_sha256': config['binaries'][arm]['sha256'], 'started_at': started, 'task_id': None, 'run_id': None,
              'exit_code': code, 'engine_status': None, 'stop_code': None, 'elapsed_ms': elapsed,
              'artifact_result': 'not_run', 'scope_result': 'fail' if forbidden else 'pass',
              'engine_result': 'error', 'environment_result': 'pass', 'checker_result': 'not_run',
              'changes': changes, 'forbidden_changes': forbidden, 'model_calls': None, 'tool_calls': None,
              'input_tokens': None, 'output_tokens': None, 'cost': None, 'pending_action_count': None,
              'final_text_present': False, 'final_text_path': 'answer.txt',
              'evidence_paths': ['before.json', 'after.json', 'stdout.txt', 'stderr.txt', 'reads.json', 'verification.txt', 'workspace/.claw']}
    events = []
    answer = run_dir / 'answer.txt'
    answer.write_text('')
    try:
        task_id, state, events = read_task(work, session)
        last = state['last_result']
        result.update(task_id=task_id, run_id=last.get('run_id'), engine_status=last['status'], stop_code=last.get('stop_code'),
                      pending_action_count=len(state.get('pending') or {}))
        answer.write_text(clean(last.get('final_text', ''), key))
        result['final_text_present'] = bool(answer.read_text().strip())
        usage = last.get('usage') or state.get('usage', {})
        result['model_calls'], result['tool_calls'] = usage.get('model_calls'), usage.get('tool_calls')
        result['engine_result'] = 'pass' if (last['status'] == case['expected_status'] and code == case['expected_exit_code'] and
            (result['final_text_present'] or not case['require_final_text']) and result['pending_action_count'] == 0) else 'fail'
    except (ValueError, OSError, KeyError, TypeError) as exc:
        result['protocol_error'] = str(exc)
    if result['engine_status'] == 'FAILED' and re.search(r'invalid_api_key|401 Unauthorized|429 Too Many Requests|rate.limit|connect:|dial tcp|tls handshake|certificate|proxyconnect|connection refused|connection reset|503 Service Unavailable', out + err, re.I):
        result['environment_result'] = 'error'
    token = re.search(r'会话费用 (.*?) · Token 输入 (\d+) / 输出 (\d+)(.*)', out)
    if token and '未知' not in token[4]:
        result['input_tokens'], result['output_tokens'] = int(token[2]), int(token[3])
        result['cost'] = None if token[1] == '未知' else token[1]
    evidence = read_evidence(events)
    write_json(run_dir / 'reads.json', evidence)
    try:
        check_code, check_output, infrastructure = checker(root, suite, case, work, answer, run_dir / 'reads.json')
        (run_dir / 'verification.txt').write_text(clean(check_output, key))
        result['checker_result'] = 'error' if infrastructure else 'pass'
        result['artifact_result'] = 'error' if infrastructure else ('pass' if check_code == 0 else 'fail')
        required = set(case.get('required_reads', []))
        if not infrastructure and not required <= {r['path'] for r in evidence['reads']}:
            result['artifact_result'] = 'fail'
            result['missing_reads'] = sorted(required - {r['path'] for r in evidence['reads']})
    except (OSError, ValueError) as exc:
        result['checker_result'] = result['artifact_result'] = 'error'
        (run_dir / 'verification.txt').write_text(clean(str(exc), key))
    finish_dimensions(result, case)
    write_json(run_dir / 'result.json', result)
    summarize(root)
    print(f'{identifier}: {"PASS" if result["combined_pass"] else ",".join(result["labels"])} · {result["engine_status"]}/{code} · 模型 {result["model_calls"]} / 工具 {result["tool_calls"]}', flush=True)
    return result


def stage_gate(results, names):
    for name in names:
        for arm in ('baseline', 'candidate'):
            group = [r for r in results if r['arm'] == arm and r['case_id'] == name]
            if len(group) != 3 or {r['repeat'] for r in group} != {1, 2, 3}:
                return False
    candidate = [r for r in results if r['arm'] == 'candidate' and r['case_id'] in names]
    return all(sum(r['combined_pass'] for r in candidate if r['case_id'] == name) >= 2 for name in names) and all(
        not r['forbidden_changes'] and not (r['engine_status'] in ('ANSWERED', 'VERIFIED') and r['artifact_result'] == 'fail') for r in candidate)


def summarize(root):
    root = Path(root)
    records = [read_json(p) for p in sorted((root / 'runs').glob('*/result.json'))]
    # 支持旧 v1 记录的多标签重解释，保留原 category 与综合分数。
    if records and records[0].get('schema_version') != 2:
        counts = Counter(label for r in records for label in ({r['category']} - {'passed'} | ({'budget_exhausted'} if r.get('status') == 'BUDGET_EXHAUSTED' else set())))
        report = {'schema_version': 2, 'legacy': True, 'original_passed': sum(r.get('category') == 'passed' for r in records),
                  'runs': len(records), 'labels': dict(counts), 'original_categories': dict(Counter(r['category'] for r in records))}
        return report
    summary = {'schema_version': 2, 'runs': len(records), 'arms': {}, 'stage1_gate': stage_gate(records, FIRST),
               'final_gate': len(records) == 30 and stage_gate(records, FIRST + REST) and sum(r['combined_pass'] for r in records if r['arm'] == 'candidate') >= 12}
    for arm in ('baseline', 'candidate'):
        rows = [r for r in records if r['arm'] == arm]
        summary['arms'][arm] = {'runs': len(rows), 'combined_passed': sum(r['combined_pass'] for r in rows),
            'artifact_passed': sum(r['artifact_result'] == 'pass' for r in rows),
            'normal_completed': sum(r['engine_result'] == 'pass' for r in rows),
            'labels': dict(Counter(label for r in rows for label in r['labels'])),
            'model_calls': sum(r['model_calls'] for r in rows) if all(r['model_calls'] is not None for r in rows) else None,
            'tool_calls': sum(r['tool_calls'] for r in rows) if all(r['tool_calls'] is not None for r in rows) else None,
            'input_tokens': sum(r['input_tokens'] or 0 for r in rows) if all(r['input_tokens'] is not None for r in rows) else None,
            'output_tokens': sum(r['output_tokens'] or 0 for r in rows) if all(r['output_tokens'] is not None for r in rows) else None,
            'cost': None, 'cases': {name: {'passed': sum(r['combined_pass'] for r in rows if r['case_id'] == name),
            'runs': sum(r['case_id'] == name for r in rows)} for name in FIRST + REST}}
    write_json(root / 'summary.json', summary)
    with (root / 'results.jsonl').open('w') as f:
        for row in records:
            f.write(json.dumps(row, ensure_ascii=False) + '\n')
    lines = ['# 真实任务 v2 双组评估', '', '| 用例 | 旧版通过 | 新版通过 |', '|---|---:|---:|']
    for name in FIRST + REST:
        pair = [summary['arms'][a]['cases'][name] for a in ('baseline', 'candidate')]
        lines.append(f'| {name} | {pair[0]["passed"]}/{pair[0]["runs"]} | {pair[1]["passed"]}/{pair[1]["runs"]} |')
    lines.extend(['', f'第一阶段门槛：{summary["stage1_gate"]}；完整门槛：{summary["final_gate"]}。', '', '产物、文件范围、引擎终态和基础设施独立评分。费用未知不记为零。', ''])
    for r in records:
        if not r['combined_pass']:
            lines.append(f'- {r["case_id"]}/{r["repeat"]}/{r["arm"]}：{", ".join(r["labels"])}；产物 {r["artifact_result"]}；终态 {r["engine_status"]}/{r["exit_code"]}')
    (root / 'report.md').write_text('\n'.join(lines) + '\n')
    return summary


def selfcheck(suite, output=None):
    suite, config = load_suite(suite)
    root = Path(output).resolve() if output else Path(tempfile.mkdtemp(prefix='claw-eval-selfcheck-')).resolve()
    root.mkdir(parents=True, exist_ok=True)
    preflight(root, suite)
    passed = []
    def expect(case, work, answer, evidence, succeeds, label):
        code, out, infra = checker(root, suite, case, work, answer, evidence)
        (root / (label + '.log')).write_text(out)
        if infra or (code == 0) != succeeds:
            raise AssertionError(label + ': ' + out[-2500:])
        passed.append(label)
    evidence = root / 'reads.json'
    answer = root / 'answer.txt'
    by_id = {c['id']: c for c in config['cases']}
    r01 = suite / 'cases/R01/workspace'
    reads = {'reads': [{'path': p.relative_to(r01).as_posix(), 'version': digest(p), 'start': 0, 'end': p.stat().st_size}
                       for p in r01.rglob('*') if p.is_file()]}
    write_json(evidence, reads)
    good = read_json(suite / 'selfcheck/R01-answer.json')
    write_json(answer, good)
    expect(by_id['R01'], r01, answer, evidence, True, 'R01-positive')
    for label, change in [
        ('missing-edge', lambda d: d['call_path'].pop(6)),
        ('swapped-edge', lambda d: d['call_path'].reverse()),
        ('wrong-line', lambda d: d['call_path'][0]['citation'].update(line=89))]:
        bad = json.loads(json.dumps(good)); change(bad); write_json(answer, bad)
        expect(by_id['R01'], r01, answer, evidence, False, 'R01-' + label)
    write_json(answer, good)
    write_json(evidence, {'reads': [dict(r, end=min(r['end'], 100)) for r in reads['reads']]})
    expect(by_id['R01'], r01, answer, evidence, False, 'R01-unread-citation')
    write_json(evidence, reads)
    shutil.copyfile(suite / 'selfcheck/R02-answer.json', answer)
    expect(by_id['R02'], suite / 'cases/R02/workspace', answer, evidence, True, 'R02-positive')
    bad = read_json(answer); bad['resume']['replayed'] = True; write_json(answer, bad)
    expect(by_id['R02'], suite / 'cases/R02/workspace', answer, evidence, False, 'R02-wrong-replay')
    for name in ('W01', 'W02', 'W03'):
        work = root / name
        shutil.copytree(suite / by_id[name]['fixture'], work)
        expect(by_id[name], work, answer, evidence, False, name + '-baseline-negative')
        if name == 'W01':
            shutil.copyfile(suite / 'cases/W01/expected.json', work / 'agentops.config.json')
        elif name == 'W02':
            shutil.copyfile(suite / 'selfcheck/W02-read_file.go', work / 'internal/tools/read_file.go')
            expect(by_id[name], work, answer, evidence, True, name + '-without-optional-test')
            shutil.copyfile(suite / 'selfcheck/W02-test.go', work / 'internal/tools/definition_test.go')
        else:
            shutil.copyfile(suite / 'selfcheck/W03-test.go', work / 'internal/tools/version_conflict_test.go')
        expect(by_id[name], work, answer, evidence, True, name + '-positive')
    # 准确区分弱化测试、缺陷断言失败和编译失败。
    work = root / 'W02'
    test = work / 'internal/tools/definition_test.go'
    test.write_text('package tools\nimport "testing"\nfunc TestEmpty(t *testing.T) {}\n')
    expect(by_id['W02'], work, answer, evidence, False, 'W02-weak-test')
    test.write_text('package tools\nimport "testing"\nfunc TestSkip(t *testing.T) {t.Skip("skip")}\n')
    expect(by_id['W02'], work, answer, evidence, False, 'W02-skipped-test')
    shutil.copyfile(suite / 'selfcheck/W02-test.go', test)
    correct = (work / 'internal/tools/read_file.go').read_text()
    for label, source in [('null', (suite / 'cases/W02/workspace/internal/tools/read_file.go').read_text()),
                          ('missing-required', correct.replace('inputSchema["required"] = required', 'inputSchema["required"] = []string{}')),
                          ('reordered-required', correct.replace('inputSchema["required"] = required', 'inputSchema["required"] = append([]string(nil), required...)\n        if len(required) > 1 { inputSchema["required"] = []string{required[1], required[0]} }'))]:
        (work / 'internal/tools/read_file.go').write_text(source)
        expect(by_id['W02'], work, answer, evidence, False, 'W02-' + label)
    # 无 API 的协议、范围、多维统计、事件回退与汇总回归。
    import importlib.util
    spec = importlib.util.spec_from_file_location('real_eval_tests', Path(__file__).with_name('test_run.py'))
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    import unittest
    tests = unittest.defaultTestLoader.loadTestsFromModule(module)
    outcome = unittest.TextTestRunner(verbosity=1).run(tests)
    if not outcome.wasSuccessful():
        raise AssertionError('结果协议单元测试失败')
    write_json(root / 'selfcheck.json', {'passed': passed, 'unit_tests': outcome.testsRun, 'suite_digest': object_digest(tree_hashes(suite))})
    print(f'离线自检通过：{len(passed)} 个 oracle 场景，{outcome.testsRun} 个协议测试；证据 {root}', flush=True)
    return root


def model_control(binary, model):
    """帮助信息探测不访问 provider；旧版仅接受已核实的固定二进制。"""
    probe = subprocess.run([str(binary), '-help'], capture_output=True, text=True, timeout=10)
    if re.search(r'(?m)^\s*-model(?:\s|$)', probe.stdout + probe.stderr):
        return {'method': 'cli_flag', 'model': model}
    known = LEGACY_MODELS.get(digest(binary))
    if known == model:
        return {'method': 'known_legacy_sha256', 'model': model}
    raise ValueError('二进制无法显式指定模型且不在已核实的旧版摘要映射中，拒绝真实调用')


def prepare(args):
    suite, config = load_suite(args.suite)
    hashes = tree_hashes(suite)
    binary_paths = {'baseline': Path(args.baseline_bin).resolve(), 'candidate': Path(args.candidate_bin).resolve()}
    binaries = {arm: {'sha256': digest(path)} for arm, path in binary_paths.items()}
    controls = {arm: model_control(path, config['model']) for arm, path in binary_paths.items()}
    runner_files = {name: digest(Path(__file__).with_name(name)) for name in ('run.py', 'test_run.py')}
    if args.resume:
        root = Path(args.resume).resolve()
        lock = read_json(root / 'manifest.json')
        if lock.get('model_control') != controls:
            raise ValueError('模型指定能力发生变化')
        if lock.get('runner_files') != runner_files or tree_hashes(root / 'runner') != runner_files:
            raise ValueError('评估 runner 已变化，拒绝继续原实验')
        if lock['suite_files'] != hashes or lock['binaries'] != binaries or tree_hashes(root / 'suite') != hashes:
            raise ValueError('冻结输入已变化，拒绝继续原实验')
        if any(digest(root / 'bin' / arm) != binaries[arm]['sha256'] for arm in binaries):
            raise ValueError('冻结二进制已变化')
    else:
        root = Path(args.output).resolve() if args.output else REPO / '.claw/evals' / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:8])
        if root.exists():
            raise ValueError('新实验输出目录已存在，请用 --resume 或换目录')
        (root / 'bin').mkdir(parents=True)
        (root / 'runner').mkdir()
        for name in runner_files:
            shutil.copyfile(Path(__file__).with_name(name), root / 'runner' / name)
        shutil.copytree(suite, root / 'suite', ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
        for arm, path in binary_paths.items():
            shutil.copy2(path, root / 'bin' / arm)
        lock = {'schema_version': 2, 'suite_files': hashes, 'suite_digest': object_digest(hashes),
                'binaries': binaries, 'model_control': controls, 'runner_files': runner_files, 'budget': config['budget'], 'model': config['model'],
                'endpoint': 'https://api.openai.com/v1/', 'python': sys.version, 'platform': sys.platform,
                'go': subprocess.check_output(['go', 'version'], text=True).strip(),
                'created_at': datetime.now(timezone.utc).isoformat(), 'candidate_source': None}
        if args.candidate_source:
            source = Path(args.candidate_source).resolve()
            shutil.copyfile(source, root / 'candidate-source.tar.gz')
            lock['candidate_source'] = {'file': 'candidate-source.tar.gz', 'sha256': digest(source)}
        write_json(root / 'manifest.json', lock)
    return root, root / 'suite', config, lock


def run_experiment(args):
    key = os.environ.get('OPENAI_API_KEY', '')
    if not key:
        raise ValueError('请通过进程环境提供 OPENAI_API_KEY，不自动读取项目 .env')
    root, suite, config, lock = prepare(args)
    # 同一冻结 suite 的自检必须先通过，故障不得触发真实请求。
    check_dir = root / 'preflight'
    if not (check_dir / 'selfcheck.json').exists():
        if check_dir.exists():
            raise ValueError('先前自检未完成，请检查证据并使用新实验目录')
        selfcheck(suite, check_dir)
    else:
        if read_json(check_dir / 'selfcheck.json')['suite_digest'] != lock['suite_digest']:
            raise ValueError('自检与冻结 suite 不匹配')
        preflight(root, suite)
    print('实验目录 ' + str(root), flush=True)
    by_id = {c['id']: c for c in config['cases']}
    rows = []
    for group in (FIRST, REST):
        if group == REST and not stage_gate(rows, FIRST):
            print('第一阶段未达到门槛，停止扩展样本。', flush=True)
            break
        for repeat in range(1, 4):
            for name in group:
                for arm in ('baseline', 'candidate') if repeat % 2 else ('candidate', 'baseline'):
                    row = run_one(root, suite, lock, by_id[name], arm, repeat, key)
                    rows.append(row)
                    if row['environment_result'] == 'error' or row['checker_result'] == 'error' or row['engine_result'] == 'error':
                        print('基础设施或结果协议异常，停止本批，不自动重试。', flush=True)
                        return 2
        if args.stage == 'first':
            break
    summary = summarize(root)
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0 if summary['final_gate'] or (args.stage == 'first' and summary['stage1_gate']) else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    check = sub.add_parser('selfcheck'); check.add_argument('--suite', default=str(DEFAULT_SUITE)); check.add_argument('--output')
    run = sub.add_parser('run'); run.add_argument('--suite', default=str(DEFAULT_SUITE)); run.add_argument('--baseline-bin', required=True)
    run.add_argument('--candidate-bin', required=True); run.add_argument('--candidate-source')
    run.add_argument('--output'); run.add_argument('--resume'); run.add_argument('--stage', choices=['first', 'all'], default='all')
    summary = sub.add_parser('summarize'); summary.add_argument('directory')
    args = parser.parse_args()
    try:
        if args.command == 'selfcheck':
            selfcheck(args.suite, args.output); return 0
        if args.command == 'summarize':
            print(json.dumps(summarize(Path(args.directory).resolve()), ensure_ascii=False, indent=2)); return 0
        if args.resume and args.output:
            raise ValueError('--resume 与 --output 互斥')
        return run_experiment(args)
    except (AssertionError, ValueError, RuntimeError, OSError, KeyError, subprocess.SubprocessError) as exc:
        print('评估停止：' + clean(str(exc), os.environ.get('OPENAI_API_KEY', '')), file=sys.stderr)
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
