"""有限脱敏反例，仅标注完整命题；不执行来源脚本或任意表达式。"""
from fractions import Fraction
import hashlib
import json
from pathlib import Path


def load_cases(path):
    cases = json.loads(Path(path).read_text())
    if cases.get('schema_version') != 1 or cases.get('kind') != 'finite_quality_first_counterexamples':
        raise ValueError('未知反例版本')
    annotation = cases.get('annotation', {})
    if annotation.get('provider_calls') != 0 or annotation.get('scripts_executed') is not False or annotation.get('independent_human_review') is not False:
        raise ValueError('反例不得冒充付费模型、脚本执行或独立人工')
    sources = {}
    for source in cases['sources']:
        if source['id'] in sources or hashlib.sha256(source['text'].encode()).hexdigest() != source['sha256']:
            raise ValueError('反例来源身份或摘要不符')
        sources[source['id']] = source
    names, claims = set(), set()
    for case in cases['cases']:
        if case['id'] in names or case['claim'] in claims or not case['source_ids'] or not set(case['source_ids']) <= set(sources) or case['expected'] not in ('pass', 'fail'):
            raise ValueError('反例命题或来源不唯一')
        names.add(case['id']); claims.add(case['claim'])
    return cases


def evaluate_claim(claim, cases):
    return next((case['expected'] for case in cases['cases'] if claim.strip() == case['claim']), 'not_run')


def selfcheck(cases):
    # 固定、明确的代数前提；没有eval/exec或动态读取课程代码。
    if (Fraction(10) - 1) / 9 != 1 or (Fraction(10) - 1) / 9 * 25 != 25 or Fraction(5, 10) * 25 != Fraction(25, 2):
        raise ValueError('固定有理数前提不成立')
    expectations = {'missing_factor': 'fail', 'correct_fix': 'pass', 'default': 'pass', 'wrong_default': 'fail',
                    'ast_prose_disagree': 'fail', 'null_is_present': 'pass', 'nested_name': 'fail', 'pointer_escape': 'pass',
                    'json_scope': 'fail', 'version_boundary': 'fail', 'truthy_not_legal': 'fail', 'command_omission': 'fail'}
    if {c['id']: c['expected'] for c in cases['cases']} != expectations:
        raise ValueError('冻结标签变化，不能静默提高成绩')
    return {'checker_result': 'pass', 'cases': len(expectations), 'provider_calls': 0, 'unknown_result': 'not_run'}
