"""隔离假CLI验证profile控制面；不启动真实Provider，不给测试评分冒充真实质量。"""
import copy
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('work_notes_profile', HERE / 'run.py')
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
spec = importlib.util.spec_from_file_location('work_notes_fake_source', r.REPO / 'scripts/write_efficiency_eval/test_run.py')
old = importlib.util.module_from_spec(spec); spec.loader.exec_module(old)
c = old.c
FAKE = old.FAKE.replace("mode=os.environ.get('WRITE_EVAL_FAKE','')", "mode=pathlib.Path(__file__).with_name('mode').read_text() if pathlib.Path(__file__).with_name('mode').exists() else ''")
FAKE = FAKE.replace("'plan':False", "'plan':False,'read_context_version':1")
FAKE = FAKE.replace("['read_context_version']=1", "['read_context_version']=2")


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.root = Path(self.tmp.name)
        self.binary = self.root / 'fake'; self.binary.write_text(FAKE); self.binary.chmod(0o700)
        self.exp = self.root / 'experiment'; self.dotenv = self.root / 'project.env'
        self.dotenv.write_text('DEEPSEEK_API_KEY=unit-only-not-a-real-key\n')
        self.patches = [mock.patch.object(r.r, 'preflight', lambda path: None),
                        mock.patch.object(r.r, 'run_checker', lambda root, sample, m: c.inspect(sample / 'workspace', m['files'])),
                        mock.patch.object(r, 'dotenv_snapshot', lambda path: self.load_dotenv(self.dotenv))]
        self.load_dotenv = r.dotenv_snapshot
        for patch in self.patches: patch.start()
    def tearDown(self):
        for patch in reversed(self.patches): patch.stop()
        self.tmp.cleanup()
    def init(self): return r.init_experiment(self.exp, self.binary, self.binary)
    def mode(self, name): (self.exp / 'binaries/mode').write_text(name)
    def run_pair(self):
        self.init(); return r.run_one(self.exp), r.run_one(self.exp)
    def review_data(self, name):
        if not (self.exp / 'review-map.json').exists(): r.export_review(self.exp, self.root / 'blind')
        mapping = r.r.read_json(self.exp / 'review-map.json')
        rid, binding = next((rid, b) for rid, b in mapping.items() if b['sample_id'] == name)
        artifact = r.artifact_bytes(self.exp, name); text = '# 优化建议'; start = artifact.index(text.encode())
        source = r.r.read_json(self.exp / 'manifest.json')['files'][0]
        line = (self.exp / 'fixture' / source['path']).read_text().splitlines()[0]
        return dict(review_id=rid, artifact_sha256=binding['artifact_sha256'], reviewer='unit-test-only-no-real-quality', method='source_audit_agent',
                    scores=dict.fromkeys(r.r.SCORES, 2), score_basis={score:dict(reason='单元协议夹具，不是实际质量成绩',finding_ids=['f1']) for score in r.r.SCORES}, major_errors=0,major_error_ids=[],
                    findings=[dict(id='f1',kind='claim', claim=text, start_byte=start, end_byte=start+len(text.encode()), verdict='supported', evidence=[dict(path=source['path'], source_sha256=source['sha256'], start_line=1, end_line=1, quote=line)])])
    def review(self, name, data=None):
        path = self.root / 'review.json'; r.r.write_json(path, data or self.review_data(name)); return r.record_review(self.exp, path)
    def test_free_goal_does_not_leak_source_paths_or_count_and_budget(self):
        manifest = self.init()
        for file in manifest['files']: self.assertNotIn(file['path'], manifest['goal'])
        self.assertNotIn('15', manifest['goal']); self.assertEqual(manifest['max_model_admissions'], 360)
        self.assertEqual(len(manifest['files']), 15); self.assertEqual(r.r.ORDER[-1], 'candidate-ordinary-3')
        self.assertEqual(r.r.candidate_arguments(manifest), ['-read-page-bytes', '8000'])
        self.assertEqual((self.exp / 'manifest.json').stat().st_mode & 0o777, 0o600)
    def test_whole_order_review_gate_and_final_medians(self):
        baseline, candidate = self.run_pair()
        self.assertTrue(r.r.mechanical_ok(baseline)); self.assertTrue(r.r.candidate_gate(candidate))
        self.assertIsNone(candidate['metrics']['cost'])
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
        self.assertEqual(r.summarize(self.exp)['quality_result'], 'not_run')
        self.review(r.r.ORDER[0]); self.review(r.r.ORDER[1])
        for index, name in enumerate(r.r.ORDER[2:]):
            item = r.run_one(self.exp); self.assertEqual(item['sample_id'], name)
            r.export_review(self.exp, self.root / ('blind-%d' % index)); self.review(name)
        result = r.summarize(self.exp)
        self.assertTrue(result['passed'], result); self.assertEqual(result['groups']['ordinary']['candidate']['samples'], 3)
        self.assertIsNone(result['groups']['ordinary']['candidate']['metrics']['cost_median'])
        (self.exp / 'state.json').unlink(); self.assertTrue(r.summarize(self.exp)['passed'])
    def test_blind_export_has_no_arm_metrics_or_internal_map(self):
        self.run_pair(); target = self.root / 'blind'; r.export_review(self.exp, target)
        exported = json.loads((target / 'index.json').read_text())
        self.assertEqual(len(exported['reviews']), 2)
        for forbidden in ['baseline', 'candidate', 'model_calls', 'sample_id', 'binary_sha256']:
            self.assertNotIn(forbidden, (target / 'index.json').read_text())
        self.assertFalse((target / 'review-map.json').exists())
        self.assertTrue((target / 'sources').is_dir())
    def test_invalid_review_bindings_and_identity_are_rejected(self):
        self.run_pair(); good = self.review_data(r.r.ORDER[0])
        for change in ['artifact', 'span', 'claim', 'source', 'quote', 'identity', 'method', 'major', 'fake_omission', 'basis_string', 'basis_id', 'major_unbound', 'major_supported']:
            data = copy.deepcopy(good); finding = data['findings'][0]
            if change == 'artifact': data['artifact_sha256'] = '0'*64
            elif change == 'span': finding['start_byte'] += 1
            elif change == 'claim': finding['claim'] = '源文本不存在的结论'
            elif change == 'source': finding['evidence'][0]['source_sha256'] = '0'*64
            elif change == 'quote': finding['evidence'][0]['quote'] = '不存在的原文'
            elif change == 'identity': data['reviewer'] = ''
            elif change == 'method': data['method'] = 'independent_agent'
            elif change == 'major': data['major_errors'] = False
            elif change == 'basis_string': data['score_basis']['coverage'] = '无证据的空泛断言'
            elif change == 'basis_id': data['score_basis']['coverage']['finding_ids'] = ['invented']
            elif change == 'major_unbound': data.update(major_errors=1,major_error_ids=[])
            elif change == 'major_supported': data.update(major_errors=1,major_error_ids=['f1'])
            else: finding.update(kind='omission', rubric_id='numeric_discrepancy', reason='遗漏矛盾')
            with self.subTest(change=change), self.assertRaises(r.EvalError): self.review(r.r.ORDER[0], data)
        self.review(r.r.ORDER[0], good)
        with self.assertRaises(r.EvalError): self.review(r.r.ORDER[0], good)
    def test_rubric_omission_has_no_fabricated_span(self):
        self.run_pair(); data = self.review_data(r.r.ORDER[0])
        oracle = r.r.read_json(self.exp / 'harness/oracle.json'); source = next(s for s in oracle['sources'] if s['id'] == 'quality')
        evidence = next(e for e in source['evidence'] if e['id'] == 'quality_reported_numbers')
        ref = dict(path=source['path'], source_sha256=source['sha256'], start_line=evidence['start_line'], end_line=evidence['end_line'], quote=evidence['quote'])
        data['findings'] = [dict(id='f1',kind='omission', rubric_id='numeric_discrepancy', reason='全文未指出原文数值与代码推导不一致', evidence=[ref])]
        self.assertEqual(self.review(r.r.ORDER[0], data)['findings'][0]['kind'], 'omission')
    def test_env_snapshot_is_runtime_only_isolated_and_drift_stops(self):
        self.init()
        with mock.patch.dict(os.environ, {'OPENAI_API_KEY': 'never-copy', 'CLAW_REQUEST_DIAGNOSTICS': 'on_error', 'CLAW_REQUEST_DIAGNOSTICS_DIR': '/unrelated'}):
            with r.runtime_environment(self.exp, {'DEEPSEEK_API_KEY': 'unit'}, r.r.ORDER[0]):
                self.assertNotIn('OPENAI_API_KEY', os.environ); self.assertEqual(os.environ['CLAW_REQUEST_DIAGNOSTICS'], 'metadata')
                self.assertIn('/samples/baseline-ordinary-1/diagnostics', os.environ['CLAW_REQUEST_DIAGNOSTICS_DIR'])
                self.assertIn('/agent-runtime/baseline-ordinary-1/home', os.environ['HOME'])
                self.assertIn('/agent-runtime/baseline-ordinary-1/tmp', os.environ['TMPDIR'])
            r.run_one(self.exp)
        snapshot = (self.exp / 'runtime-environment.json').read_text()
        self.assertNotIn('unit-only-not-a-real-key', snapshot)
        self.dotenv.write_text('DEEPSEEK_API_KEY=changed-unit-secret\n')
        with mock.patch.object(r.r.subprocess, 'Popen') as popen, self.assertRaises(r.EvalError):
            r.run_one(self.exp)
        popen.assert_not_called(); self.assertTrue(r.r.read_json(self.exp / 'state.json')['stopped'])
    def test_freeze_resume_and_archive_history_are_checked(self):
        self.run_pair()
        state = r.r.read_json(self.exp / 'state.json'); state['samples'][r.r.ORDER[0]]['metrics']['total_tokens'] = 0; r.r.write_json(self.exp / 'state.json', state)
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
        # 汇总从原样本重建，忽略可重建索引。
        self.assertGreater(r.summarize(self.exp)['samples'][r.r.ORDER[0]]['metrics']['total_tokens'], 0)
        frozen = self.exp / 'harness/helpers.py'; frozen.write_text(frozen.read_text() + '\n# tamper\n')
        with self.assertRaises(r.EvalError): r.summarize(self.exp)
    def test_frozen_runner_rebuilds_history_without_current_source_imports(self):
        self.run_pair()
        command = [sys.executable, str(self.exp / 'harness/run.py'), 'summarize', '--experiment', str(self.exp)]
        process = subprocess.run(command, cwd=self.root, capture_output=True, text=True, check=True)
        report = json.loads(process.stdout)
        self.assertEqual(len(report['samples']), 2)
        self.assertEqual(report['quality_result'], 'not_run')
        source = self.exp / 'fixture' / r.r.read_json(self.exp / 'manifest.json')['files'][0]['path']
        source.write_bytes(source.read_bytes() + b'\n')
        with self.assertRaises(r.EvalError): r.summarize(self.exp)
    def test_first_pair_major_quality_coverage_or_cost_gate(self):
        self.run_pair(); baseline = self.review_data(r.r.ORDER[0]); candidate = self.review_data(r.r.ORDER[1])
        self.review(r.r.ORDER[0], baseline); candidate.update(major_errors=1,major_error_ids=['f1']); candidate['findings'][0]['verdict']='incorrect'; self.review(r.r.ORDER[1], candidate)
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
    def test_candidate_first_pair_expensive_and_infra_no_retry(self):
        self.init(); r.run_one(self.exp); self.mode('expensive'); item = r.run_one(self.exp)
        self.assertEqual(item['metrics']['model_calls'], 13)
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
        self.exp = self.root / 'infra'; self.init(); self.mode('infra'); item = r.run_one(self.exp)
        self.assertIn('infrastructure_error', item); self.assertIsNone(item['metrics']['total_tokens'])
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
        self.assertNotIn('unit-only-not-a-real-key', (self.exp / 'samples' / r.r.ORDER[0] / 'stdout.log').read_text())
    def test_derived_metrics_use_dynamic_count(self):
        events = []
        def emit(kind, payload, call='m1'): events.append(dict(task_id='t', event_type=kind, payload=payload, model_call_id=call))
        emit('model_started', dict(folded_source_bytes=120, notes_metadata_removed_bytes=31))
        emit('model_finished', dict(duration_ms=1500)); emit('tool_attempted', dict(tool='list_dir')); emit('tool_attempted', dict(tool='find_files'))
        emit('tool_attempted', dict(tool='read_file')); emit('work_notes_processed', dict(notes_accepted=2, notes_carrier_bytes=44))
        emit('work_notes_processed', dict(notes_accepted=1, error_class='duplicate'))
        emit('work_notes_processed', dict(notes_rejected=1));emit('work_notes_processed',dict(notes_missing=1))
        emit('tool_attempted', dict(tool='write_file'))
        metrics = r.derive_metrics(events, [], 't', True, 37)
        self.assertEqual(metrics['source_file_count'], 37); self.assertEqual(metrics['model_calls_before_first_read'], 1)
        self.assertEqual(metrics['list_dir_before_find'], 1); self.assertEqual(metrics['accepted_notes'], 2)
        self.assertEqual(metrics['publish_generation_seconds'], 1.5); self.assertEqual(metrics['folded_source_bytes'], 120)
        self.assertEqual(metrics['duplicate_carriers'], 1); self.assertEqual(metrics['accepted_carriers'],3)
        self.assertEqual(metrics['carrier_acceptance_denominator'],4);self.assertEqual(metrics['carrier_acceptance_rate'],.5)
        self.assertIsNone(metrics['notes_acceptance_denominator']); self.assertIsNone(metrics['notes_acceptance_rate'])
        self.assertEqual(metrics['missing_notes'],1)
    def test_candidate_later_pair_does_not_reuse_first_pair_gate(self):
        item = dict(sample_id='candidate-ordinary-2', metrics=dict(model_calls=13, tool_calls=45))
        with mock.patch.object(r.r, 'mechanical_ok', return_value=True): self.assertTrue(r.r.candidate_gate(item))


@unittest.skipUnless(sys.platform == 'darwin' and Path('/usr/bin/sandbox-exec').is_file(), '需要真实macOS沙箱')
class FrozenSandboxTests(unittest.TestCase):
    def test_frozen_checker_runs_without_credentials_network_or_writes(self):
        parent = Path.home() / '.claw'; parent.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='work-notes-offline-', dir=parent) as tmp:
            experiment = Path(tmp) / 'experiment'
            r.init_experiment(experiment, '/usr/bin/true', '/usr/bin/true')
            r.r.preflight(experiment)
            proof = r.r.read_json(experiment / 'checker-runtime/isolation-preflight.json')
            self.assertEqual(proof['returncode'], 0)
            self.assertEqual(proof['credentials'], 'absent')
    def test_real_preflight_inside_exact_agent_runtime_environment(self):
        # 复现run_one的真实组合：先重定向HOME，再调用未mock的preflight和checker。
        parent = r.r.account_home() / '.claw'; parent.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='work-notes-runtime-isolation-', dir=parent) as tmp:
            experiment = Path(tmp) / 'experiment'
            r.init_experiment(experiment, '/usr/bin/true', '/usr/bin/true')
            original = {name:r.r.digest(experiment / name) for name in ('manifest.json','state.json')}
            with r.runtime_environment(experiment, {}, 'baseline-ordinary-1'):
                self.assertNotEqual(Path.home(), r.r.account_home())
                r.r.preflight(experiment)
                profile = r.r.checker_context(experiment)[2]
                self.assertIn('(deny file-read* (subpath '+json.dumps(str(r.r.account_home()))+')', profile)
            self.assertEqual(original, {name:r.r.digest(experiment / name) for name in original})
            self.assertEqual(r.r.read_json(experiment / 'state.json')['samples'], {})
            proof = r.r.read_json(experiment / 'checker-runtime/isolation-preflight.json')
            self.assertEqual(proof['returncode'], 0)
            self.assertEqual(proof['ancestor_data'], 'denied')
    def test_forged_home_cannot_change_profile_or_sentinel_protection(self):
        # HOME外部根目录也验证真实HOME的数据拒绝与仅metadata放行。
        with tempfile.TemporaryDirectory(prefix='work-notes-external-isolation-') as tmp:
            experiment = Path(tmp) / 'experiment'
            r.init_experiment(experiment, '/usr/bin/true', '/usr/bin/true')
            actual_home = r.r.account_home()
            original_profile = r.r.checker_context(experiment)[2]
            with mock.patch.dict(os.environ, {'HOME':str(experiment / 'forged-home')}):
                self.assertEqual(r.r.account_home(), actual_home)
                self.assertEqual(r.r.checker_context(experiment)[2], original_profile)
                r.r.preflight(experiment)
            self.assertEqual(r.r.read_json(experiment / 'checker-runtime/isolation-preflight.json')['returncode'], 0)


if __name__ == '__main__': unittest.main()
