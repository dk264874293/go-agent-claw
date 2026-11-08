#!/usr/bin/env python3
"""普通交付前核对 v1 独立双样本：仅 run-one 启动真实CLI；质量优先，不自动重试。"""
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
DEFAULT_WORKSPACE = HERE.parent / 'fixture' if (HERE / 'common.py').is_file() else REPO / 'eval/fixtures/delivery_review_v1/workspace'
COMMON = HERE / 'common.py' if (HERE / 'common.py').is_file() else REPO / 'scripts/write_efficiency_eval/run.py'
_spec = importlib.util.spec_from_file_location('delivery_review_common', COMMON)
r = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(r)
EvalError = r.EvalError
_protocol_spec = importlib.util.spec_from_file_location('delivery_review_protocol', HERE / 'review_protocol.py')
protocol = importlib.util.module_from_spec(_protocol_spec); _protocol_spec.loader.exec_module(protocol)
PROFILE = 'delivery_review_v1'
ORDER = ['baseline-ordinary-1', 'candidate-ordinary-1']
OUTPUT = r.OUTPUT
SCORES = tuple(r.SCORES)
SPECIAL = ('proposed_repairs', 'subject_identity', 'important_omissions')
SETTINGS = dict(r.SETTINGS)
ORACLE = HERE / 'oracle.json' if (HERE / 'oracle.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/semantic_oracle.json'
RUBRIC = HERE / 'rubric.json' if (HERE / 'rubric.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/rubric.json'
CASES = HERE / 'cases.json' if (HERE / 'cases.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/cases.json'
FIXTURE_MANIFEST = HERE / 'fixture_manifest.json' if (HERE / 'fixture_manifest.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/manifest.json'
ISSUE_POLICY = HERE / 'issue_policy.json' if (HERE / 'issue_policy.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/issue_policy.json'
ANCHORS = HERE / 'anchors.json' if (HERE / 'anchors.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/anchors.json'
FEATURES = {'version': 1, 'source_views': False, 'local_checks': True, 'claim_focus': False}
TIMESTAMP_CASES = HERE / 'timestamp_cases.json' if (HERE / 'timestamp_cases.json').is_file() else REPO / 'eval/fixtures/delivery_review_v1/timestamp_cases.json'
GOAL = ('完整阅读当前工作区各资料包中的全部Markdown，仅在当前目录新建优化汇总.md，不修改原始资料。'
        '精简概览资料并指出明显矛盾，核对被引用的数值、公式、默认值及版本条件。'
        '重要结论须就地引用完整相对文件路径及行号；跨文档判断同时给出两端依据。'
        '区分原文转述、静态推导、建议及实际运行；未执行的内容保留待实测边界。'
        '一项问题只展开一次，不重复粘贴长篇原文或示例代码。')
LIMITATIONS = ('同任务同模型一对普通任务的小样本；read-features-v2仅历史症状，不替代同批baseline；'
               '内部局部核对、来源复用和引用位置检查不替代外部语义审查；质量优先，'
               '同二进制delivery-review off/on，12/40和Token下降30%仅次级观察目标；未知费用null；不执行课程代码。')
r.ORDER, r.PROFILE_VERSION = list(ORDER), 7
r.build_goal = lambda files: GOAL
r.strict_sample = lambda name: False
r.harness_inputs = lambda: harness_inputs()


def harness_inputs():
    return {'run.py': Path(__file__), 'common.py': COMMON, 'helpers.py': r.HELPERS,
            'checker.py': HERE / 'checker.py', 'test_run.py': HERE / 'test_run.py', 'review_protocol.py': HERE / 'review_protocol.py', 'go_nil_slices.json': HERE / 'go_nil_slices.json', 'issue_policy.json': ISSUE_POLICY, 'test_native_cli.py': HERE / 'test_native_cli.py', 'README.md': HERE / 'README.md', 'finite_cases.py': HERE / 'finite_cases.py', 'timestamp_cases.json': TIMESTAMP_CASES, 'fixture_manifest.json': FIXTURE_MANIFEST, 'oracle.json': ORACLE, 'rubric.json': RUBRIC, 'anchors.json': ANCHORS, 'cases.json': CASES,
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
                    project_dotenv=str(Path(project_dotenv or REPO / '.env').absolute()), source_manifests={}, policies={'baseline': 4, 'candidate': 4}, task_state_versions={'baseline': 1, 'candidate': 3}, read_memory_storage_version=1, read_features={'baseline': FEATURES, 'candidate': FEATURES}, delivery_review={'baseline': None, 'candidate': {'version': 1}})
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
    if manifest.get('policies') != {'baseline': 4, 'candidate': 4} or manifest.get('task_state_versions') != {'baseline': 1, 'candidate': 3} or manifest.get('read_memory_storage_version') != 1:
        raise EvalError('读取策略版本与普通任务状态契约变化')
    if manifest['binaries']['baseline']['sha256'] != manifest['binaries']['candidate']['sha256'] or manifest['source_manifests']['baseline']['sha256'] != manifest['source_manifests']['candidate']['sha256']:
        raise EvalError('同构建对照输入发生漂移')
    if manifest.get('read_features') != {'baseline': FEATURES, 'candidate': FEATURES} or manifest.get('delivery_review') != {'baseline': None, 'candidate': {'version': 1}}:
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
    issue_policy = r.read_json(root / 'harness/issue_policy.json')
    if issue_policy.get('schema_version') != 1 or set(issue_policy.get('special_mappings', {})) != set(SPECIAL):
        raise EvalError('冻结问题分类与SPECIAL映射缺失')
    anchors = r.read_json(root / 'harness/anchors.json')
    if anchors.get('schema_version') != 1 or set(anchors.get('score_anchors', {})) != set(SCORES):
        raise EvalError('评分锚点不完整')
    rubrics = r.read_json(root / 'harness/rubric.json')['rubrics']
    if len(rubrics) != 8 or len({item['id'] for item in rubrics}) != 8 or any(not item.get('severity_policy') for item in rubrics):
        raise EvalError('完整八项量表及severity策略缺失')


def sample_arguments(manifest, candidate):
    enabled = 'on' if candidate else 'off'
    return ['-read-page-bytes', '8000', '-output-style', 'concise',
            '-source-views=off', '-local-checks=on', '-claim-focus=off', '-delivery-review=' + enabled]


def validate_candidate_spec(spec, manifest):
    if spec.get('read_context_version') != 4 or spec.get('read_features') != FEATURES or spec.get('delivery_review') != {'version': 1}:
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
    keys = ('source_bytes', 'analysis_bytes', 'evidence_bytes', 'action_bytes', 'schema_bytes', 'folded_source_bytes', 'notes_metadata_removed_bytes', 'notes_metadata_retained_bytes', 'feature_prompt_bytes', 'delivery_review_prompt_bytes', 'local_check_schema_bytes', 'source_view_schema_bytes')
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




def phase_metrics(events, task_id):
    calls={}
    for e in events:
        if e.get('task_id')!=task_id or not e.get('model_call_id'):continue
        kind=e.get('event_type');item=calls.setdefault(e['model_call_id'],{})
        if kind in {'model_started','model_usage','model_finished'}:item[kind]=e.get('payload',{})
    out={}
    for phase in ('main','delivery_review'):
        chosen=[v for v in calls.values() if 'model_started' in v and (v['model_started'].get('stage') or 'main')==phase]
        ins=[v.get('model_usage',{}).get('input_tokens') for v in chosen];outs=[v.get('model_usage',{}).get('output_tokens') for v in chosen];costs=[v.get('model_usage',{}).get('cost') for v in chosen]
        out[phase]={'model_calls':len(chosen),'input_tokens':sum(ins) if all(type(v) is int for v in ins) else None,
                    'output_tokens':sum(outs) if all(type(v) is int for v in outs) else None,
                    'cost':sum(costs) if all(type(v) in (int,float) for v in costs) else None,
                    'known_input_tokens':sum(v for v in ins if type(v) is int),'known_output_tokens':sum(v for v in outs if type(v) is int),
                    'unknown_usage_calls':sum(type(a) is not int or type(b) is not int for a,b in zip(ins,outs)),
                    'elapsed_seconds':sum(v.get('model_finished',{}).get('duration_ms') or 0 for v in chosen)/1000}
    other=[v for v in calls.values() if 'model_started' in v and (v['model_started'].get('stage') or 'main') not in {'main','delivery_review'}]
    if other:raise EvalError('普通交付实验出现未允许的模型阶段')
    if out['delivery_review']['model_calls']>1:raise EvalError('交付前核对模型阶段超过一次')
    return out



def model_admission_accounting(state, observations, task_id):
    budget = state.get('last_result', {}).get('usage', {}).get('model_calls')
    own = [e for e in observations if e.get('task_id') == task_id and e.get('event_type') == 'model_started']
    started = {e.get('model_call_id') for e in own}
    if None in started or '' in started or type(budget) is not int: raise EvalError('模型准入或实际请求身份无效')
    actual = len(started); unissued = budget - actual
    d = state.get('delivery_review') or {}
    flags = {}
    for key in ('turn_consumed', 'model_consumed'):
        value = d.get(key, False)  # DeliveryReviewState v1的omitempty明确表示false，不是未知。
        if type(value) is not bool: raise EvalError('交付预留消费标志必须是bool，不能为null或其他类型')
        flags[key] = value
    if flags['model_consumed'] and not flags['turn_consumed']:
        raise EvalError('交付预留不能先消费模型票据再消费轮次')
    if unissued:
        cfg = state.get('spec', {}).get('delivery_review')
        valid_cfg = isinstance(cfg, dict) and set(cfg) == {'version'} and type(cfg.get('version')) is int and cfg['version'] == 1
        actual_review = {e.get('model_call_id') for e in own if e.get('payload', {}).get('stage') == 'delivery_review'}
        # 标志描述本地票据消费，不证明已进入Provider。消费前/后立即取消都可能尚无model_started。
        if unissued != 1 or type(state.get('version')) is not int or state['version'] != 3 or not valid_cfg or type(d.get('version')) is not int or d['version'] != 1 or type(d.get('admission_count')) is not int or d['admission_count'] != 1 or actual_review or d.get('status') not in {'failed', 'outcome_unknown'} or d.get('origin_run_id') != state.get('last_result', {}).get('run_id'):
            raise EvalError('模型准入与实际请求差额没有合法的一次交付预留解释')
    return {'budget_model_admissions': budget, 'actual_model_requests': actual, 'unissued_reserved_model_admissions': unissued}



def recorded_slice(record, key):
    """Go已知slice字段的省略/null均为空；非数组不能静默当作没有发生。"""
    value = record.get(key)
    if value is None: return []
    if not isinstance(value, list): raise EvalError('任务记录字段应为数组或null：' + key)
    return value


def delivery_mechanisms(task_events, state, item):
    """只计动作当时已提交的来源声明；不从声明推断报告已正确修正。"""
    run = item['run_id']; bound = set(); current_bound = set(); ready = {}; dispositions = {}; adopted = set(); adopted_results = {}
    files = {v['path']: v['sha256'] for v in item.get('_source_files', [])}
    def valid_evidence(memory, eid, agent=None):
        e = (memory.get('evidence') or {}).get(eid) or {}; receipt = e.get('receipt') or {}
        return e.get('run_id') == run and (agent is None or e.get('agent_id') == agent) and receipt.get('path') in files and receipt.get('version') == files[receipt['path']]
    def source_findings(snapshot):
        memory = snapshot.get('read_memory') or {}; invalid = {eid for v in recorded_slice(memory, 'invalidations') if v.get('run_id') == run for eid in recorded_slice(v, 'evidence_ids')}; found = set()
        for agent, report in (memory.get('notes') or {}).items():
            for f in recorded_slice(report, 'findings'):
                refs = recorded_slice(f, 'evidence')
                if f.get('status') == 'evidence_valid' and refs and all(ref.get('evidence_id') not in invalid and valid_evidence(memory, ref.get('evidence_id'), agent) for ref in refs):
                    found.add((agent, f.get('id'), protocol.digest(f)))
        return found
    for event in task_events:
        snap = event.get('payload') or {}; memory = snap.get('read_memory') or {}; d = snap.get('delivery_review') or {}; summary = d.get('summary') or {}
        if event.get('kind') == 'analysis_updated': bound |= source_findings(snap)
        for issue in recorded_slice(summary, 'unresolved') + recorded_slice(d, 'issues'):
            if issue.get('kind') == 'ready_to_resolve' and issue.get('id') not in ready:
                ready[issue['id']] = {'path': issue.get('path'), 'version': issue.get('version'), 'sequence': event['sequence']}
        if event.get('kind') != 'source_reviews_updated': continue
        checks = {v.get('id'): v for v in recorded_slice(snap, 'local_checks') if v.get('origin_run_id') == run}
        for review in recorded_slice(memory, 'source_reviews'):
            agent = review.get('agent_id'); path = review.get('path'); version = review.get('version')
            if review.get('run_id') != run or path not in files or files[path] != version or not review.get('evidence_ids') or not all(valid_evidence(memory, eid, agent) for eid in recorded_slice(review, 'evidence_ids')): continue
            for rid, waiting in ready.items():
                if waiting['path'] == path and waiting['version'] == version and event['sequence'] >= waiting['sequence']:
                    dispositions[rid] = review.get('disposition')
            for check_id in recorded_slice(review, 'check_ids'):
                check = checks.get(check_id) or {}
                if check.get('agent_id') == agent and any(v.get('path') == path and v.get('version') == version for v in recorded_slice(check, 'sources')):
                    adopted.add(check_id); adopted_results[check_id] = check.get('result')
    current_bound = source_findings(state)
    d = item.get('delivery_review') or {}; check = item['metrics']['mechanisms']['local_checks']
    source_result = 'achieved' if current_bound else 'not_achieved'
    ready_result = 'not_applicable' if not ready else 'achieved' if len(dispositions) == len(ready) else 'not_achieved'
    local_result = 'achieved' if (check.get('tool_attempts') or 0) > 0 and (check.get('decidable') or 0) > 0 and adopted else 'not_achieved'
    outcome = 'achieved' if source_result == 'achieved' and d.get('undeclared') == 0 and ready_result != 'not_achieved' and local_result == 'achieved' else 'not_achieved'
    return {'result': outcome, 'semantic_result': 'not_run', 'opportunity_status': d.get('status'), 'opportunity_result': 'not_applicable' if d.get('status') == 'not_needed' else 'achieved' if d.get('status') == 'completed' else 'not_achieved',
            'source_disposition_counts': {k: d.get(k) for k in ('documents', 'addressed', 'not_relevant', 'unverified', 'undeclared', 'ready_to_resolve')},
            'source_bound_findings': {'result': source_result, 'ever_recorded_versions': len(bound), 'current': len(current_bound), 'engine_current_addressed_documents': d.get('addressed')},
            'ready_disposition_changes': {'result': ready_result, 'observed': len(ready), 'declared_dispositioned': len(dispositions), 'by_disposition': {kind: sum(v == kind for v in dispositions.values()) for kind in ('addressed', 'not_relevant', 'unverified')}, 'declaration_is_semantic_resolution': False},
            'local_checks': {'result': local_result, 'tool_attempts': check.get('tool_attempts'), 'checks_requested': check.get('checks_requested'), 'receipts': check.get('receipt_completed'), 'results': check.get('results'), 'declared_adopted': len(adopted), 'declared_adopted_results': {kind: sum(v == kind for v in adopted_results.values()) for kind in ('pass', 'fail', 'error', 'not_run')}, 'counterexample_correction_result': 'not_run'},
            'interpretation': '来源发现、就绪处置及检查声明分开；completed和addressed不等于语义通过，无待办不触发机会不算错误'}


def inspect_base_sample(sample_root, manifest, session, returncode, sample_id):
    tasks = list((sample_root / "workspace/.claw/tasks").glob("*/events.jsonl"))
    if len(tasks) != 1:
        raise EvalError("无法确定唯一任务记录")
    events = r.json_lines(tasks[0]); finals = [x["payload"] for x in events if x["kind"] == "finished"]
    if any(e.get("version") != 1 for e in events) or len(finals) != 1:
        raise EvalError("任务日志版本或终态无效")
    s = finals[0]
    if s.get('version') != (3 if sample_id.startswith('candidate') else 1):
        raise EvalError('普通交付实验仅允许各组已冻结的任务状态版本1/3')
    result = s.get("last_result", {}); spec = s.get("spec", {}); task_id = tasks[0].parent.name
    if s.get("session_id") != session or spec.get("id") != task_id or result.get("task_id") != task_id or not result.get("run_id"):
        raise EvalError("任务/会话/运行身份不匹配")
    if spec.get("goal") != manifest["goal"] or spec.get("mode") != "workspace" or spec.get("kind") != "change" or spec.get("analysis", False):
        raise EvalError("题面或执行模式与冻结配置不符")
    if spec.get("read_page_bytes", 8000) != 8000:
        raise EvalError("主实验读取档位不为8000")
    strict = r.strict_sample(sample_id)
    if bool(spec.get("report")) != strict:
        raise EvalError("实际报告开关与样本分组不符")
    if sample_id.startswith("candidate"):
        r.validate_candidate_spec(spec, manifest)
    if s.get("limits") != {"MaxModelCalls":60,"MaxToolCalls":120,"MaxTurns":30,"Timeout":600000000000}:
        raise EvalError("实际预算与冻结配置不符")
    for k in ["model_calls", "tool_calls", "turns"]:
        if s.get("usage", {}).get(k) != result.get("usage", {}).get(k):
            raise EvalError("状态与结果用量不一致")
    if r.EXIT.get(result.get("status")) != returncode:
        raise EvalError("持久终态与进程退出码不符")
    flows = list((sample_root / "observations").glob("*/events.jsonl"))
    if len(flows) != 1:
        raise EvalError("缺少唯一观测流")
    observed = r.json_lines(flows[0])
    if any(x.get("schema_version") != 1 or x["event_type"] == "observation_gap" or x.get("payload", {}).get("incomplete") for x in observed):
        raise EvalError("观测记录损坏或有缺口")
    returned = [x["payload"] for x in observed if x["event_type"] == "run_returned" and x.get("task_id") == task_id]
    if len(returned) != 1 or returned[0].get("result_run_id") != result["run_id"] or returned[0].get("status") != result["status"]:
        raise EvalError("入口返回与持久结果不一致")
    finalized = [x for x in observed if x["event_type"] == "run_finalized" and x.get("task_id") == task_id]
    return_events = [x for x in observed if x["event_type"] == "run_returned" and x.get("task_id") == task_id]
    if len(finalized) != 1 or finalized[0]["payload"].get("result_run_id") != result["run_id"] or finalized[0]["payload"].get("status") != result["status"] or finalized[0]["sequence"] >= return_events[0]["sequence"]:
        raise EvalError("缺少Run完整收尾后返回的生命周期证据")
    if any(x["payload"].get("model") != r.SETTINGS["model"] for x in observed if x["event_type"] == "model_started" and x.get("task_id") == task_id):
        raise EvalError("实际模型与冻结模型不一致")
    accounting = model_admission_accounting(s, observed, task_id)
    metrics = r.usage_metrics(observed, task_id, accounting["actual_model_requests"])
    metrics.update(accounting)
    metrics.update({k:result["usage"][k] for k in ["model_calls","tool_calls","turns"]})
    metrics["elapsed_seconds"] = result["usage"]["elapsed"] / 1e9
    if not 0 <= metrics["model_calls"] <= 60 or not 0 <= metrics["tool_calls"] <= 120 or not 0 <= metrics["turns"] <= 30:
        raise EvalError("实际调用突破冻结预算")
    wire = [x["payload"].get("request_encoded_bytes") for x in observed if x["event_type"] == "model_request_encoded" and x.get("task_id") == task_id]
    metrics["http_attempts"] = len(wire); metrics["encoded_request_bytes"] = sum(wire) if wire and all(type(n) is int and n >= 0 for n in wire) else None
    after = r.file_list(sample_root / "workspace"); r.write_json(sample_root / "files-after.json", after)
    sources = {x["path"]:x for x in after if x["path"] != OUTPUT}
    target = next((x for x in after if x["path"] == OUTPUT), None)
    scope = sorted(sources.values(),key=lambda x:x["path"]) == manifest["files"] and target is not None
    effects = result.get("delivery", {}).get("effects", [])
    receipt_ok = bool(target and result.get("delivery", {}).get("history_complete") is True and r.receipt_chain_valid(effects, OUTPUT, target["sha256"]))
    report_check = "not_run"
    if strict:
        rep, delivered = s.get("report", {}), result.get("report", {})
        expected = {"report_id":rep.get("report_id"),"output_version":target["sha256"] if target else None,"reference_result":rep.get("reference_result"),"semantic_result":"not_run"}
        report_check = "pass" if rep.get("status") == "published" and rep.get("reference_result") in {"pass","partial"} and rep.get("semantic_result") == "not_run" and all(delivered.get(k)==v for k,v in expected.items()) and delivered.get("target_path") == OUTPUT and delivered.get("origin_run_id") == result["run_id"] else "fail"
    independent_coverage = r.coverage(events, manifest["files"])
    if sample_id.startswith("candidate"):
        r.validate_candidate_result(result, independent_coverage, manifest)
    return {"task_id":task_id,"run_id":result["run_id"],"status":result["status"],"stop_code":result.get("stop_code"),"reason":result.get("reason"),"exit_code":returncode,"final_text":result.get("final_text", ""),
            "protocol_valid":True,"scope_pass":scope,"receipt_pass":receipt_ok,"coverage":independent_coverage,"pending_actions":len(s.get("pending",{})),"metrics":metrics,"report_check":report_check,
            "evidence":{"task_events":str(tasks[0].relative_to(sample_root)),"observations":str(flows[0].relative_to(sample_root)),"artifact":"workspace/"+OUTPUT}}


def validate_delivery_review_outcome(state, item):
    summary = state.get('last_result', {}).get('delivery_review')
    ledger = state.get('delivery_review')
    if not isinstance(summary, dict) or summary.get('status') not in {'not_run', 'not_needed', 'admitted', 'completed', 'failed', 'outcome_unknown'} or summary.get('version') != 1 or type(summary.get('admission_count')) is not int or not 0 <= summary['admission_count'] <= 1:
        raise EvalError('候选缺少有效交付前核对摘要或超出一次准入')
    if not isinstance(ledger, dict) or ledger.get('version') != 1 or ledger.get('admission_count') != summary['admission_count'] or ledger.get('status') != summary['status']:
        raise EvalError('交付前核对状态与返回摘要不一致')
    if summary.get('origin_run_id') != ledger.get('origin_run_id'):
        raise EvalError('交付前核对来源运行身份不匹配')
    stage_calls = item.get('metrics', {}).get('phases', {}).get('delivery_review', {}).get('model_calls')
    if summary['admission_count'] and summary.get('origin_run_id') != item.get('run_id'):
        raise EvalError('新会话核对票据不属于本次Run')
    if stage_calls is not None and (stage_calls > summary['admission_count'] or summary['status'] == 'completed' and summary['admission_count'] == 1 and stage_calls != 1):
        raise EvalError('核对准入、完成状态与实际模型阶段不匹配')
    item['delivery_review_mechanism'] = {'status': summary.get('status'), 'admission_count': summary['admission_count'],
                                         'admitted': summary['admission_count'] == 1, 'stage_model_calls': stage_calls,
                                         'opportunity_completed': summary['status'] == 'completed' and stage_calls == 1,
                                         'semantic_result': 'not_run', 'internal_complete_is_semantic_pass': False}


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    item = inspect_base_sample(sample_root, manifest, session, returncode, sample_id)
    events = r.json_lines(sample_root / item['evidence']['task_events'])
    state = next(e['payload'] for e in events if e['kind'] == 'finished')
    spec, candidate = state['spec'], sample_id.startswith('candidate')
    if state.get('version') != (3 if candidate else 1) or spec.get('read_context_version') != 4:
        raise EvalError('普通任务状态版本或组别读取策略不符')
    if spec.get('read_features') != FEATURES or spec.get('delivery_review') != ({'version': 1} if candidate else None):
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
    item['metrics']['phases'] = phase_metrics(observed, item['task_id'])
    item['metrics']['mechanisms']['all_model_requests'] = item['metrics']['mechanisms']['main']
    item['metrics']['mechanisms']['main'] = item['metrics']['phases']['main']
    item['metrics']['mechanisms']['delivery_review'] = item['metrics']['phases']['delivery_review']
    if sum(v['model_calls'] for v in item['metrics']['phases'].values()) != item['metrics']['actual_model_requests']:
        raise EvalError('分阶段模型调用与共享预算用量不一致')
    item['local_checks'] = state.get('last_result', {}).get('local_checks')
    item['delivery_review'] = state.get('last_result', {}).get('delivery_review') if candidate else None
    item['delivery_review_state'] = state.get('delivery_review') if candidate else None
    if candidate:
        validate_delivery_review_outcome(state, item)
    item['local_check_semantic_result'] = 'not_run'
    item['_source_files'] = manifest['files']
    item['delivery_review_mechanisms'] = delivery_mechanisms(events, state, item)
    item.pop('_source_files', None)
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
    required = {arm: ('-read-page-bytes', '-output-style', '-source-views', '-local-checks', '-claim-focus', '-delivery-review') for arm in ('baseline', 'candidate')}
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
        shutil.copytree(root / 'fixture', work); session = 'delivery-review-eval-' + uuid.uuid4().hex
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
    return protocol.digest(value)


def review_protocol(root, manifest):
    return protocol.Protocol(root, manifest, r)


def validate_review(root, manifest, sample_id, review):
    return review_protocol(root, manifest).final(review, sample_id)


def review_ok(review):
    return bool(review and protocol.review_ok(review))


def validate_review_draft(experiment, review_file):
    root, manifest = checked_manifest(experiment)
    raw = r.read_json(review_file)
    if 'reverse_check' in raw: raise EvalError('草稿不接受手填一致性结果')
    value = review_protocol(root, manifest).normalize(raw)
    return {'review_id': value['review_id'], 'derived_quality_checks': value['quality_checks'],
            'major_errors': value['major_errors'], 'major_omissions': value['major_omissions'],
            'major_unverified': value['major_unverified'], 'quality_result': 'pass' if review_ok(value) else 'fail',
            'semantic_sha256': protocol.digest(protocol.semantic_projection(value)), 'paid_calls': 0}


def seal_review(experiment, review_file, phase):
    root, manifest = checked_manifest(experiment)
    with experiment_lock(root):
        return review_protocol(root, manifest).seal(r.read_json(review_file), phase)



def review_schema():
    return {'schema_version': 2, 'issue_types': r.read_json(ISSUE_POLICY)['issue_types'],
            'derived_not_input': ['quality_checks', 'major_errors', 'major_error_ids', 'major_omissions', 'major_omission_ids', 'major_unverified', 'major_unverified_ids', 'reverse_check'],
            'claim_example': {'id': 'R1', 'kind': 'claim', 'claim': '产物中真实出现的原句', 'start_byte': 0, 'end_byte': 36,
                              'verdict': 'supported|incorrect|unverified', 'severity': 'none|ordinary|major', 'issue_types': ['source_attribution'], 'rubric_ids': [], 'reason': '逐项来源判断',
                              'evidence': [{'path': '冻结来源相对路径', 'source_sha256': '来源SHA256', 'start_line': 1, 'end_line': 2, 'quote': '实际源文摘录'}]},
            'omission_example': {'id': 'R2', 'kind': 'omission', 'rubric_id': '具体冻结量表ID', 'severity': '按量表冻结严重度', 'issue_types': ['omission'], 'reason': '范围中缺少何项', 'evidence': []},
            'note': '示例仅字段说明，不是待评分答案；真实字节范围/SHA/引文须替换并通过validate-review。语义正确由来源审查判断，不由结构校验证明。'}


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
        for name in ('rubric.json', 'oracle.json', 'anchors.json', 'issue_policy.json'): shutil.copyfile(root / 'harness' / name, target / name)
        public = []
        for rid, name, sha in entries:
            artifact = target / (rid + '.md'); artifact.write_bytes(artifact_bytes(root, name)); artifact.chmod(0o600)
            template = {'schema_version': 2, 'review_id': rid, 'artifact_sha256': sha, 'reviewer': '', 'method': '',
                        'scores': dict.fromkeys(SCORES, None), 'score_basis': {s: {'reason': '', 'finding_ids': []} for s in SCORES},
                        'findings': [], 'rubric_assessments': {item['id']: {'status': None, 'severity': None, 'reason': '', 'finding_ids': []} for item in r.read_json(root / 'harness/rubric.json')['rubrics']}}
            r.write_json(target / (rid + '.review.json'), template)
            public.append({'review_id': rid, 'artifact_sha256': sha, 'artifact': rid + '.md', 'template': rid + '.review.json'})
        r.write_json(target / 'index.json', {'schema_version': 1, 'goal': manifest['goal'], 'reviews': public,
                    'review_process': {'first_order': [entry[0] for entry in entries], 'reverse_order': [entry[0] for entry in reversed(entries)], 'same_reviewer': True, 'same_method': True},
                    'semantic_result': 'not_run', 'source_versions': [{'path': f['path'], 'sha256': f['sha256']} for f in manifest['files']],
                    'instructions': '按first_order逐份校验并封存完整初审，然后按reverse_order封存复查。issue_types按冻结分类填写；SPECIAL、major和consistent由程序派生，不手填。claim绑定真实UTF8区间；omission不造输出。source_audit_agent不是独立人工。', 'review_schema': 'review-schema.json'})
        r.write_json(target / 'review-schema.json', review_schema())
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
        review = validate_review(root, manifest, name, review)
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
    quality_improved = bool(cq == 'pass' and delta >= 0 and major_errors_delta <= 0 and major_omissions_delta <= 0 and (delta > 0 or major_errors_delta < 0 or major_omissions_delta < 0)) if reviewed else None
    bm, cm = baseline.get('metrics', {}), candidate.get('metrics', {})
    tokens_base, tokens_new = bm.get('total_tokens'), cm.get('total_tokens')
    known_calls = [s.get('metrics', {}).get('model_calls') for s in samples.values()]
    state = r.read_json(root / 'state.json') if (root / 'state.json').exists() else {}
    cost_comparable = bool(reviewed and quality == 'pass' and mechanical_ok(baseline) and mechanical_ok(candidate))
    raw_token_reduction = 1 - tokens_new / tokens_base if tokens_base and tokens_new is not None else None
    report = {'schema_version': 1, 'profile': PROFILE, 'experiment_id': manifest['experiment_id'], 'completed': complete,
              'passed': bool(complete and mechanical_ok(candidate) and quality == 'pass'),
              'mechanism_result': candidate.get('delivery_review_mechanisms', {}).get('result', 'not_run'),
              'candidate_delivery_result': ('pass' if mechanical_ok(candidate) else 'fail') if candidate else 'not_run',
              'candidate_quality_result': cq, 'quality_result': quality, 'paired_quality_delta_result': 'pass' if reviewed else 'not_run',
              'paired_quality_delta': delta, 'quality_improved': quality_improved, 'quality_comparison_available': bool(reviewed), 'paired_review_protocol': 'pass' if paired_protocol else 'not_run',
              'major_error_delta': major_errors_delta, 'major_omission_delta': major_omissions_delta,
              'local_improvements': [key for key in baseline['review']['rubric_assessments'] if baseline['review']['rubric_assessments'][key]['status'] != 'supported' and candidate['review']['rubric_assessments'][key]['status'] == 'supported'] if reviewed else None,
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
    for name in ('run-one', 'export-review', 'import-review', 'review', 'summarize', 'validate-review', 'seal-review'):
        p = sub.add_parser(name); p.add_argument('--experiment', required=True)
        if name == 'export-review': p.add_argument('--output', required=True)
        if name in ('import-review', 'review', 'validate-review', 'seal-review'): p.add_argument('--review-file', required=True)
        if name == 'seal-review': p.add_argument('--phase', choices=('first', 'reverse'), required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == 'selfcheck': return selfcheck()
        if args.command == 'init':
            result = init_experiment(args.output, args.baseline_bin, args.candidate_bin, args.source_workspace,
                                     args.project_dotenv, args.baseline_source_manifest, args.candidate_source_manifest)
        elif args.command == 'run-one': result = run_one(args.experiment)
        elif args.command == 'export-review': result = export_review(args.experiment, args.output)
        elif args.command == 'validate-review': result = validate_review_draft(args.experiment, args.review_file)
        elif args.command == 'seal-review': result = seal_review(args.experiment, args.review_file, args.phase)
        elif args.command in ('import-review', 'review'): result = record_review(args.experiment, args.review_file)
        else: result = summarize(args.experiment)
        print(json.dumps(result, ensure_ascii=False, indent=2)); return 0
    except (EvalError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print('评测停止：' + str(error), file=sys.stderr); return 2


if __name__ == '__main__': raise SystemExit(main())
