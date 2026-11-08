"""控制面假CLI与有限反例回归；不调用真实Provider，不把单测当成质量成绩。"""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('quality_profile_test', Path(__file__).with_name('run.py'))
p = importlib.util.module_from_spec(spec); spec.loader.exec_module(p)
spec = importlib.util.spec_from_file_location('quality_checker_test', Path(__file__).with_name('checker.py'))
c = importlib.util.module_from_spec(spec); spec.loader.exec_module(c)
spec = importlib.util.spec_from_file_location('quality_cases_test', Path(__file__).with_name('finite_cases.py'))
f = importlib.util.module_from_spec(spec); spec.loader.exec_module(f)

FAKE = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,uuid
if '-h' in sys.argv:
 print('\n'.join(['-report','-read-scope','-require-full-read','-read-page-bytes','-output-style','-quality']));sys.exit(0)
args={};scopes=[];i=1
while i<len(sys.argv):
 k=sys.argv[i];i+=1
 if k in ('-quality','-require-full-read'):args[k]=True;continue
 v=sys.argv[i];i+=1;args[k]=v
 if k=='-read-scope':scopes.append(v)
work=pathlib.Path(args['-dir']);candidate=pathlib.Path(__file__).name=='candidate'
modefile=pathlib.Path(__file__).with_name('mode');mode=modefile.read_text() if modefile.exists() else ''
if mode=='infra':print(os.environ.get('DEEPSEEK_API_KEY',''));sys.exit(1)
task=uuid.uuid4().hex;run=uuid.uuid4().hex;folder=work/'.claw/tasks'/task;folder.mkdir(parents=True)
files=sorted(work.rglob('*.md'));content='# 优化建议\n版本V1/V2与默认score需同步迁移，JSON契约需统一，运行结果待实测。\n'*15
for file in files[:3]:content+='引用：'+file.relative_to(work).as_posix()+':1\n'
blocked=mode in ('blocked','bad-json','budget')
status='BUDGET_EXHAUSTED' if mode=='budget' else 'BLOCKED' if blocked else 'NEEDS_VERIFICATION'
stop='tool_limit' if mode=='budget' else 'quality_contract_invalid' if mode=='bad-json' else 'quality_not_ready' if blocked else 'needs_verification'
if not blocked:(work/'优化汇总.md').write_text(content)
output=hashlib.sha256(content.encode()).hexdigest();mc=13 if mode=='expensive' else 3 if candidate else 1
usage={'model_calls':mc,'tool_calls':len(files)+1,'turns':mc,'elapsed':1000000}
effect={'action_id':'write-action','tool':'write_report','status':'SUCCEEDED','effect':'CONFIRMED','file':{'path':'优化汇总.md','before_version':'missing','after_version':output}}
result={'task_id':task,'run_id':run,'status':status,'stop_code':stop,'reason':'协议单测','usage':usage,'delivery':{'history_complete':True,'effects':[] if blocked else [effect]}}
spec={'id':task,'goal':args['-prompt'],'kind':'change','mode':'workspace','plan':False,'read_context_version':2,'read_page_bytes':8000,'output_style':'concise','read_scope':{'version':1,'paths':scopes,'name_glob':'*','require_complete':True},'report':{'target_path':'优化汇总.md','create_only':True,'version':1,'format':'evidence_v1'}}
if candidate:spec['quality']={'version':1}
state={'version':2 if candidate else 1,'spec':spec,'session_id':args['-session'],'usage':usage,'pending':{},'last_result':result,'limits':{'MaxModelCalls':60,'MaxToolCalls':120,'MaxTurns':30,'Timeout':600000000000}}
if not blocked:
 state['report']={'status':'published','report_id':'report-fixture','output_version':output,'reference_result':'pass','semantic_result':'not_run'}
 result['report']=dict(state['report'],target_path='优化汇总.md',origin_run_id=run)
if candidate:
 state['quality']={'version':1,'draft_id':'unit-draft','draft_sha256':'a'*64,'status':'blocked' if blocked else 'published','check_result':'not_run' if blocked else 'pass','review_status':'completed','revision_status':'completed','origin_run_id':run,'source_versions':{file.relative_to(work).as_posix():hashlib.sha256(file.read_bytes()).hexdigest() for file in files}}
 result['quality']={key:state['quality'][key] for key in ('version','draft_id','status','check_result','review_status','revision_status')}
 if not blocked:
  frozen=uuid.uuid4().hex;state['quality'].update(frozen_artifact_id=frozen,frozen_sha256=output);(folder/'artifacts').mkdir();(folder/'artifacts'/frozen).write_text(content)
events=[]
for n,file in enumerate(files):
 data=file.read_bytes();end=len(data)-(1 if mode=='incomplete' and n==len(files)-1 else 0)
 read={'path':file.relative_to(work).as_posix(),'version':hashlib.sha256(data).hexdigest(),'byte_start':0,'byte_end':end,'total_bytes':len(data),'evidence_id':'E'+str(n),'action_id':'a'+str(n)}
 state=dict(state,last_action={'id':'a'+str(n),'tool':'read_file','result':{'status':'SUCCEEDED','read':read}})
 events.append({'version':1,'sequence':len(events)+1,'kind':'action_result','payload':state})
if not blocked:
 state['report']['checked']={'findings':[{'id':'F'+str(n),'kind':'fact','statement':'单元夹具引用目录','status':'evidence_valid','evidence':[{'path':file.relative_to(work).as_posix(),'version':hashlib.sha256(file.read_bytes()).hexdigest(),'evidence_id':'E'+str(n),'action_id':'a'+str(n),'status':'evidence_valid','byte_start':0,'byte_end':len(file.read_bytes().splitlines()[0]),'start_line':1,'end_line':1,'quote':file.read_text().splitlines()[0]}]} for n,file in enumerate(files[:3])]}
if mode=='scope':files[0].write_text('非法修改')
events.append({'version':1,'sequence':len(events)+1,'kind':'finished','payload':state})
(folder/'events.jsonl').write_text(''.join(json.dumps(e,ensure_ascii=False)+'\n' for e in events))
obs=pathlib.Path(os.environ['CLAW_OBSERVE_DIR'])/uuid.uuid4().hex;obs.mkdir(parents=True);observations=[]
def emit(kind,payload,call=''):
 observations.append({'schema_version':1,'sequence':len(observations)+1,'event_type':kind,'model_call_id':call,'task_id':task,'payload':payload})
for n in range(mc):
 call='m'+str(n);stage='quality_review' if candidate and n==1 else 'quality_revision' if candidate and n==2 else 'main'
 emit('model_started',{'model':args['-model'],'stage':stage},call);emit('model_request_encoded',{'request_encoded_bytes':500},call)
 emit('model_usage',{'input_tokens':1000,'output_tokens':50,'cost':None},call);emit('model_finished',{'duration_ms':10},call)
 emit('tool_attempted',{'tool':'write_report' if n==mc-1 else 'verify_findings'},call)
emit('run_finalized',{'result_run_id':run,'status':status});emit('run_returned',{'result_run_id':run,'status':status})
(obs/'events.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in observations));sys.exit(3 if mode=='budget' else 2)
'''


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.root = Path(self.temp.name)
        self.binary = self.root / 'fake'; self.binary.write_text(FAKE); self.binary.chmod(0o700)
        self.exp = self.root / 'experiment'; self.dotenv = self.root / '.env'; self.dotenv.write_text('DEEPSEEK_API_KEY=unit-not-paid-secret\n')
        self.patches = [mock.patch.object(p.r, 'preflight', lambda path: None),
                        mock.patch.object(p.r, 'run_checker', lambda root, sample, manifest: c.inspect(sample / 'workspace', manifest['files']))]
        for patch in self.patches: patch.start()
    def tearDown(self):
        for patch in reversed(self.patches): patch.stop()
        self.temp.cleanup()
    def init(self):
        source_manifest = self.root / 'source-manifest.json'
        p.r.write_json(source_manifest, [{'path': 'cmd/claw/main.go', 'sha256': p.r.digest(self.binary), 'bytes': self.binary.stat().st_size}])
        return p.init_experiment(self.exp, self.binary, self.binary, project_dotenv=self.dotenv, baseline_source_manifest=source_manifest, candidate_source_manifest=source_manifest)
    def mode(self, value): (self.exp / 'binaries/mode').write_text(value)
    def pair(self): self.init(); return p.run_one(self.exp), p.run_one(self.exp)
    def review_data(self, name):
        if not (self.exp / 'review-map.json').exists(): p.export_review(self.exp, self.root / 'blind')
        mapping = p.r.read_json(self.exp / 'review-map.json')
        if not any(value['sample_id'] == name for value in mapping.values()):
            p.export_review(self.exp, self.root / ('blind-' + name)); mapping = p.r.read_json(self.exp / 'review-map.json')
        rid, binding = next((rid, value) for rid, value in mapping.items() if value['sample_id'] == name)
        artifact = p.artifact_bytes(self.exp, name); claim = '# 优化建议'; start = artifact.index(claim.encode())
        source = p.r.read_json(self.exp / 'manifest.json')['files'][0]
        quote = (self.exp / 'fixture' / source['path']).read_text().splitlines()[0]
        return {'review_id': rid, 'artifact_sha256': binding['artifact_sha256'], 'reviewer': 'unit-protocol-fixture-only', 'method': 'source_audit_agent',
                'scores': dict.fromkeys(p.SCORES, 2), 'score_basis': {s: {'reason': '仅协议夹具，不是真实质量成绩', 'finding_ids': ['f1']} for s in p.SCORES},
                'major_errors': 0, 'major_error_ids': [], 'major_omissions': 0, 'major_omission_ids': [],
                'findings': [{'id': 'f1', 'kind': 'claim', 'claim': claim, 'start_byte': start, 'end_byte': start + len(claim.encode()), 'verdict': 'supported',
                              'evidence': [{'path': source['path'], 'source_sha256': source['sha256'], 'start_line': 1, 'end_line': 1, 'quote': quote}]}],
                'quality_checks': {key: {'verdict': 'not_applicable', 'reason': '单元控制面没有真实模型修改建议', 'finding_ids': []} for key in p.SPECIAL}}
    def review(self, name, data=None):
        path = self.root / 'review.json'; p.r.write_json(path, data or self.review_data(name)); return p.record_review(self.exp, path)

    def test_independent_order_both_strict_same_scope_and_frozen_offline_rebuild(self):
        manifest = self.init(); self.assertEqual(manifest['order'], p.ORDER); self.assertEqual(manifest['max_model_admissions'], 120)
        a, b = p.run_one(self.exp), p.run_one(self.exp)
        self.assertTrue(p.mechanical_ok(a), a); self.assertTrue(p.mechanical_ok(b), b)
        self.assertNotIn('-quality', a['command']); self.assertIn('-quality', b['command'])
        for sample in (a, b): self.assertIn('-report', sample['command']); self.assertIn('-require-full-read', sample['command'])
        self.assertEqual(p.summarize(self.exp)['quality_result'], 'not_run')
        self.review(p.ORDER[0]); self.review(p.ORDER[1]); report = p.summarize(self.exp)
        self.assertTrue(report['passed']); self.assertEqual(report['paired_quality_delta'], 0); self.assertFalse(report['quality_improved'])
        self.assertEqual(report['cost_comparison']['cost_gate'], 'not_applicable')
        self.assertIsNone(report['samples'][p.ORDER[0]]['metrics']['cost'])
        with self.assertRaises(p.EvalError): p.run_one(self.exp)
        (self.exp / 'state.json').unlink()
        process = subprocess.run([sys.executable, str(self.exp / 'harness/run.py'), 'summarize', '--experiment', str(self.exp)], cwd=self.root, capture_output=True, text=True, check=True)
        self.assertTrue(json.loads(process.stdout)['passed'])

    def test_business_failure_and_missing_artifact_do_not_stop_candidate_or_score_zero(self):
        for mode in ('blocked', 'bad-json', 'budget'):
            self.exp = self.root / mode; self.init(); self.mode(mode); first = p.run_one(self.exp)
            self.assertFalse(first['artifact_exists']); self.assertTrue(first['scope_pass']); self.assertFalse(p.r.read_json(self.exp / 'state.json')['stopped'])
            self.assertEqual(first['checker']['scope_result'], 'pass')
            self.mode(''); second = p.run_one(self.exp); self.assertTrue(second['artifact_exists'])
            summary = p.summarize(self.exp); self.assertEqual(summary['paired_quality_delta_result'], 'not_run'); self.assertIsNone(summary['paired_quality_delta'])
            self.assertEqual(summary['samples'][p.ORDER[0]]['metrics']['model_calls'], 1)

    def test_low_baseline_quality_does_not_block_candidate_and_cost_is_second(self):
        self.init(); p.run_one(self.exp); data = self.review_data(p.ORDER[0]); data['scores'] = dict.fromkeys(p.SCORES, 1)
        data.update(major_errors=1, major_error_ids=['f1']); data['findings'][0]['verdict'] = 'incorrect'; self.review(p.ORDER[0], data)
        self.mode('expensive'); candidate = p.run_one(self.exp); self.assertEqual(candidate['metrics']['model_calls'], 13)
        self.assertFalse(p.r.read_json(self.exp / 'state.json')['stopped']); self.review(p.ORDER[1]); summary = p.summarize(self.exp)
        self.assertEqual(summary['paired_quality_delta'], 5); self.assertEqual(summary['quality_result'], 'pass'); self.assertTrue(summary['passed'])
        self.assertLess(summary['cost_comparison']['token_reduction'], 0)

    def test_reverse_export_order_does_not_swap_group_or_quality(self):
        self.pair()
        class Reverse:
            def shuffle(self, values): values.reverse()
        with mock.patch.object(p.random, 'SystemRandom', return_value=Reverse()): p.export_review(self.exp, self.root / 'blind')
        public = p.r.read_json(self.root / 'blind/index.json')['reviews']
        mapping = p.r.read_json(self.exp / 'review-map.json')
        self.assertEqual(mapping[public[0]['review_id']]['sample_id'], p.ORDER[1])
        first = self.review_data(p.ORDER[0]); first['scores'] = dict.fromkeys(p.SCORES, 1)
        for entry in public:
            name = mapping[entry['review_id']]['sample_id']; result = self.review(name, first if name == p.ORDER[0] else None)
            self.assertEqual(result['sample_id'], name); self.assertEqual(result['artifact_sha256'], mapping[entry['review_id']]['artifact_sha256'])
        self.assertEqual(p.summarize(self.exp)['paired_quality_delta'], 5)
        self.assertNotIn('accuracy_rate', json.dumps(p.summarize(self.exp)))

    def test_infra_forbidden_scope_and_unconfirmed_stop_without_retry(self):
        for mode in ('infra', 'scope'):
            self.exp = self.root / mode; self.init(); self.mode(mode); item = p.run_one(self.exp)
            self.assertTrue(p.r.read_json(self.exp / 'state.json')['stopped'])
            with self.assertRaises(p.EvalError): p.run_one(self.exp)
            self.assertNotIn('unit-not-paid-secret', (self.exp / 'samples' / p.ORDER[0] / 'stdout.log').read_text())
            if mode == 'scope': self.assertFalse(item['scope_pass']); self.assertEqual(item['checker']['scope_result'], 'fail')

    def test_omission_and_repair_failures_cannot_hide_behind_valid_citations(self):
        self.pair(); self.review(p.ORDER[0]); data = self.review_data(p.ORDER[1])
        data['quality_checks']['proposed_repairs'] = {'verdict': 'incorrect', 'reason': '合法引用仍可能提出错误修复', 'finding_ids': ['f1']}
        self.review(p.ORDER[1], data); self.assertEqual(p.summarize(self.exp)['quality_result'], 'fail')

    def test_invalid_review_binding_spans_major_omissions_and_unreviewed_are_rejected(self):
        self.pair(); good = self.review_data(p.ORDER[0])
        for change in ('sha', 'span', 'source_sha', 'duplicate_id', 'major', 'omission_id', 'claim_as_omission', 'basis', 'unknown_rubric'):
            data = copy.deepcopy(good)
            if change == 'sha': data['artifact_sha256'] = '0'*64
            elif change == 'span': data['findings'][0]['start_byte'] += 1
            elif change == 'source_sha': data['findings'][0]['evidence'][0]['source_sha256'] = '0'*64
            elif change == 'duplicate_id': data['findings'].append(data['findings'][0])
            elif change == 'major': data['major_errors'] = True
            elif change == 'omission_id': data.update(major_omissions=1, major_omission_ids=['missing'])
            elif change == 'claim_as_omission': data.update(major_omissions=1, major_omission_ids=['f1'])
            elif change == 'basis': data['score_basis']['coverage']['finding_ids'] = ['unknown']
            else: data['findings'][0]['rubric_ids'] = ['imaginary']
            with self.subTest(change=change), self.assertRaises(p.EvalError): self.review(p.ORDER[0], data)
        self.assertEqual(p.summarize(self.exp)['quality_result'], 'not_run')

    def test_major_omission_is_bound_to_real_rubric_and_is_a_quality_failure(self):
        self.pair(); self.review(p.ORDER[0]); data = self.review_data(p.ORDER[1])
        oracle = p.r.read_json(self.exp / 'harness/oracle.json')
        source, evidence = next((source, evidence) for source in oracle['sources'] for evidence in source['evidence'] if evidence['id'] == 'score_default')
        data['findings'] = [{'id': 'f1', 'kind': 'omission', 'rubric_id': 'score_default', 'reason': '单元协议：遗漏固定默认值说明',
                            'evidence': [{'path': source['path'], 'source_sha256': source['sha256'], 'start_line': evidence['start_line'], 'end_line': evidence['end_line'], 'quote': evidence['quote']}]}]
        data.update(major_omissions=1, major_omission_ids=['f1']); self.review(p.ORDER[1], data)
        self.assertEqual(p.summarize(self.exp)['candidate_quality_result'], 'fail')
        self.assertEqual(p.summarize(self.exp)['quality_result'], 'fail')

    def test_source_manifests_are_mandatory_and_credentials_paths_rejected(self):
        with self.assertRaises(p.EvalError): p.init_experiment(self.exp, self.binary, self.binary, project_dotenv=self.dotenv)
        bad = self.root / 'bad-source.json'; p.r.write_json(bad, [{'path': '.env', 'sha256': '0'*64}])
        with self.assertRaises(p.EvalError):
            p.init_experiment(self.exp, self.binary, self.binary, project_dotenv=self.dotenv, baseline_source_manifest=bad, candidate_source_manifest=bad)

    def test_accepted_citations_are_independent_of_engine_pass_and_zero_omission(self):
        self.init(); item = p.run_one(self.exp)
        sample = self.exp / 'samples' / p.ORDER[0]; manifest = p.r.read_json(self.exp / 'manifest.json')
        events = p.r.json_lines(sample / item['evidence']['task_events']); state = events[-1]['payload']
        self.assertEqual(p.check_accepted_citations(self.exp, manifest, state, events)['result'], 'pass')
        legal = copy.deepcopy(state); legal['report']['checked']['findings'][0]['evidence'][0].pop('byte_start')
        self.assertEqual(p.check_accepted_citations(self.exp, manifest, legal, events)['result'], 'pass')
        for field, value in [('version', '0'*64), ('start_line', 90), ('quote', '未读或编造摘录'), ('action_id', 'future')]:
            changed = copy.deepcopy(state); changed['report']['checked']['findings'][0]['evidence'][0][field] = value
            with self.subTest(field=field): self.assertEqual(p.check_accepted_citations(self.exp, manifest, changed, events)['result'], 'fail')

    def test_quality_frozen_bytes_and_returned_stage_must_match(self):
        _, item = self.pair(); sample = self.exp / 'samples' / p.ORDER[1]; manifest = p.r.read_json(self.exp / 'manifest.json')
        events = p.r.json_lines(sample / item['evidence']['task_events']); state = events[-1]['payload']
        self.assertEqual(p.check_quality_publication(sample, manifest, state)['result'], 'pass')
        partial = copy.deepcopy(state); partial['quality']['review_status'] = 'invalid'; partial['last_result']['quality']['review_status'] = 'invalid'
        self.assertEqual(p.check_quality_publication(sample, manifest, partial)['result'], 'pass')
        self.assertEqual(p.check_quality_publication(sample, manifest, partial)['semantic_result'], 'not_run')
        for field, value in [('frozen_sha256', '0'*64), ('revision_status', 'admitted'), ('review_status', 'not_run'), ('review_status', 'failed'), ('source_versions', {'ghost.md': '0'*64})]:
            changed = copy.deepcopy(state); changed['quality'][field] = value
            with self.subTest(field=field), self.assertRaises(p.EvalError): p.check_quality_publication(sample, manifest, changed)

    def test_phase_metrics_are_deduplicated_and_unknown_not_zero(self):
        events = []
        def emit(kind, call, payload): events.append({'task_id': 't', 'event_type': kind, 'model_call_id': call, 'payload': payload})
        emit('model_started', 'a', {'stage': 'quality_review'}); emit('model_usage', 'a', {'input_tokens': 8, 'output_tokens': 2, 'cost': None})
        emit('model_usage', 'a', {'input_tokens': 8, 'output_tokens': 2, 'cost': None}); emit('model_request_encoded', 'a', {'request_encoded_bytes': 500}); emit('model_request_encoded', 'a', {'request_encoded_bytes': 500}); emit('model_finished', 'a', {'duration_ms': 200})
        emit('model_started', 'b', {'stage': 'quality_revision'}); emit('model_finished', 'b', {}); emit('tool_attempted', '', {'tool': 'write_report'})
        metric = p.phase_metrics(events, 't'); self.assertEqual(metric['review']['total_tokens'], 10); self.assertEqual(metric['review']['model_calls'], 1); self.assertEqual(metric['review']['http_attempts'], 2); self.assertEqual(metric['review']['encoded_request_bytes'], 1000)
        self.assertIsNone(metric['review']['cost']); self.assertIsNone(metric['revision']['total_tokens']); self.assertEqual(metric['revision']['unknown_usage_calls'], 1)
        self.assertEqual(metric['revision']['tool_calls'], 1)
        emit('model_started', 'c', {'stage': 'quality_review'})
        with self.assertRaises(p.EvalError): p.phase_metrics(events, 't')

    def test_preflight_failure_or_env_change_starts_no_process(self):
        self.init()
        with mock.patch.object(p.r, 'preflight', side_effect=p.EvalError('模拟沙箱失败')), mock.patch.object(p.subprocess, 'Popen') as popen:
            with self.assertRaises(p.EvalError): p.run_one(self.exp)
        popen.assert_not_called(); self.assertTrue(p.r.read_json(self.exp / 'state.json')['stopped'])
        self.exp = self.root / 'drift'; self.init(); p.run_one(self.exp); self.dotenv.write_text('DEEPSEEK_API_KEY=unit-changed\n')
        with mock.patch.object(p.subprocess, 'Popen') as popen:
            with self.assertRaises(p.EvalError): p.run_one(self.exp)
        popen.assert_not_called()

    def test_binary_flags_are_checked_before_any_paid_sample(self):
        self.binary.write_text(FAKE.replace("'-quality']", "'-unsupported']"))
        self.init()
        with self.assertRaises(p.EvalError): p.run_one(self.exp)
        state = p.r.read_json(self.exp / 'state.json')
        self.assertTrue(state['stopped']); self.assertEqual(state['samples'], {})
        self.assertFalse((self.exp / 'samples').exists())

    def test_source_and_harness_seals_reject_tampering(self):
        self.init(); p.run_one(self.exp)
        (self.exp / 'harness/cases.json').write_text('{}')
        with self.assertRaises(p.EvalError): p.summarize(self.exp)
        with self.assertRaises(p.EvalError): p.run_one(self.exp)


class CounterexampleTests(unittest.TestCase):
    def test_finite_counterexamples_use_exact_claims_and_no_scripts(self):
        cases = f.load_cases(p.CASES); self.assertEqual(f.selfcheck(cases)['cases'], 12)
        self.assertEqual(f.evaluate_claim('合法引文加错误结论也不会自动通过', cases), 'not_run')
        for case in cases['cases']: self.assertEqual(f.evaluate_claim(case['claim'], cases), case['expected'])
    def test_finite_labels_and_source_changes_fail_closed(self):
        cases = f.load_cases(p.CASES); cases['cases'][0]['expected'] = 'pass'
        with self.assertRaises(ValueError): f.selfcheck(cases)


@unittest.skipUnless(sys.platform == 'darwin' and Path('/usr/bin/sandbox-exec').is_file(), '需要macOS禁网只读沙箱')
class SandboxTests(unittest.TestCase):
    def test_frozen_checker_preflight_has_no_credentials_network_or_source_write(self):
        (p.REPO / '.claw').mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=p.REPO / '.claw', prefix='quality-preflight-test-') as temporary:
            root = Path(temporary); binary = root / 'unused'; binary.write_text('not a model invocation'); binary.chmod(0o700)
            manifest = root / 'source.json'; p.r.write_json(manifest, [{'path': 'cmd/claw/main.go', 'sha256': p.r.digest(binary)}])
            experiment = root / 'experiment'
            p.init_experiment(experiment, binary, binary, baseline_source_manifest=manifest, candidate_source_manifest=manifest)
            p.r.preflight(experiment)
            proof = p.r.read_json(experiment / 'checker-runtime/isolation-preflight.json')
            self.assertEqual(proof['returncode'], 0); self.assertEqual(proof['credentials'], 'absent')
            self.assertEqual(proof['network'], 'denied'); self.assertEqual(proof['ancestor_data'], 'denied')
            self.assertFalse((experiment / 'samples').exists())


if __name__ == '__main__': unittest.main()
