#!/usr/bin/env python3
"""独立读取能力 v2 独立双样本：仅 run-one 启动真实CLI；质量优先，不自动重试。"""
import argparse
from contextlib import contextmanager
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import random
import re
import shutil
import signal
import subprocess
import sys
import uuid

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
DEFAULT_WORKSPACE = HERE.parent / 'fixture' if (HERE / 'common.py').is_file() else REPO / 'eval/fixtures/read_features_v2/workspace'
COMMON = HERE / 'common.py' if (HERE / 'common.py').is_file() else REPO / 'scripts/write_efficiency_eval/run.py'
_spec = importlib.util.spec_from_file_location('read_features_common', COMMON)
r = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(r)
EvalError = r.EvalError
PROFILE = 'read_features_v2'
ORDER = ['baseline-ordinary-1', 'candidate-ordinary-1']
OUTPUT = r.OUTPUT
SCORES = tuple(r.SCORES)
SPECIAL = ('proposed_repairs', 'subject_identity', 'important_omissions')
SETTINGS = dict(r.SETTINGS)
ORACLE = HERE / 'oracle.json' if (HERE / 'oracle.json').is_file() else REPO / 'eval/fixtures/read_features_v2/semantic_oracle.json'
RUBRIC = HERE / 'rubric.json' if (HERE / 'rubric.json').is_file() else REPO / 'eval/fixtures/read_features_v2/rubric.json'
CASES = HERE / 'cases.json' if (HERE / 'cases.json').is_file() else REPO / 'eval/fixtures/read_features_v2/cases.json'
FIXTURE_MANIFEST = HERE / 'fixture_manifest.json' if (HERE / 'fixture_manifest.json').is_file() else REPO / 'eval/fixtures/read_features_v2/manifest.json'
ANCHORS = HERE / 'anchors.json' if (HERE / 'anchors.json').is_file() else REPO / 'eval/fixtures/read_features_v2/anchors.json'
FEATURES = {'version': 1, 'source_views': True, 'local_checks': True, 'claim_focus': True}
TIMESTAMP_CASES = HERE / 'timestamp_cases.json' if (HERE / 'timestamp_cases.json').is_file() else REPO / 'eval/fixtures/read_features_v2/timestamp_cases.json'
GOAL = ('完整阅读当前工作区各资料包中的全部Markdown，仅在当前目录新建优化汇总.md，不修改原始资料。'
        '精简概览资料并指出明显矛盾，核对被引用的数值、公式、默认值及版本条件。'
        '重要结论须就地引用完整相对文件路径及行号；跨文档判断同时给出两端依据。'
        '区分原文转述、静态推导、建议及实际运行；未执行的内容保留待实测边界。'
        '一项问题只展开一次，不重复粘贴长篇原文或示例代码。')
LIMITATIONS = ('同任务同模型一对普通任务的小样本；33f仅历史症状，不替代同批baseline；'
               '内部局部核对、来源复用和引用位置检查不替代外部语义审查；质量优先，'
               '同二进制能力off/on，12/40和Token下降30%仅次级观察目标；未知费用null；不执行课程代码。')
r.ORDER, r.PROFILE_VERSION = list(ORDER), 6
r.build_goal = lambda files: GOAL
r.strict_sample = lambda name: False
r.harness_inputs = lambda: harness_inputs()


def harness_inputs():
    return {'run.py': Path(__file__), 'common.py': COMMON, 'helpers.py': r.HELPERS,
            'checker.py': HERE / 'checker.py', 'test_run.py': HERE / 'test_run.py', 'test_native_cli.py': HERE / 'test_native_cli.py', 'README.md': HERE / 'README.md', 'finite_cases.py': HERE / 'finite_cases.py', 'timestamp_cases.json': TIMESTAMP_CASES, 'fixture_manifest.json': FIXTURE_MANIFEST, 'oracle.json': ORACLE, 'rubric.json': RUBRIC, 'anchors.json': ANCHORS, 'cases.json': CASES,
            'quality_oracle.py': HERE / 'quality_oracle.py' if (HERE / 'quality_oracle.py').is_file() else REPO / 'scripts/coverage_efficiency_eval/quality_oracle.py',
            'semantic_oracle.py': HERE / 'semantic_oracle.py' if (HERE / 'semantic_oracle.py').is_file() else REPO / 'scripts/write_efficiency_eval/semantic_oracle.py'}


def init_experiment(output, baseline_bin, candidate_bin, source_workspace=None, project_dotenv=None,
                    baseline_source_manifest=None, candidate_source_manifest=None):
    if not baseline_source_manifest or not candidate_source_manifest:
        raise EvalError('必须提供两组冻结源码清单；无源码归档不得启动本profile')
    if r.digest(baseline_bin) != r.digest(candidate_bin):
        raise EvalError('两组必须使用同一冻结二进制')
    if r.digest(baseline_source_manifest) != r.digest(candidate_source_manifest):
        raise EvalError('两组必须使用同一冻结源码清单')
    root = Path(output).absolute()
    manifest = r.init_experiment(root, baseline_bin, candidate_bin, source_workspace or DEFAULT_WORKSPACE)
    if manifest['files'] != r.read_json(FIXTURE_MANIFEST).get('files'):
        raise EvalError('本profile必须使用已核对的完整冻结资料版本')
    manifest.update(profile=PROFILE, max_samples=2, max_model_admissions=120, limitations=LIMITATIONS,
                    credentials={'source': 'project_dotenv_runtime_only', 'snapshot': 'bind_first_run', 'persist_values': False},
                    project_dotenv=str(Path(project_dotenv or REPO / '.env').absolute()), source_manifests={}, policies={'baseline': 2, 'candidate': 4}, task_state_version=1, read_memory_storage_version=1, read_features={'baseline': None, 'candidate': FEATURES})
    for arm, source in [('baseline', baseline_source_manifest), ('candidate', candidate_source_manifest)]:
        if source:
            # manifest只含源码路径/摘要/长度，不复制.env或任务正文；源码副本由实施检查点保留。
            data = r.read_json(source)
            if not isinstance(data, list) or not data:
                raise EvalError('源码清单必须是非空路径/摘要数组')
            for entry in data:
                parts = Path(entry.get('path', '')).parts
                if not parts or Path(entry['path']).is_absolute() or parts[0] == 'workspace' or any(p in {'.claw', '.git', '..'} or p.startswith('.env') for p in parts):
                    raise EvalError('源码清单包含凭据、任务数据或越界路径')
                if not re.fullmatch('[0-9a-f]{64}', entry.get('sha256', '')):
                    raise EvalError('源码清单摘要无效')
            target = root / 'source-manifests' / (arm + '.json'); r.write_json(target, data)
            manifest['source_manifests'][arm] = {'path': str(target.relative_to(root)), 'sha256': r.digest(target)}
    if bool(baseline_source_manifest) != bool(candidate_source_manifest):
        raise EvalError('必须同时提供两组源码清单')
    r.write_json(root / 'manifest.json', manifest)
    r.write_json(root / 'manifest-seal.json', {'sha256': r.digest(root / 'manifest.json')})
    return manifest


def checked_manifest(experiment, *, require_current=True):
    root, manifest = r.checked_manifest(experiment, require_current=require_current)
    if r.read_json(root / 'manifest-seal.json') != {'sha256': r.digest(root / 'manifest.json')}:
        raise EvalError('冻结manifest摘要变化')
    if manifest.get('profile') != PROFILE or manifest.get('max_samples') != 2 or manifest.get('max_model_admissions') != 120:
        raise EvalError('不是独立普通双样本契约')
    path = Path(manifest.get('project_dotenv', ''))
    if not path.is_absolute() or path.name != '.env':
        raise EvalError('凭据必须来自项目.env，且不得冻结凭据值')
    if manifest.get('credentials') != {'source': 'project_dotenv_runtime_only', 'snapshot': 'bind_first_run', 'persist_values': False}:
        raise EvalError('运行凭据契约变化')
    if manifest.get('source_manifests') and set(manifest['source_manifests']) != {'baseline', 'candidate'}:
        raise EvalError('源码清单不完整')
    for arm, item in manifest.get('source_manifests', {}).items():
        if item.get('path') != 'source-manifests/' + arm + '.json' or r.digest(root / item['path']) != item.get('sha256'):
            raise EvalError('源码清单摘要变化')
    if manifest.get('policies') != {'baseline': 2, 'candidate': 4} or manifest.get('task_state_version') != 1 or manifest.get('read_memory_storage_version') != 1:
        raise EvalError('读取策略版本与普通任务状态契约变化')
    if manifest['binaries']['baseline']['sha256'] != manifest['binaries']['candidate']['sha256'] or manifest['source_manifests']['baseline']['sha256'] != manifest['source_manifests']['candidate']['sha256']:
        raise EvalError('同构建对照输入发生漂移')
    if manifest.get('read_features') != {'baseline': None, 'candidate': FEATURES}:
        raise EvalError('冻结能力组合变化')
    validate_frozen_oracles(root)
    return root, manifest


def validate_frozen_oracles(root):
    if r.file_list(root / 'fixture') != r.read_json(root / 'harness/fixture_manifest.json').get('files'):
        raise EvalError('冻结资料清单与私有验收版本不一致')
    spec = importlib.util.spec_from_file_location('quality_frozen_finite_cases', root / 'harness/finite_cases.py')
    finite = importlib.util.module_from_spec(spec); spec.loader.exec_module(finite)
    finite.selfcheck(finite.load_cases(root / 'harness/cases.json'))
    finite.timestamp_selfcheck(root / 'harness/timestamp_cases.json', root / 'fixture')
    spec = importlib.util.spec_from_file_location('quality_frozen_source_oracle', root / 'harness/quality_oracle.py')
    oracle = importlib.util.module_from_spec(spec); spec.loader.exec_module(oracle)
    checked = oracle.inspect_evidence(oracle.load_oracle(root / 'harness/oracle.json'), root / 'fixture')
    if checked.get('environment_result') != 'pass': raise EvalError('冻结oracle与来源不匹配')
    anchors = r.read_json(root / 'harness/anchors.json')
    if anchors.get('schema_version') != 1 or set(anchors.get('score_anchors', {})) != set(SCORES):
        raise EvalError('评分锚点不完整')
    rubrics = r.read_json(root / 'harness/rubric.json')['rubrics']
    if len(rubrics) != 8 or len({item['id'] for item in rubrics}) != 8 or any(not item.get('severity_policy') for item in rubrics):
        raise EvalError('完整八项量表及severity策略缺失')


def sample_arguments(manifest, candidate):
    enabled = 'on' if candidate else 'off'
    return ['-read-page-bytes', '8000', '-output-style', 'concise',
            '-source-views=' + enabled, '-local-checks=' + enabled, '-claim-focus=' + enabled]


def validate_candidate_spec(spec, manifest):
    if spec.get('read_context_version') != 4 or spec.get('read_features') != FEATURES:
        raise EvalError('候选未实际启用普通读取策略v4')


r.validate_candidate_spec = validate_candidate_spec


def physical_coverage(events, files):
    # source view只重用已有读据，不能为源文件补造物理读取；同源区间仍由helper取并集。
    accepted = [e for e in events if e.get('kind') == 'action_result'
                and e.get('payload', {}).get('last_action', {}).get('tool') in {'read_file', 'read_lines'}]
    return r.h.coverage(accepted, files)


r.coverage = physical_coverage


def observation_metrics(events, task_id, candidate):
    """只汇总明确采集的白名单元数据；创建/交付/处理/发送后的折叠分别统计。"""
    calls, stages, checks, batches, attempts, notes, config = {}, {}, {}, {}, {}, {}, None
    recording_complete = True
    for index, event in enumerate(events, 1):
        raw_payload = event.get('payload', {})
        if event.get('task_id') in (None, '', task_id) and (event.get('event_type') in {'gap', 'observation_gap'} or raw_payload.get('dropped', 0) or raw_payload.get('incomplete')):
            recording_complete = False
        if event.get('task_id') != task_id: continue
        kind, payload, identity = event.get('event_type'), event.get('payload', {}), event.get('model_call_id')
        if kind in {'gap', 'observation_gap'} or payload.get('dropped', 0) or payload.get('incomplete'):
            recording_complete = False
        if kind == 'run_configured' and isinstance(payload.get('read_features'), dict):
            if any(type(payload['read_features'].get(key)) is not bool for key in ('source_views', 'local_checks', 'claim_focus')):
                raise EvalError('观测能力配置缺少明确bool，不能补零')
            if config is not None and config != payload['read_features']: raise EvalError('观测能力配置发生变化')
            config = payload['read_features']
        elif kind in {'model_started', 'model_finished', 'model_usage', 'model_request_encoded'}:
            if not identity: raise EvalError('模型事件缺少model_call_id')
            call = calls.setdefault(identity, {})
            if kind == 'model_request_encoded':
                attempt = payload.get('http_attempt')
                if type(attempt) is not int or attempt < 1:
                    call.setdefault('unknown_encoded_attempts', []).append(payload); continue
                prior = call.setdefault('encoded_attempts_by_id', {})
                if attempt in prior and prior[attempt] != payload: raise EvalError('相同HTTP尝试身份出现冲突')
                prior[attempt] = payload
                call['encoded_attempts'] = list(prior.values()); continue
            if kind in call and call[kind] != payload: raise EvalError('相同模型身份出现冲突用量')
            call[kind] = payload
            if kind == 'model_started' and payload.get('stage') in {'quality_review', 'quality_revision'}:
                raise EvalError('普通实验不得混入质量复核/修订')
        elif kind == 'work_notes_response':
            if not identity: raise EvalError('笔记响应缺少model_call_id')
            if identity in notes and notes[identity] != payload: raise EvalError('笔记响应身份冲突')
            notes[identity] = payload
        elif kind.startswith('source_view_') and kind in {'source_view_created', 'source_view_historical', 'source_view_delivered', 'source_view_processed', 'source_view_folded'}:
            view = payload.get('view_evidence_id')
            if kind == 'source_view_historical' and not view: view = 'historical:' + str(event.get('action_id') or event.get('sequence') or index)
            if not view: raise EvalError('来源视图缺少身份')
            if kind in {'source_view_delivered', 'source_view_processed', 'source_view_folded'} and not identity:
                raise EvalError('视图阶段缺少模型身份，不能证明交付或折叠')
            key = (kind, view, identity if kind not in {'source_view_created', 'source_view_historical'} else '')
            if key in stages and stages[key] != payload: raise EvalError('来源视图阶段身份冲突')
            stages[key] = payload
        elif kind == 'tool_attempted' and payload.get('tool') == 'check_evidence':
            action = event.get('action_id') or payload.get('tool_call_id') or 'unknown:' + str(index)
            if action in attempts and attempts[action] != payload: raise EvalError('核对工具尝试身份冲突')
            attempts[action] = payload
        elif kind == 'local_check_batch':
            action = event.get('action_id'); count = payload.get('checks_requested')
            if not action or type(count) is not int or not 1 <= count <= 8: raise EvalError('核对批次缺少动作或请求项数')
            if action in batches and batches[action] != payload: raise EvalError('核对批次计数冲突')
            batches[action] = payload
        elif kind == 'local_check_completed':
            check = payload.get('check_id')
            if not check or payload.get('check_result') not in {'pass', 'fail', 'error', 'not_run'}: raise EvalError('核对回执身份/结果无效')
            if check in checks and checks[check] != payload: raise EvalError('核对回执身份冲突')
            checks[check] = payload
    if config is not None and config['source_views'] is False and stages:
        raise EvalError('关闭来源视图时出现视图阶段事件')
    if config is not None and config['local_checks'] is False and (batches or checks):
        raise EvalError('关闭核对时出现实际核对批次或完成回执')
    selected = [call for call in calls.values() if 'model_started' in call]
    ins = [call.get('model_usage', {}).get('input_tokens') for call in selected]
    outs = [call.get('model_usage', {}).get('output_tokens') for call in selected]
    costs = [call.get('model_usage', {}).get('cost') for call in selected]
    wire = [attempt.get('request_encoded_bytes') for call in selected for attempt in call.get('encoded_attempts', [])]
    unknown = sum(x is None or y is None for x, y in zip(ins, outs))
    main = {'model_calls': len(selected), 'input_tokens': None if unknown else sum(ins), 'output_tokens': None if unknown else sum(outs),
            'unknown_usage_calls': unknown, 'known_input_tokens': sum(x for x in ins if x is not None), 'known_output_tokens': sum(x for x in outs if x is not None),
            'cost': sum(costs) if selected and all(x is not None for x in costs) else None, 'known_cost': sum(x for x in costs if x is not None), 'unknown_cost_calls': costs.count(None),
            'http_attempts': len(wire) if selected and all(call.get('encoded_attempts') for call in selected) and not any(call.get('unknown_encoded_attempts') for call in selected) else None, 'encoded_request_bytes': sum(wire) if selected and all(call.get('encoded_attempts') and not call.get('unknown_encoded_attempts') for call in selected) and all(type(x) is int and x >= 0 for x in wire) else None,
            'elapsed_seconds': sum(call.get('model_finished', {}).get('duration_ms', 0) for call in selected) / 1000}
    keys = ('source_bytes', 'analysis_bytes', 'evidence_bytes', 'action_bytes', 'schema_bytes', 'folded_source_bytes', 'notes_metadata_removed_bytes', 'notes_metadata_retained_bytes', 'feature_prompt_bytes', 'local_check_schema_bytes', 'source_view_schema_bytes')
    totals, known, missing = {}, {}, {}
    for key in keys:
        values = [call['model_started'].get(key) for call in selected]
        if any(value is not None and (type(value) is not int or value < 0) for value in values): raise EvalError('材料字节指标无效')
        missing[key] = values.count(None); known[key] = sum(x for x in values if x is not None)
        totals[key] = sum(values) if selected and not missing[key] else None
    opportunities = sum(bool(item.get('notes_opportunity')) for item in notes.values())
    accepted = sum(bool(item.get('notes_opportunity')) and bool(item.get('notes_response_accepted')) for item in notes.values())
    note_metrics = {'observed_responses': len(notes), 'opportunities': opportunities if notes else None, 'accepted_opportunities': accepted if notes else None,
                    'response_adoption_rate': accepted / opportunities if opportunities else None,
                    'processed_sources': sum(item.get('notes_processed_sources', 0) for item in notes.values()) if notes else None,
                    'basis': 'model_call_id去重的响应机会，不使用逐工具missing作分母'}
    def confirmed(model):
        finish = calls.get(model, {}).get('model_finished')
        return finish is not None and not finish.get('error_class')
    def bytes_of(payload):
        size = payload.get('content_bytes')
        if type(size) is int and size >= 0: return size
        start, end = payload.get('byte_start'), payload.get('byte_end')
        return end - start if type(start) is int and type(end) is int and 0 <= start <= end else None
    view_metrics = {'observed_config': config, 'enabled': config.get('source_views') if config is not None else None, 'coverage_policy': 'physical_read_receipts_only; views never add coverage'}
    stage_known = config is not None or bool(stages)
    for stage in ('created', 'historical', 'delivered', 'processed', 'folded'):
        rows = [(key, payload) for key, payload in stages.items() if key[0] == 'source_view_' + stage]
        projected = rows
        if stage in {'delivered', 'processed'}: rows = [(key, payload) for key, payload in rows if confirmed(key[2])]
        if stage == 'folded': rows = [(key, payload) for key, payload in rows if calls.get(key[2], {}).get('encoded_attempts')]
        unique = {}
        for key, payload in rows:
            size = bytes_of(payload)
            if key[1] in unique and unique[key[1]] != size: raise EvalError('同一来源视图原文长度变化')
            unique[key[1]] = size
        sizes = [bytes_of(payload) for key, payload in rows]
        view_metrics[stage] = len(unique) if stage_known else None
        view_metrics[stage + '_observations'] = len(rows) if stage_known else None
        view_metrics[stage + '_unique_bytes'] = sum(unique.values()) if stage_known and all(x is not None for x in unique.values()) else None
        view_metrics[stage + '_cumulative_bytes'] = sum(sizes) if stage_known and all(x is not None for x in sizes) else None
        if stage == 'folded':
            view_metrics['folded_projection_observations'] = len(projected) if stage_known else None
            view_metrics['folded_unencoded_observations'] = len(projected) - len(rows) if stage_known else None
    check_known = config is not None or bool(batches) or bool(checks) or bool(attempts)
    results = {key: sum(item['check_result'] == key for item in checks.values()) for key in ('pass', 'fail', 'error', 'not_run')}
    requested = sum(item['checks_requested'] for item in batches.values()) if batches and len(attempts) <= len(batches) else (0 if config is not None and not attempts and not checks else None)
    if requested is not None and len(checks) > requested: raise EvalError('核对回执超过显式请求项数')
    decidable = results['pass'] + results['fail']
    check_metrics = {'enabled': config.get('local_checks') if config is not None else None, 'tool_attempts': len(attempts) if config is not None or attempts else None, 'validated_batches': len(batches) if check_known else None,
                     'checks_requested': requested, 'receipt_completed': len(checks) if check_known else None, 'completed': len(checks) if check_known else None,
                     'results': results if check_known else None, 'decidable': decidable if check_known else None,
                     'decidable_rate': decidable / requested if requested else None, 'semantic_result': 'not_run'}
    observed_views, observed_checks = dict(view_metrics), dict(check_metrics)
    if not recording_complete:
        for key in tuple(view_metrics):
            if key not in {'observed_config', 'enabled', 'coverage_policy'}: view_metrics[key] = None
        for key in tuple(check_metrics):
            if key not in {'enabled', 'semantic_result'}: check_metrics[key] = None
    return {'recording_complete': recording_complete, 'known_observed_source_views': observed_views if not recording_complete else None,
            'known_observed_local_checks': observed_checks if not recording_complete else None,
            'main': main, 'review': 'not_applicable', 'revision': 'not_applicable', 'source_views': view_metrics, 'local_checks': check_metrics,
            'materials': totals, 'known_material_bytes': known, 'unknown_material_calls': missing, 'work_notes': note_metrics}


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    item = r.inspect_sample(sample_root, manifest, session, returncode, sample_id)
    events = r.json_lines(sample_root / item['evidence']['task_events'])
    state = next(e['payload'] for e in events if e['kind'] == 'finished')
    spec, candidate = state['spec'], sample_id.startswith('candidate')
    if state.get('version') != 1 or spec.get('read_context_version') != (4 if candidate else 2):
        raise EvalError('普通任务状态版本或组别读取策略不符')
    if candidate and spec.get('read_features') != FEATURES or not candidate and spec.get('read_features') is not None:
        raise EvalError('两组能力组合与冻结实验不符')
    if any(spec.get(key) for key in ('quality', 'report', 'analysis', 'plan', 'read_scope')):
        raise EvalError('普通样本不得混入严格或显式完整阅读模式')
    if spec.get('output_style') != 'concise': raise EvalError('两组必须均采用精简输出')
    memory = state.get('read_memory')
    if memory is not None and memory.get('version') != 1: raise EvalError('读取记忆存储版本无效')
    after = r.read_json(sample_root / 'files-after.json')
    sources = [x for x in after if x['path'] != OUTPUT]
    target = next((x for x in after if x['path'] == OUTPUT), None)
    effects = state.get('last_result', {}).get('delivery', {}).get('effects', [])
    changed_paths = sorted({effect['file']['path'] for effect in effects if effect.get('file')
                            and effect['file'].get('path') != OUTPUT
                            and effect['file'].get('before_version') != effect['file'].get('after_version')})
    item['scope_pass'] = sources == manifest['files'] and not changed_paths
    item['receipt_scope_violations'] = changed_paths
    item['artifact_exists'] = target is not None
    item['artifact_sha256'] = target['sha256'] if target else None
    observed = r.json_lines(sample_root / item['evidence']['observations'])
    item['metrics']['mechanisms'] = observation_metrics(observed, item['task_id'], candidate)
    item['local_checks'] = state.get('last_result', {}).get('local_checks') if candidate else None
    item['local_check_semantic_result'] = 'not_run'
    # 外部覆盖只接受实际文件读取区间，绝不将source view按新读取累加。
    item['coverage']['basis'] = 'successful_action_read_receipts_current_frozen_versions'
    return item


def dotenv_snapshot(path):
    raw, values = Path(path).read_bytes(), {}
    allowed = {'DEEPSEEK_API_KEY', 'DEEPSEEK_BASE_URL', 'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY',
               'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy', 'SSL_CERT_FILE', 'SSL_CERT_DIR'}
    for line in raw.decode('utf-8').splitlines():
        line = line.strip()
        if not line or line.startswith('#'): continue
        if line.startswith('export '): line = line[7:].lstrip()
        key, sep, value = line.partition('='); key, value = key.strip(), value.strip()
        if not sep or key not in allowed: continue
        if value[:1] in {'"', "'"}:
            quote = value[0]; end = value.find(quote, 1)
            if end < 0 or value[end+1:].strip() and not value[end+1:].lstrip().startswith('#'):
                raise EvalError('项目.env引号格式不支持；未启动模型')
            value = value[1:end]
        else: value = re.split(r'\s+#', value, maxsplit=1)[0].rstrip()
        values[key] = value
    if not values.get('DEEPSEEK_API_KEY', '').strip(): raise EvalError('项目.env缺少Deepseek凭据；未启动模型')
    return hashlib.sha256(raw).hexdigest(), values


@contextmanager
def experiment_lock(root):
    path = root / 'run.lock'
    try: fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600); os.close(fd)
    except FileExistsError: raise EvalError('实验运行或历史锁尚存，禁止并发和重跑')
    try: yield
    finally: path.unlink(missing_ok=True)


def check_history(root, manifest, *, state_required=True):
    samples = {}
    for name in ORDER:
        path = root / 'samples' / name / 'result.json'
        if not path.exists(): continue
        item = r.read_json(path)
        if item.get('sample_id') != name: raise EvalError('逐例身份不匹配')
        if item.get('review'):
            review = r.read_json(path.with_name('review.json')); validate_review(root, manifest, name, review)
            if item['review'] != review: raise EvalError('逐例评审与保存档案不符')
        samples[name] = item
    if state_required and r.read_json(root / 'state.json').get('samples') != samples:
        raise EvalError('状态索引与逐例历史不符，禁止续跑')
    return samples


def stop_reason(item):
    if item.get('state') != 'completed': return '运行未确认，不自动重试'
    if item.get('infrastructure_error'): return item['infrastructure_error']
    if not item.get('protocol_valid'): return '结果协议错误'
    if not item.get('scope_pass'): return '发生禁止路径变更'
    if item.get('pending_actions'): return '存在未决动作，运行效果未确认'
    if item.get('status') == 'CANCELLED': return '运行取消'
    if item.get('metrics', {}).get('provider_errors'): return '模型传输或接口失败，不自动重试'
    if item.get('status') == 'FAILED': return '运行或存储基础设施失败'
    # BLOCKED / BUDGET_EXHAUSTED /无产物/低质量均为业务失败，不能拦住本批唯一候选。
    return None



def binary_preflight(root, manifest):
    # -h只解析CLI接口，不携带模型凭据，也不启动任务。防止付费基线跑完才发现候选入口缺失。
    required = {arm: ('-read-page-bytes', '-output-style', '-source-views', '-local-checks', '-claim-focus') for arm in ('baseline', 'candidate')}
    for arm, flags in required.items():
        command = [str(root / manifest['binaries'][arm]['path']), '-h']
        checked = subprocess.run(command, cwd=root, env={'PATH': os.defpath, 'LANG': 'en_US.UTF-8'}, capture_output=True, timeout=10)
        help_text = (checked.stdout + checked.stderr).decode('utf-8', errors='replace')
        if checked.returncode not in (0, 2) or any(not re.search(r'(?m)^\s*' + re.escape(flag) + r'(?:\s|$)', help_text) for flag in flags):
            raise EvalError(arm + '二进制未提供本profile所需入口，付费样本未启动')
    return {'result': 'pass', 'credentials': 'absent', 'provider_calls': 0}


def run_one(experiment):
    root, manifest = checked_manifest(experiment)
    with experiment_lock(root):
        state = r.read_json(root / 'state.json'); check_history(root, manifest)
        if state.get('stopped'): raise EvalError('实验已停止：' + str(state.get('stop_reason', '未知')))
        if any(item.get('state') != 'completed' for item in state['samples'].values()): raise EvalError('存在未确认样本，禁止重跑')
        name = next((n for n in ORDER if n not in state['samples']), None)
        if name is None: raise EvalError('双样本已完成，禁止扩样或重跑')
        try:
            r.preflight(root)
            snapshot, values = dotenv_snapshot(manifest['project_dotenv'])
            marker = root / 'runtime-environment.json'
            if marker.exists() and r.read_json(marker).get('dotenv_sha256') != snapshot: raise EvalError('项目.env已变化，停止批次')
            r.write_json(root / 'binary-preflight.json', binary_preflight(root, manifest))
            if not marker.exists(): r.write_json(marker, {'dotenv_sha256': snapshot, 'source': 'project_dotenv', 'values_persisted': False})
        except (EvalError, OSError, ValueError, subprocess.SubprocessError) as error:
            state.update(stopped=True, stop_reason=str(error)); r.write_json(root / 'state.json', state); raise
        arm = name.split('-')[0]; sample = root / 'samples' / name; work = sample / 'workspace'
        shutil.copytree(root / 'fixture', work); session = 'source-reuse-eval-' + uuid.uuid4().hex
        command = [str(root / manifest['binaries'][arm]['path']), '-dir', str(work), '-session', session,
                   '-mode', 'workspace', '-task-kind', 'change', '-model', SETTINGS['model'], '-provider', 'deepseek',
                   '-max-model-calls', '60', '-max-tool-calls', '120', '-max-turns', '30', '-timeout', '600s',
                   '-context-bytes', '300000', '-prompt', manifest['goal'], *sample_arguments(manifest, arm == 'candidate')]
        scratch = root / 'agent-runtime' / name
        for folder in ('home', 'tmp'): (scratch / folder).mkdir(parents=True, exist_ok=True, mode=0o700)
        env = {'PATH': os.defpath, 'HOME': str(scratch / 'home'), 'TMPDIR': str(scratch / 'tmp'), 'LANG': 'en_US.UTF-8',
               'PYTHONDONTWRITEBYTECODE': '1', 'CLAW_OBSERVE': 'on', 'CLAW_OBSERVE_DIR': str(sample / 'observations'),
               'CLAW_REQUEST_DIAGNOSTICS': 'metadata', 'CLAW_REQUEST_DIAGNOSTICS_DIR': str(sample / 'diagnostics'), **values}
        item = {'state': 'running', 'sample_id': name, 'arm': arm, 'group': 'ordinary', 'session_id': session,
                'started_at': r.now(), 'command': command, 'binary_sha256': manifest['binaries'][arm]['sha256'], 'fixture_digest': manifest['fixture_digest']}
        state['samples'][name] = item; r.write_json(root / 'state.json', state)
        r.write_json(sample / 'command.json', {'argv': command, 'cwd': str(work), 'environment_names': sorted(env)})
        r.write_json(sample / 'files-before.json', manifest['files'])
        print('启动 ' + name + '；不自动重试', flush=True)
        try:
            process = subprocess.Popen(command, cwd=work, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            item['process_id'] = process.pid; r.write_json(root / 'state.json', state)
            try: out, err = process.communicate(timeout=660)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGINT)
                try: out, err = process.communicate(timeout=10)
                except subprocess.TimeoutExpired: os.killpg(process.pid, signal.SIGKILL); out, err = process.communicate()
                item['infrastructure_error'] = '执行进程超时，结果未确认'
            for label, data in [('stdout', out), ('stderr', err)]:
                path = sample / (label + '.log'); path.write_text(r.sanitize(data.decode('utf-8', errors='replace'), env)); path.chmod(0o600)
            item['process_exit_code'] = process.returncode
            try: item.update(inspect_sample(sample, manifest, session, process.returncode, name))
            except (EvalError, OSError, ValueError, KeyError, TypeError) as error:
                item.update(protocol_valid=False, infrastructure_error=str(error), metrics=r.recover_metrics(sample))
            # 进程结束后检查隔离副本；范围/预算失败不短路验收。
            try:
                item['checker'] = r.run_checker(root, sample, manifest)
                if item['checker'].get('scope_result') == 'fail': item['scope_pass'] = False
            except (EvalError, OSError, ValueError, subprocess.SubprocessError) as error:
                item.update(checker={'artifact_result': 'error', 'scope_result': 'error', 'semantic_result': 'not_run'}, infrastructure_error=str(error))
            item.update(state='completed', finished_at=r.now()); item = r.sanitize_value(item, env); state['samples'][name] = item
            reason = stop_reason(item)
            if reason: state.update(stopped=True, stop_reason=reason)
            r.write_json(sample / 'result.json', item); r.write_json(root / 'state.json', state)
            return item
        except BaseException as error:
            item.update(state='unconfirmed', infrastructure_error=r.sanitize(str(error), env), metrics=r.recover_metrics(sample))
            state.update(stopped=True, stop_reason='运行中断或未确认，不自动重试')
            r.write_json(sample / 'result.json', item); r.write_json(root / 'state.json', state); raise


def mechanical_ok(item):
    return bool(item and item.get('protocol_valid') and not item.get('infrastructure_error') and item.get('scope_pass')
                and item.get('artifact_exists') and item.get('receipt_pass') and item.get('coverage', {}).get('complete')
                and item.get('status') == 'NEEDS_VERIFICATION' and item.get('exit_code') == 2 and not item.get('pending_actions')
                and item.get('checker', {}).get('artifact_result') == 'pass')


def artifact_bytes(root, sample_id):
    path = root / "samples" / sample_id / "workspace" / OUTPUT
    if path.is_symlink() or not path.is_file(): raise EvalError("评审产物缺失或不是普通文件")
    return path.read_bytes()


def source_evidence(root, manifest, ref):
    sources = {f["path"]: f for f in manifest["files"]}
    if not isinstance(ref, dict) or ref.get("path") not in sources: raise EvalError("评审证据不是冻结来源")
    source = sources[ref["path"]]
    if ref.get("source_sha256") != source["sha256"]: raise EvalError("评审来源SHA不匹配")
    a, b = ref.get("start_line"), ref.get("end_line")
    lines = (root / "fixture" / ref["path"]).read_text(encoding="utf-8").splitlines()
    if type(a) is not int or type(b) is not int or not 1 <= a <= b <= len(lines): raise EvalError("评审来源行号无效")
    if not isinstance(ref.get("quote"), str) or not ref["quote"].strip() or ref["quote"] not in "\n".join(lines[a-1:b]):
        raise EvalError("评审摘录不在真实源行范围")


def assessment_digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def validate_assessments_and_reverse(root, rubric, review):
    findings = {finding['id']: finding for finding in review['findings']}
    oracle = r.read_json(root / 'harness/oracle.json')
    evidence = {entry['id']: (source, entry) for source in oracle['sources'] for entry in source['evidence']}
    assessments = review.get('rubric_assessments')
    if not isinstance(assessments, dict) or set(assessments) != set(rubric):
        raise EvalError('冻结八项rubric必须逐项评审，不能抽查少量claim替代')
    for identity, assessment in assessments.items():
        if not isinstance(assessment, dict) or assessment.get('status') not in {'supported', 'incorrect', 'omitted', 'unverified'}:
            raise EvalError('rubric assessment状态无效')
        severity, ids = assessment.get('severity'), assessment.get('finding_ids')
        if severity not in {'none', 'ordinary', 'major'} or not isinstance(assessment.get('reason'), str) or not assessment['reason'].strip():
            raise EvalError('逐项评审须有severity和理由')
        if not isinstance(ids, list) or not ids or len(set(ids)) != len(ids) or not set(ids) <= set(findings):
            raise EvalError('逐项评审必须绑定真实finding')
        selected = [findings[ref] for ref in ids]
        expected = [evidence[ref] for ref in rubric[identity]['evidence_ids']]
        if not any(ref['path'] == source['path'] and ref['start_line'] <= entry['end_line'] and ref['end_line'] >= entry['start_line'] for finding in selected for ref in finding['evidence'] for source, entry in expected):
            raise EvalError('rubric评审依据没有定位到对应冻结来源')
        status = assessment['status']
        if status == 'supported':
            valid = any(f['kind'] == 'claim' and f['verdict'] == 'supported' and identity in f.get('rubric_ids', []) for f in selected)
            if severity != 'none': raise EvalError('来源支持的rubric不得标错误severity')
        elif status == 'omitted':
            valid = any(f['kind'] == 'omission' and f['rubric_id'] == identity for f in selected)
        else:
            valid = any(f['kind'] == 'claim' and f['verdict'] == status and identity in f.get('rubric_ids', []) for f in selected)
        if not valid or status != 'supported' and severity == 'none':
            raise EvalError('assessment与finding类型/判断不一致')
        if status == 'omitted' and severity != rubric[identity]['severity_policy']['omitted']:
            raise EvalError('遗漏severity必须遵守冻结量表，不能事后降低门槛')
        if severity == 'major' and status in {'incorrect', 'omitted'}:
            marked = set(review['major_error_ids'] if status == 'incorrect' else review['major_omission_ids'])
            if not marked.intersection(ids): raise EvalError('major assessment必须计入对应重大错误/遗漏')
    if any(finding.get('verdict') == 'incorrect' for finding in findings.values()) and review['scores']['correctness'] == 2:
        raise EvalError('存在明确错误时不能按冻结锚点给正确性2分')
    if review['major_errors'] and review['scores']['correctness'] != 0:
        raise EvalError('重大事实错误对应正确性0分锚点')
    if any(item['status'] == 'omitted' for item in assessments.values()) and review['scores']['important_findings'] == 2:
        raise EvalError('存在明确量表遗漏时不能给重要发现满分')
    reverse = review.get('reverse_check')
    if not isinstance(reverse, dict) or type(reverse.get('consistent')) is not bool or not isinstance(reverse.get('reason'), str) or not reverse['reason'].strip():
        raise EvalError('必须保存反序复查一致性及理由')
    mapping = r.read_json(root / 'review-map.json')
    first, second = reverse.get('first_order'), reverse.get('reverse_order')
    if not isinstance(first, list) or len(first) != len(set(first)) or set(first) != set(mapping) or first != r.read_json(root / 'review-order.json') or second != list(reversed(first)):
        raise EvalError('反序复查必须覆盖匿名包全部产物，严格反转初审顺序')
    scores = reverse.get('initial_scores')
    if not isinstance(scores, dict) or set(scores) != set(SCORES) or any(type(n) is not int or n not in (0, 1, 2) for n in scores.values()):
        raise EvalError('反序复查须保留初审分数')
    digest = reverse.get('initial_assessments_sha256')
    if not isinstance(digest, str) or not re.fullmatch('[0-9a-f]{64}', digest): raise EvalError('缺少初审逐项评审摘要')
    if reverse['consistent'] and (scores != review['scores'] or digest != assessment_digest(assessments)):
        raise EvalError('声称一致但初审/复查分数或逐项评审发生变化')


def validate_review(root, manifest, sample_id, review):
    if not isinstance(review.get("reviewer"), str) or not review["reviewer"].strip() or review.get("method") not in {"human", "source_audit_agent"}:
        raise EvalError("必须填写真实评审者与human/source_audit_agent方式")
    if set(review.get("scores", {})) != set(SCORES) or any(type(v) is not int or v not in (0, 1, 2) for v in review["scores"].values()):
        raise EvalError("五项量表均须为0/1/2")
    if set(review.get("score_basis", {})) != set(SCORES): raise EvalError("五项量表均须提供实际依据")
    if type(review.get("major_errors")) is not int or review["major_errors"] < 0: raise EvalError("重大错误数无效")
    mapping = r.read_json(root / "review-map.json")
    binding = mapping.get(review.get("review_id"))
    artifact = artifact_bytes(root, sample_id)
    sha = hashlib.sha256(artifact).hexdigest()
    if binding != {"sample_id": sample_id, "artifact_sha256": sha} or review.get("artifact_sha256") != sha:
        raise EvalError("匿名评审身份或产物SHA未绑定到实际样本")
    if not isinstance(review.get("findings"), list) or not review["findings"] or any(not isinstance(f, dict) for f in review["findings"]):
        raise EvalError("须记录实际结论或rubric遗漏")
    identities = [finding.get("id") for finding in review["findings"]]
    if any(not isinstance(identity, str) or not identity.strip() for identity in identities) or len(set(identities)) != len(identities):
        raise EvalError("每个finding必须有唯一非空id")
    for basis in review["score_basis"].values():
        if (not isinstance(basis, dict) or not isinstance(basis.get("reason"), str) or not basis["reason"].strip()
                or not isinstance(basis.get("finding_ids"), list) or not basis["finding_ids"]
                or any(not isinstance(identity, str) for identity in basis["finding_ids"]) or not set(basis["finding_ids"]) <= set(identities)):
            raise EvalError("每项score_basis必须用reason及finding_ids绑定真实证据")
    major_ids = review.get("major_error_ids")
    if (not isinstance(major_ids, list) or any(not isinstance(identity, str) for identity in major_ids)
            or len(major_ids) != len(set(major_ids)) or len(major_ids) != review["major_errors"] or not set(major_ids) <= set(identities)):
        raise EvalError("major_errors必须与已绑定证据的major_error_ids一致")
    rubric = {x["id"]: x for x in r.read_json(root / "harness/rubric.json")["rubrics"]}
    oracle = r.read_json(root / "harness/oracle.json")
    evidence_ids = {e["id"]: (s, e) for s in oracle["sources"] for e in s["evidence"]}
    for finding in review["findings"]:
        kind = finding.get("kind")
        if kind == "claim":
            start, end, claim = finding.get("start_byte"), finding.get("end_byte"), finding.get("claim")
            if type(start) is not int or type(end) is not int or not 0 <= start < end <= len(artifact) or not isinstance(claim, str) or not claim.strip():
                raise EvalError("claim必须绑定真实UTF8字节范围")
            if artifact[start:end] != claim.encode("utf-8"): raise EvalError("claim文本与产物字节span不一致")
            if finding.get("verdict") not in {"supported", "incorrect", "unverified"}: raise EvalError("claim判断无效")
            if not set(finding.get("rubric_ids", [])) <= set(rubric): raise EvalError("claim关联未知rubric")
        elif kind == "omission":
            if any(k in finding for k in ("claim", "start_byte", "end_byte")): raise EvalError("遗漏不能伪造产物claim/span")
            if finding.get("rubric_id") not in rubric or not isinstance(finding.get("reason"), str) or not finding["reason"].strip():
                raise EvalError("遗漏必须绑定rubric_id并说明原因")
        else: raise EvalError("finding必须是claim或omission")
        if finding["id"] in major_ids and kind == "claim" and finding.get("verdict") != "incorrect":
            raise EvalError("重大错误ID须指向incorrect claim或rubric遗漏")
        if not isinstance(finding.get("evidence"), list) or not finding["evidence"]: raise EvalError("finding缺少冻结原文依据")
        for ref in finding["evidence"]: source_evidence(root, manifest, ref)
        if kind == "omission":
            expected = [evidence_ids[k] for k in rubric[finding["rubric_id"]]["evidence_ids"]]
            if not any(ref["path"] == s["path"] and ref["start_line"] <= e["end_line"] and ref["end_line"] >= e["start_line"] for ref in finding["evidence"] for s, e in expected):
                raise EvalError("遗漏依据没有定位到对应rubric来源")
    omission_ids = review.get("major_omission_ids")
    if (type(review.get("major_omissions")) is not int or review["major_omissions"] < 0
            or not isinstance(omission_ids, list) or any(not isinstance(identity, str) for identity in omission_ids)
            or len(omission_ids) != len(set(omission_ids)) or len(omission_ids) != review["major_omissions"]
            or not set(omission_ids) <= set(identities)):
        raise EvalError("重大遗漏必须按major_omission_ids绑定")
    for finding in review["findings"]:
        if finding["id"] in omission_ids and finding.get("kind") != "omission":
            raise EvalError("major_omission_ids必须指向真实rubric遗漏")
        if finding["id"] in major_ids and finding.get("kind") != "claim":
            raise EvalError("major_error_ids必须指向incorrect claim；遗漏单列")
    checks = review.get("quality_checks", {})
    if set(checks) != set(SPECIAL): raise EvalError("必须分别评审修正建议、主体身份及重要遗漏")
    for check in checks.values():
        if (not isinstance(check, dict) or check.get("verdict") not in {"supported", "incorrect", "not_applicable"}
                or not isinstance(check.get("reason"), str) or not check["reason"].strip()
                or not isinstance(check.get("finding_ids"), list)
                or check["verdict"] != "not_applicable" and not check["finding_ids"]
                or not set(check.get("finding_ids", [])) <= set(identities)):
            raise EvalError("专项评审须填写原因，并绑定实际finding或明确不适用")
    validate_assessments_and_reverse(root, rubric, review)
    return review


def review_ok(review):
    return bool(review and sum(review['scores'].values()) >= 8 and review['major_errors'] == 0
                and review['major_omissions'] == 0 and all(c['verdict'] != 'incorrect' for c in review['quality_checks'].values())
                and all(a['severity'] != 'major' for a in review['rubric_assessments'].values()))


def export_review(experiment, output):
    root, manifest = checked_manifest(experiment, require_current=False)
    target = Path(output).absolute()
    if target == root or target.is_relative_to(root) or target.exists(): raise EvalError('匿名包须新建在实验目录之外')
    with experiment_lock(root):
        samples = check_history(root, manifest)
        if len(samples) != 2 or any(item.get('state') != 'completed' for item in samples.values()):
            raise EvalError('两样本结束后统一盲评，不能在组间更换评审者或提前选分')
        names = [n for n, item in samples.items() if item.get('state') == 'completed' and item.get('artifact_exists')]
        if not names: raise EvalError('没有可评分产物；不能给缺失产物记零分')
        mapping = r.read_json(root / 'review-map.json') if (root / 'review-map.json').exists() else {}
        entries = []
        for name in names:
            sha = hashlib.sha256(artifact_bytes(root, name)).hexdigest()
            if sha != samples[name].get('artifact_sha256'): raise EvalError('产物相对执行结果已变化')
            existing = [(rid, value) for rid, value in mapping.items() if value['sample_id'] == name]
            if len(existing) > 1 or existing and existing[0][1]['artifact_sha256'] != sha: raise EvalError('匿名映射或已导出产物变化')
            rid = existing[0][0] if existing else uuid.uuid4().hex
            mapping[rid] = {'sample_id': name, 'artifact_sha256': sha}; entries.append((rid, name, sha))
        random.SystemRandom().shuffle(entries)
        order_path = root / 'review-order.json'
        if order_path.exists():
            order = r.read_json(order_path)
            if set(order) != {entry[0] for entry in entries}: raise EvalError('匿名评审顺序身份变化')
            entries.sort(key=lambda entry: order.index(entry[0]))
        else:
            r.write_json(order_path, [entry[0] for entry in entries])
        target.mkdir(parents=True, mode=0o700); shutil.copytree(root / 'fixture', target / 'sources')
        for name in ('rubric.json', 'oracle.json', 'anchors.json'): shutil.copyfile(root / 'harness' / name, target / name)
        public = []
        for rid, name, sha in entries:
            artifact = target / (rid + '.md'); artifact.write_bytes(artifact_bytes(root, name)); artifact.chmod(0o600)
            template = {'review_id': rid, 'artifact_sha256': sha, 'reviewer': '', 'method': '',
                        'scores': dict.fromkeys(SCORES, None), 'score_basis': {s: {'reason': '', 'finding_ids': []} for s in SCORES},
                        'major_errors': None, 'major_error_ids': [], 'major_omissions': None, 'major_omission_ids': [], 'findings': [],
                        'rubric_assessments': {item['id']: {'status': None, 'severity': None, 'reason': '', 'finding_ids': []} for item in r.read_json(root / 'harness/rubric.json')['rubrics']},
                        'reverse_check': {'first_order': [entry[0] for entry in entries], 'reverse_order': [entry[0] for entry in reversed(entries)], 'consistent': None, 'initial_scores': dict.fromkeys(SCORES, None), 'initial_assessments_sha256': '', 'reason': ''},
                        'quality_checks': {s: {'verdict': None, 'reason': '', 'finding_ids': []} for s in SPECIAL}}
            r.write_json(target / (rid + '.review.json'), template)
            public.append({'review_id': rid, 'artifact_sha256': sha, 'artifact': rid + '.md', 'template': rid + '.review.json'})
        r.write_json(target / 'index.json', {'schema_version': 1, 'goal': manifest['goal'], 'reviews': public,
                    'review_process': {'first_order': [entry[0] for entry in entries], 'reverse_order': [entry[0] for entry in reversed(entries)], 'same_reviewer': True, 'same_method': True},
                    'semantic_result': 'not_run', 'source_versions': [{'path': f['path'], 'sha256': f['sha256']} for f in manifest['files']],
                    'instructions': '先匿名评审产物，逐条绑定UTF8字节区间及原文；遗漏绑定量表，不能伪造输出。核对建议是否成立、主体/版本是否一致及重要遗漏；局部检查不代替语义审查。source_audit_agent不是独立人工。'})
        r.write_json(root / 'review-map.json', mapping)
    return {'output': str(target), 'reviews': len(entries), 'semantic_result': 'not_run', 'paid_calls': 0}


def record_review(experiment, review_path):
    root, manifest = checked_manifest(experiment)
    with experiment_lock(root):
        check_history(root, manifest)
        review = r.read_json(review_path)
        if not isinstance(review, dict): raise EvalError('评审必须是JSON对象')
        mapping = r.read_json(root / 'review-map.json')
        binding = mapping.get(review.get('review_id'))
        if not isinstance(binding, dict) or binding.get('sample_id') not in ORDER: raise EvalError('未知匿名review_id')
        # 唯一归属路径：review-map -> sample_id -> 实际产物SHA，绝不依赖导出顺序。
        name = binding['sample_id']; state = r.read_json(root / 'state.json'); item = state['samples'].get(name)
        if not item or item.get('state') != 'completed' or not item.get('artifact_exists'): raise EvalError('没有可评审的完成产物')
        if item.get('review') or (root / 'samples' / name / 'review.json').exists(): raise EvalError('评审已存在，禁止覆盖')
        validate_review(root, manifest, name, review)
        for other_name, other_item in state['samples'].items():
            old = other_item.get('review')
            if old and (old['reviewer'] != review['reviewer'] or old['method'] != review['method']):
                raise EvalError('同一配对必须由同一评审者以相同方式审查；不得计算跨评审者分差')
            if old and old['reverse_check']['first_order'] != review['reverse_check']['first_order']:
                raise EvalError('两份评审的匿名初审/反序顺序不同')
        review = dict(review, reviewed_at=r.now(), semantic_result='pass' if review_ok(review) else 'fail')
        item['review'] = review
        r.write_json(root / 'samples' / name / 'review.json', review)
        r.write_json(root / 'samples' / name / 'result.json', item); r.write_json(root / 'state.json', state)
        return {'sample_id': name, 'artifact_sha256': binding['artifact_sha256'], 'review': review}


def summarize(experiment):
    root, manifest = checked_manifest(experiment, require_current=False)
    samples = check_history(root, manifest, state_required=False)
    baseline, candidate = (samples.get(n, {}) for n in ORDER)
    complete = len(samples) == 2 and all(s.get('state') == 'completed' for s in samples.values())
    both_reviewed = bool(baseline.get('artifact_exists') and candidate.get('artifact_exists') and baseline.get('review') and candidate.get('review'))
    paired_protocol = bool(both_reviewed and baseline['review']['reviewer'] == candidate['review']['reviewer'] and baseline['review']['method'] == candidate['review']['method'] and all(item['review']['reverse_check']['consistent'] for item in (baseline, candidate)) and baseline['review']['reverse_check']['first_order'] == candidate['review']['reverse_check']['first_order'])
    reviewed = paired_protocol
    delta = sum(candidate['review']['scores'].values()) - sum(baseline['review']['scores'].values()) if reviewed else None
    cq = ('pass' if review_ok(candidate['review']) else 'fail') if candidate.get('review') else 'not_run'
    quality = ('pass' if cq == 'pass' and delta >= 0 else 'fail') if reviewed else 'not_run'
    major_errors_delta = candidate['review']['major_errors'] - baseline['review']['major_errors'] if reviewed else None
    major_omissions_delta = candidate['review']['major_omissions'] - baseline['review']['major_omissions'] if reviewed else None
    quality_improved = bool(delta >= 0 and (delta > 0 or major_errors_delta < 0 or major_omissions_delta < 0)) if reviewed else None
    bm, cm = baseline.get('metrics', {}), candidate.get('metrics', {})
    tokens_base, tokens_new = bm.get('total_tokens'), cm.get('total_tokens')
    known_calls = [s.get('metrics', {}).get('model_calls') for s in samples.values()]
    state = r.read_json(root / 'state.json') if (root / 'state.json').exists() else {}
    cost_comparable = bool(reviewed and quality == 'pass' and mechanical_ok(baseline) and mechanical_ok(candidate))
    raw_token_reduction = 1 - tokens_new / tokens_base if tokens_base and tokens_new is not None else None
    report = {'schema_version': 1, 'profile': PROFILE, 'experiment_id': manifest['experiment_id'], 'completed': complete,
              'passed': bool(complete and mechanical_ok(candidate) and quality == 'pass'),
              'candidate_delivery_result': ('pass' if mechanical_ok(candidate) else 'fail') if candidate else 'not_run',
              'candidate_quality_result': cq, 'quality_result': quality, 'paired_quality_delta_result': 'pass' if reviewed else 'not_run',
              'paired_quality_delta': delta, 'quality_improved': quality_improved, 'quality_comparison_available': bool(reviewed), 'paired_review_protocol': 'pass' if paired_protocol else 'not_run',
              'major_error_delta': major_errors_delta, 'major_omission_delta': major_omissions_delta,
              'rubric_changes': {key: {'baseline': baseline['review']['rubric_assessments'][key]['status'], 'candidate': candidate['review']['rubric_assessments'][key]['status']} for key in baseline['review']['rubric_assessments']} if reviewed else None,
              'promote_default': False, 'samples': samples, 'max_samples': 2, 'max_model_admissions': 120,
              'total_model_admissions': sum(known_calls) if all(x is not None for x in known_calls) else None,
              'known_model_admissions': sum(x for x in known_calls if x is not None), 'unknown_model_admission_samples': known_calls.count(None),
              'cost_comparison': {'baseline': bm, 'candidate': cm,
                                  'token_reduction': raw_token_reduction if cost_comparable else None,
                                  'raw_token_reduction': raw_token_reduction, 'comparison_result': 'pass' if cost_comparable else 'not_run',
                                  'cost_gate': 'not_applicable', 'secondary_targets': {'model_calls_at_most': 12, 'tool_calls_at_most': 40, 'token_reduction_at_least': 0.3}, 'interpretation': '同等质量优先低成本；失败消耗全部保留，不能靠缺产物宣布降耗'},
              'stopped': state.get('stopped', False), 'stop_reason': state.get('stop_reason'), 'limitations': LIMITATIONS}
    r.write_json(root / 'summary.json', report); return report


def selfcheck():
    import unittest
    suite = unittest.defaultTestLoader.discover(str(HERE), pattern='test_*.py')
    return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__); sub = parser.add_subparsers(dest='command', required=True)
    sub.add_parser('selfcheck'); p = sub.add_parser('init')
    for name in ('output', 'baseline-bin', 'candidate-bin', 'baseline-source-manifest', 'candidate-source-manifest'): p.add_argument('--' + name, required=True)
    for name in ('source-workspace', 'project-dotenv'): p.add_argument('--' + name)
    for name in ('run-one', 'export-review', 'import-review', 'review', 'summarize'):
        p = sub.add_parser(name); p.add_argument('--experiment', required=True)
        if name == 'export-review': p.add_argument('--output', required=True)
        if name in ('import-review', 'review'): p.add_argument('--review-file', required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == 'selfcheck': return selfcheck()
        if args.command == 'init':
            result = init_experiment(args.output, args.baseline_bin, args.candidate_bin, args.source_workspace,
                                     args.project_dotenv, args.baseline_source_manifest, args.candidate_source_manifest)
        elif args.command == 'run-one': result = run_one(args.experiment)
        elif args.command == 'export-review': result = export_review(args.experiment, args.output)
        elif args.command in ('import-review', 'review'): result = record_review(args.experiment, args.review_file)
        else: result = summarize(args.experiment)
        print(json.dumps(result, ensure_ascii=False, indent=2)); return 0
    except (EvalError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print('评测停止：' + str(error), file=sys.stderr); return 2


if __name__ == '__main__': raise SystemExit(main())
