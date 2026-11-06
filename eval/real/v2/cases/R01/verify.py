"""按冻结调用边和真实读取区间核对引用，不使用模型自评。"""
import hashlib
import json
from pathlib import Path
import re
import sys


def verify(work, answer, evidence):
    case = Path(__file__).resolve().parent
    facts = json.loads((case / 'facts.json').read_text())
    raw = answer.read_text().strip()
    fence = re.fullmatch(r'```(?:json)?\s*(.*?)\s*```', raw, re.S | re.I)
    d = json.loads(fence[1] if fence else raw)
    assert d['default_mode'] == 'readonly', '默认模式错误'
    assert set(d['write_modes']) == {'workspace', 'trusted'}, '写入模式错误'
    assert d['bash_modes'] == ['trusted'], 'Bash 模式错误'
    assert d['checks'] == ['registered', 'scope', 'parameters', 'middleware', 'handler'], '准入顺序错误'
    assert d['call_path'] == facts['edges'], '调用边、顺序或调用表达式引用不符'
    assert d['registration'] == facts['registration'], '缺少真实注册证据'
    reads = json.loads(evidence.read_text())['reads']
    anchors = {'cmd/claw/main.go': (26, 33), 'internal/tools/scope.go': (49, 69), 'internal/tools/registry.go': (158, 202)}
    covered = set()
    refs = d['citations'] + [e['citation'] for e in d['call_path']] + [d['registration']]
    for ref in refs:
        path, line = ref['path'], ref['line']
        assert path in facts['hashes'] and type(line) is int, '引用字段无效'
        data = (work / path).read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        assert digest == facts['hashes'][path], '冻结源码发生变化'
        lines = data.splitlines(keepends=True)
        assert 1 <= line <= len(lines), '引用行号越界'
        start, end = sum(map(len, lines[:line - 1])), sum(map(len, lines[:line]))
        assert any(r['path'] == path and r['version'] == digest and r['start'] <= start and r['end'] >= end for r in reads), f'引用未被完整读取 {path}:{line}'
        if path in anchors and anchors[path][0] <= line <= anchors[path][1]:
            covered.add(path)
    assert covered == set(anchors), '权限依据不完整'


if __name__ == '__main__':
    try:
        verify(*map(Path, sys.argv[1:4]))
    except (AssertionError, ValueError, KeyError, TypeError, OSError) as exc:
        sys.exit('R01 未通过：' + str(exc))
    print('权限、调用边、注册证据及引用读取区间通过')
