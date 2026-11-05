"""独立 wire oracle 与候选测试分别执行，防止新增测试影响独立判定。"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

CASE = Path(__file__).resolve().parent
TEST = Path('internal/tools/definition_test.go')


def copy_base(workspace, stage):
    for original in (CASE / 'workspace').rglob('*'):
        if not original.is_file():
            continue
        relative = original.relative_to(CASE / 'workspace')
        candidate = workspace / relative
        if candidate.is_symlink() or not candidate.is_file():
            raise ValueError(f'缺少普通文件 {relative}')
        target = stage / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(candidate, target)


def run(stage, args):
    result = subprocess.run(['go', 'test', '-json', '-mod=readonly', '-count=1', '-timeout=30s', *args], cwd=stage, capture_output=True, text=True, timeout=75)
    events = []
    for line in result.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            pass
    output = result.stdout + result.stderr
    print(output)
    if re.search(r'module lookup disabled|failed to initialize build cache|sandbox-exec:', output, re.I):
        raise OSError('评分依赖或环境不可用')
    return result.returncode, events


def verify(workspace):
    with tempfile.TemporaryDirectory(prefix='claw-w02-') as directory:
        root = Path(directory)
        wire = root / 'wire'
        copy_base(workspace, wire)
        shutil.copyfile(CASE / 'schema_wire_test.go', wire / 'internal/provider/schema_wire_test.go')
        code, events = run(wire, ['-run', '^TestEvaluationToolSchemaOnResponsesWire$', './internal/provider'])
        if code or not any(e.get('Test') == 'TestEvaluationToolSchemaOnResponsesWire' and e['Action'] == 'pass' for e in events):
            raise ValueError('独立 Responses wire oracle 未通过')
        candidate = workspace / TEST
        if not candidate.exists():
            print('未新增可选测试；独立语义验收通过')
            return
        if candidate.is_symlink() or not candidate.is_file():
            raise ValueError('候选测试必须为普通文件')
        stage = root / 'candidate'
        copy_base(workspace, stage)
        shutil.copyfile(candidate, stage / TEST)
        names = re.findall(r'^func\s+(Test\w+)\s*\(', candidate.read_text(), re.M)
        if not names:
            raise ValueError('候选文件没有测试函数')
        pattern = '^(' + '|'.join(map(re.escape, names)) + ')$'
        code, events = run(stage, ['-run', pattern, './internal/tools'])
        passed = {e.get('Test') for e in events if e.get('Action') == 'pass'}
        ran = {e.get('Test') for e in events if e.get('Action') == 'run'}
        if code or not set(names) <= passed or not set(names) <= ran:
            raise ValueError('候选测试必须实际运行且通过，不得跳过')
        shutil.copyfile(CASE / 'workspace/internal/tools/read_file.go', stage / 'internal/tools/read_file.go')
        code, events = run(stage, ['-run', pattern, './internal/tools'])
        failed = {e.get('Test') for e in events if e.get('Action') == 'fail'}
        ran = {e.get('Test') for e in events if e.get('Action') == 'run'}
        if code == 0 or not (set(names) & failed & ran):
            raise ValueError('历史缺陷必须由实际测试失败检出，编译失败不算')
        print('独立语义验收及候选测试缺陷检出通过')


if __name__ == '__main__':
    try:
        verify(Path(sys.argv[1]).resolve())
    except ValueError as exc:
        sys.exit(str(exc))
    except (OSError, subprocess.TimeoutExpired) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(125)
