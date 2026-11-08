"""有限脱敏反例，仅标注完整命题；不执行来源脚本或任意表达式。"""
from fractions import Fraction
import hashlib
import json
from pathlib import Path


def load_cases(path):
    cases = json.loads(Path(path).read_text())
    if cases.get('schema_version') != 1 or cases.get('kind') != 'finite_source_reuse_counterexamples':
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


def strict_object(raw):
    """仅解析有限夹具JSON，不导入或执行课程代码。"""
    def pairs(items):
        value = {}
        for key, member in items:
            if key in value: raise ValueError('JSON重复键')
            value[key] = member
        return value
    value = json.loads(raw, object_pairs_hook=pairs,
                       parse_constant=lambda value: (_ for _ in ()).throw(ValueError('非标准JSON')))
    if not isinstance(value, dict): raise ValueError('主体必须是完整明确JSON对象')
    return value


def pointer_exists(value, pointer):
    if not isinstance(pointer, str) or not pointer.startswith('/'):
        raise ValueError('本夹具要求非空JSONPointer')
    for part in pointer[1:].split('/'):
        import re
        if re.search(r'~(?![01])', part): raise ValueError('Pointer转义无效')
        part = part.replace('~1', '/').replace('~0', '~')
        if isinstance(value, dict):
            if part not in value: return False
            value = value[part]
        elif isinstance(value, list):
            if not part.isascii() or not part.isdigit() or len(part) > 1 and part.startswith('0'):
                raise ValueError('Pointer数组位置无效')
            index = int(part)
            if index >= len(value): return False
            value = value[index]
        else: return False
    return True


def timestamp_selfcheck(path, workspace=None):
    data = json.loads(Path(path).read_text())
    if data.get('schema_version') != 1 or data.get('kind') != 'finite_source_reuse_timestamp_cases':
        raise ValueError('未知时间字段反例契约')
    annotation = data.get('annotation', {})
    if annotation.get('provider_calls') != 0 or annotation.get('scripts_executed') is not False or annotation.get('independent_human_review') is not False:
        raise ValueError('时间字段反例不能冒充执行验收')
    subjects = {}
    for subject in data['subjects']:
        if subject['id'] in subjects or hashlib.sha256(subject['raw_json'].encode()).hexdigest() != subject['sha256']:
            raise ValueError('时间字段主体身份或摘要错误')
        if workspace is not None:
            root = Path(workspace).resolve(strict=True)
            source = (root / subject['path']).resolve(strict=True)
            if not source.is_relative_to(root) or source.is_symlink() or hashlib.sha256(source.read_bytes()).hexdigest() != subject['source_sha256']:
                raise ValueError('时间主体不匹配冻结来源版本')
            lines = source.read_text().splitlines()
            if '\n'.join(lines[subject['start_line']-1:subject['end_line']]) != subject['raw_json']:
                raise ValueError('时间主体原文与声明范围不符')
        subjects[subject['id']] = strict_object(subject['raw_json'])
    expected = {'good_collected': True, 'good_updated_missing': False, 'bad_collected_missing': False}
    if {c['id']: c['expected_exists'] for c in data['cases']} != expected:
        raise ValueError('冻结时间字段标签变化')
    for case in data['cases']:
        if pointer_exists(subjects[case['subject_id']], case['pointer']) is not case['expected_exists']:
            raise ValueError('主体字段实际存在性与标注不符')
    # 同名键、缺失、false/null/0/空串不能混淆；对象选择仍需独立语义核对。
    for member in ('null', 'false', '0', '""'):
        if not pointer_exists(strict_object('{"collected_at":'+member+'}'), '/collected_at'):
            raise ValueError('字段存在性被误当成truthiness')
    # 自适应证明：错误待核对结论由实际检查结果修正，而非直接返回预制正确答案。
    draft_missing = True
    actual_exists = pointer_exists(subjects['quality_sample_good'], '/collected_at')
    initial_result = 'fail' if draft_missing == actual_exists else 'pass'
    corrected_missing = not actual_exists if initial_result == 'fail' else draft_missing
    final_result = 'pass' if corrected_missing is not actual_exists else 'fail'
    if initial_result != 'fail' or final_result != 'pass': raise ValueError('自适应核对未根据真实结果修订')
    return {'checker_result': 'pass', 'cases': len(expected), 'adaptive_initial': initial_result,
            'adaptive_final': final_result, 'provider_calls': 0, 'semantic_result': 'not_run'}
