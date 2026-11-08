#!/usr/bin/env python3
"""普通来源复用 v1 独立双样本：仅 run-one 启动真实CLI；质量优先，不自动重试。"""
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
DEFAULT_WORKSPACE = HERE.parent / 'fixture' if (HERE / 'common.py').is_file() else REPO / 'eval/fixtures/source_reuse_v1/workspace'
COMMON = HERE / 'common.py' if (HERE / 'common.py').is_file() else REPO / 'scripts/write_efficiency_eval/run.py'
_spec = importlib.util.spec_from_file_location('source_reuse_common', COMMON)
r = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(r)
EvalError = r.EvalError
PROFILE = 'source_reuse_v1'
ORDER = ['baseline-ordinary-1', 'candidate-ordinary-1']
OUTPUT = r.OUTPUT
SCORES = tuple(r.SCORES)
SPECIAL = ('proposed_repairs', 'subject_identity', 'important_omissions')
SETTINGS = dict(r.SETTINGS)
ORACLE = HERE / 'oracle.json' if (HERE / 'oracle.json').is_file() else REPO / 'eval/fixtures/source_reuse_v1/semantic_oracle.json'
RUBRIC = HERE / 'rubric.json' if (HERE / 'rubric.json').is_file() else REPO / 'eval/fixtures/source_reuse_v1/rubric.json'
CASES = HERE / 'cases.json' if (HERE / 'cases.json').is_file() else REPO / 'eval/fixtures/source_reuse_v1/cases.json'
FIXTURE_MANIFEST = HERE / 'fixture_manifest.json' if (HERE / 'fixture_manifest.json').is_file() else REPO / 'eval/fixtures/source_reuse_v1/manifest.json'
TIMESTAMP_CASES = HERE / 'timestamp_cases.json' if (HERE / 'timestamp_cases.json').is_file() else REPO / 'eval/fixtures/source_reuse_v1/timestamp_cases.json'
GOAL = ('完整阅读当前工作区各资料包中的全部Markdown，仅在当前目录新建优化汇总.md，不修改原始资料。'
        '精简概览资料并指出明显矛盾，核对被引用的数值、公式、默认值及版本条件。'
        '重要结论须就地引用完整相对文件路径及行号；跨文档判断同时给出两端依据。'
        '区分原文转述、静态推导、建议及实际运行；未执行的内容保留待实测边界。'
        '一项问题只展开一次，不重复粘贴长篇原文或示例代码。')
LIMITATIONS = ('同任务同模型一对普通任务的小样本；33f仅历史症状，不替代同批baseline；'
               '内部局部核对、来源复用和引用位置检查不替代外部语义审查；质量优先，'
               '12/40和Token下降30%仅次级观察目标；未知费用null；不执行课程代码。')
r.ORDER, r.PROFILE_VERSION = list(ORDER), 5
r.build_goal = lambda files: GOAL
r.strict_sample = lambda name: False
r.harness_inputs = lambda: harness_inputs()


def harness_inputs():
    return {'run.py': Path(__file__), 'common.py': COMMON, 'helpers.py': r.HELPERS,
            'checker.py': HERE / 'checker.py', 'test_run.py': HERE / 'test_run.py', 'README.md': HERE / 'README.md', 'finite_cases.py': HERE / 'finite_cases.py', 'timestamp_cases.json': TIMESTAMP_CASES, 'fixture_manifest.json': FIXTURE_MANIFEST, 'oracle.json': ORACLE, 'rubric.json': RUBRIC, 'cases.json': CASES,
            'quality_oracle.py': HERE / 'quality_oracle.py' if (HERE / 'quality_oracle.py').is_file() else REPO / 'scripts/coverage_efficiency_eval/quality_oracle.py',
            'semantic_oracle.py': HERE / 'semantic_oracle.py' if (HERE / 'semantic_oracle.py').is_file() else REPO / 'scripts/write_efficiency_eval/semantic_oracle.py'}


def init_experiment(output, baseline_bin, candidate_bin, source_workspace=None, project_dotenv=None,
                    baseline_source_manifest=None, candidate_source_manifest=None):
    if not baseline_source_manifest or not candidate_source_manifest:
        raise EvalError('必须提供两组冻结源码清单；无源码归档不得启动本profile')
    root = Path(output).absolute()
    manifest = r.init_experiment(root, baseline_bin, candidate_bin, source_workspace or DEFAULT_WORKSPACE)
    if manifest['files'] != r.read_json(FIXTURE_MANIFEST).get('files'):
        raise EvalError('本profile必须使用已核对的完整冻结资料版本')
    manifest.update(profile=PROFILE, max_samples=2, max_model_admissions=120, limitations=LIMITATIONS,
                    credentials={'source': 'project_dotenv_runtime_only', 'snapshot': 'bind_first_run', 'persist_values': False},
                    project_dotenv=str(Path(project_dotenv or REPO / '.env').absolute()), source_manifests={}, policies={'baseline': 2, 'candidate': 3}, task_state_version=1, read_memory_storage_version=1)
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
    if manifest.get('policies') != {'baseline': 2, 'candidate': 3} or manifest.get('task_state_version') != 1 or manifest.get('read_memory_storage_version') != 1:
        raise EvalError('读取策略版本与普通任务状态契约变化')
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


def sample_arguments(manifest, candidate):
    # 两组均为普通入口；candidate策略由新二进制为新任务持久化，不注入严格模式。
    return ['-read-page-bytes', '8000', '-output-style', 'concise']


def validate_candidate_spec(spec, manifest):
    if spec.get('read_context_version') != 3:
        raise EvalError('候选未实际启用普通读取策略v3')


r.validate_candidate_spec = validate_candidate_spec


def physical_coverage(events, files):
    # source view只重用已有读据，不能为源文件补造物理读取；同源区间仍由helper取并集。
    accepted = [e for e in events if e.get('kind') == 'action_result'
                and e.get('payload', {}).get('last_action', {}).get('tool') in {'read_file', 'read_lines'}]
    return r.h.coverage(accepted, files)


r.coverage = physical_coverage


def observation_metrics(events, task_id, candidate):
    """仅按白名单元数据统计；旧二进制没有新事件时是未知，不能补零。"""
    calls, views, checks, note_responses = {}, {}, {}, {}
    for index, event in enumerate(events, 1):
        if event.get('task_id') != task_id: continue
        kind, payload, identity = event.get('event_type'), event.get('payload', {}), event.get('model_call_id')
        if kind in {'model_started', 'model_finished', 'model_usage', 'model_request_encoded'}:
            if not identity: raise EvalError('模型事件缺少model_call_id')
            call = calls.setdefault(identity, {})
            if kind == 'model_request_encoded':
                call.setdefault('encoded_attempts', []).append(payload); continue
            if kind in call and call[kind] != payload: raise EvalError('相同模型身份出现冲突用量')
            call[kind] = payload
            if kind == 'model_started' and payload.get('stage') in {'quality_review', 'quality_revision'}:
                raise EvalError('普通实验不得混入质量复核或修订调用')
        elif kind == 'work_notes_response':
            if not identity: raise EvalError('工作笔记响应缺少model_call_id')
            if identity in note_responses and note_responses[identity] != payload: raise EvalError('笔记响应身份冲突')
            note_responses[identity] = payload
        elif kind in {'source_view_created', 'source_view_historical'}:
            view = payload.get('view_evidence_id')
            if kind == 'source_view_historical' and not view:
                view = 'historical:' + str(event.get('action_id') or event.get('sequence') or index)
            if not view: raise EvalError('当前来源视图事件缺少view_evidence_id')
            if view in views and views[view] != payload: raise EvalError('来源视图身份冲突')
            views[view] = payload
        elif kind == 'local_check_completed':
            check = payload.get('check_id')
            if not check: raise EvalError('局部检查缺少check_id')
            if check in checks and checks[check] != payload: raise EvalError('局部检查身份冲突')
            checks[check] = payload
    selected = [c for c in calls.values() if 'model_started' in c]
    ins = [c.get('model_usage', {}).get('input_tokens') for c in selected]
    outs = [c.get('model_usage', {}).get('output_tokens') for c in selected]
    costs = [c.get('model_usage', {}).get('cost') for c in selected]
    wire = [p.get('request_encoded_bytes') for c in selected for p in c.get('encoded_attempts', [])]
    unknown = sum(a is None or b is None for a, b in zip(ins, outs))
    main = {'model_calls': len(selected), 'input_tokens': None if unknown else sum(ins),
            'output_tokens': None if unknown else sum(outs), 'unknown_usage_calls': unknown,
            'known_input_tokens': sum(x for x in ins if x is not None), 'known_output_tokens': sum(x for x in outs if x is not None),
            'cost': sum(costs) if all(x is not None for x in costs) else None,
            'known_cost': sum(x for x in costs if x is not None), 'unknown_cost_calls': costs.count(None),
            'http_attempts': len(wire), 'encoded_request_bytes': sum(wire) if selected and all(c.get('encoded_attempts') for c in selected) and all(type(x) is int and x >= 0 for x in wire) else None,
            'elapsed_seconds': sum(c.get('model_finished', {}).get('duration_ms', 0) for c in selected) / 1000}
    material_keys = ('source_bytes', 'analysis_bytes', 'evidence_bytes', 'action_bytes', 'schema_bytes', 'folded_source_bytes', 'notes_metadata_removed_bytes', 'notes_metadata_retained_bytes')
    supported = bool(selected) and all(c['model_started'].get('read_context_version') in (2, 3) for c in selected)
    totals = {}
    for key in material_keys:
        values = [c['model_started'].get(key, 0) for c in selected]
        if any(type(value) is not int or value < 0 for value in values): raise EvalError('材料字节指标无效')
        totals[key] = sum(values) if supported else None
    opportunities = sum(bool(n.get('notes_opportunity')) for n in note_responses.values())
    accepted = sum(bool(n.get('notes_opportunity')) and bool(n.get('notes_response_accepted')) for n in note_responses.values())
    notes = {'observed_responses': len(note_responses), 'opportunities': opportunities if note_responses else None,
             'accepted_opportunities': accepted if note_responses else None,
             'response_adoption_rate': accepted / opportunities if opportunities else None,
             'processed_sources': sum(n.get('notes_processed_sources', 0) for n in note_responses.values()) if note_responses else None,
             'basis': 'deduplicated work_notes_response.model_call_id; not per-tool missing counts'}
    current = [v for v in views.values() if v.get('source_kind') == 'current']
    historical = [v for v in views.values() if v.get('source_kind') == 'historical']
    return {'main': main, 'review': 'not_applicable', 'revision': 'not_applicable',
            'source_views': {'instrumented_policy': candidate, 'created': len(current) if candidate or views else None,
                             'historical': len(historical) if candidate or views else None,
                             'created_content_bytes': sum(v.get('content_bytes', 0) for v in current) if candidate or views else None,
                             'coverage_policy': 'physical_read_receipts_only; views never add coverage'},
            'local_checks': {'instrumented_policy': candidate, 'completed': len(checks) if candidate or checks else None,
                             'results': {key: sum(c.get('check_result') == key for c in checks.values()) for key in ('pass', 'fail', 'error', 'not_run')} if candidate or checks else None,
                             'semantic_result': 'not_run'}, 'materials': totals, 'work_notes': notes}


def inspect_sample(sample_root, manifest, session, returncode, sample_id):
    item = r.inspect_sample(sample_root, manifest, session, returncode, sample_id)
    events = r.json_lines(sample_root / item['evidence']['task_events'])
    state = next(e['payload'] for e in events if e['kind'] == 'finished')
    spec, candidate = state['spec'], sample_id.startswith('candidate')
    if state.get('version') != 1 or spec.get('read_context_version') != (3 if candidate else 2):
        raise EvalError('普通任务状态版本或组别读取策略不符')
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
    required = {'baseline': ('-read-page-bytes', '-output-style'),
                'candidate': ('-read-page-bytes', '-output-style')}
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
    return review


def review_ok(review):
    return bool(review and sum(review['scores'].values()) >= 8 and review['major_errors'] == 0
                and review['major_omissions'] == 0 and all(c['verdict'] != 'incorrect' for c in review['quality_checks'].values()))


def export_review(experiment, output):
    root, manifest = checked_manifest(experiment, require_current=False)
    target = Path(output).absolute()
    if target == root or target.is_relative_to(root) or target.exists(): raise EvalError('匿名包须新建在实验目录之外')
    with experiment_lock(root):
        samples = check_history(root, manifest)
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
        target.mkdir(parents=True, mode=0o700); shutil.copytree(root / 'fixture', target / 'sources')
        for name in ('rubric.json', 'oracle.json'): shutil.copyfile(root / 'harness' / name, target / name)
        public = []
        for rid, name, sha in entries:
            artifact = target / (rid + '.md'); artifact.write_bytes(artifact_bytes(root, name)); artifact.chmod(0o600)
            template = {'review_id': rid, 'artifact_sha256': sha, 'reviewer': '', 'method': '',
                        'scores': dict.fromkeys(SCORES, None), 'score_basis': {s: {'reason': '', 'finding_ids': []} for s in SCORES},
                        'major_errors': None, 'major_error_ids': [], 'major_omissions': None, 'major_omission_ids': [], 'findings': [],
                        'quality_checks': {s: {'verdict': None, 'reason': '', 'finding_ids': []} for s in SPECIAL}}
            r.write_json(target / (rid + '.review.json'), template)
            public.append({'review_id': rid, 'artifact_sha256': sha, 'artifact': rid + '.md', 'template': rid + '.review.json'})
        r.write_json(target / 'index.json', {'schema_version': 1, 'goal': manifest['goal'], 'reviews': public,
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
    reviewed = bool(baseline.get('artifact_exists') and candidate.get('artifact_exists') and baseline.get('review') and candidate.get('review'))
    delta = sum(candidate['review']['scores'].values()) - sum(baseline['review']['scores'].values()) if reviewed else None
    cq = ('pass' if review_ok(candidate['review']) else 'fail') if candidate.get('review') else 'not_run'
    quality = ('pass' if cq == 'pass' and delta >= 0 else 'fail') if reviewed else 'not_run'
    quality_improved = delta > 0 if reviewed else None
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
              'paired_quality_delta': delta, 'quality_improved': quality_improved,
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
