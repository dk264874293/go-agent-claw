"""同构建开关对照、完整盲评和有限反例控制面；全部使用假CLI，零付费。"""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('read_features_profile_test', Path(__file__).with_name('run.py'))
p = importlib.util.module_from_spec(spec); spec.loader.exec_module(p)
spec = importlib.util.spec_from_file_location('read_features_checker_test', Path(__file__).with_name('checker.py'))
c = importlib.util.module_from_spec(spec); spec.loader.exec_module(c)
spec = importlib.util.spec_from_file_location('read_features_cases_test', Path(__file__).with_name('finite_cases.py'))
f = importlib.util.module_from_spec(spec); spec.loader.exec_module(f)

FAKE = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,uuid
if '-h' in sys.argv:
 print('\n'.join(['-read-page-bytes','-output-style','-source-views','-local-checks','-claim-focus']));sys.exit(0)
args={};i=1
while i<len(sys.argv):
 k=sys.argv[i];i+=1
 if '=' in k:k,v=k.split('=',1)
 else:v=sys.argv[i];i+=1
 args[k]=v
work=pathlib.Path(args['-dir']);candidate=args.get('-source-views')=='on'
modefile=pathlib.Path(__file__).with_name('mode');mode=modefile.read_text() if modefile.exists() else ''
if mode=='infra':print(os.environ.get('DEEPSEEK_API_KEY',''));sys.exit(1)
task=uuid.uuid4().hex;run=uuid.uuid4().hex;folder=work/'.claw/tasks'/task;folder.mkdir(parents=True)
files=sorted(work.rglob('*.md'));content='# 优化建议\n版本V1/V2与默认score需同步迁移，JSON契约需统一，运行结果待实测。\n'*15
for file in files[:3]:content+='引用：'+file.relative_to(work).as_posix()+':1\n'
blocked=mode in ('blocked','budget');status='BUDGET_EXHAUSTED' if mode=='budget' else 'BLOCKED' if blocked else 'NEEDS_VERIFICATION';stop='tool_limit' if mode=='budget' else 'reading_incomplete' if blocked else 'needs_verification'
if not blocked:(work/'优化汇总.md').write_text(content)
output=hashlib.sha256(content.encode()).hexdigest();mc=13 if mode=='expensive' else 3 if candidate else 1
usage={'model_calls':mc,'tool_calls':len(files)+1,'turns':mc,'elapsed':1000000}
effect={'action_id':'write-action','tool':'write_file','status':'SUCCEEDED','effect':'CONFIRMED','file':{'path':'优化汇总.md','before_version':'missing','after_version':output}}
result={'task_id':task,'run_id':run,'status':status,'stop_code':stop,'reason':'协议单测','usage':usage,'delivery':{'history_complete':True,'effects':[] if blocked else [effect]}}
spec={'id':task,'goal':args['-prompt'],'kind':'change','mode':'workspace','plan':False,'read_context_version':4 if candidate else 2,'read_page_bytes':8000,'output_style':'concise'}
if candidate:spec['read_features']={'version':1,'source_views':True,'local_checks':True,'claim_focus':True}
if mode=='wrong-policy' and candidate:spec['read_context_version']=3
if mode=='wrong-feature' and candidate:spec['read_features']['local_checks']=False
state={'version':1,'spec':spec,'session_id':args['-session'],'usage':usage,'pending':{},'last_result':result,'limits':{'MaxModelCalls':60,'MaxToolCalls':120,'MaxTurns':30,'Timeout':600000000000}}
events=[]
for n,file in enumerate(files):
 data=file.read_bytes();end=len(data)-(1 if mode=='incomplete' and n==len(files)-1 else 0)
 read={'path':file.relative_to(work).as_posix(),'version':hashlib.sha256(data).hexdigest(),'byte_start':0,'byte_end':end,'total_bytes':len(data),'evidence_id':'E'+str(n),'action_id':'a'+str(n)}
 state=dict(state,last_action={'id':'a'+str(n),'tool':'read_file','result':{'status':'SUCCEEDED','read':read}})
 events.append({'version':1,'sequence':len(events)+1,'kind':'action_result','payload':state})
if mode=='scope':files[0].write_text('非法修改')
events.append({'version':1,'sequence':len(events)+1,'kind':'finished','payload':state})
(folder/'events.jsonl').write_text(''.join(json.dumps(e,ensure_ascii=False)+'\n' for e in events))
obs=pathlib.Path(os.environ['CLAW_OBSERVE_DIR'])/uuid.uuid4().hex;obs.mkdir(parents=True);observations=[]
def emit(kind,payload,call=''):
 observations.append({'schema_version':1,'sequence':len(observations)+1,'event_type':kind,'model_call_id':call,'task_id':task,'payload':payload})
for n in range(mc):
 call='m'+str(n);emit('model_started',{'model':args['-model'],'stage':'main','read_context_version':4 if candidate else 2},call)
 emit('model_request_encoded',{'request_encoded_bytes':500,'http_attempt':1},call);emit('model_usage',{'input_tokens':1000,'output_tokens':50,'cost':None},call);emit('model_finished',{'duration_ms':10},call)
 emit('tool_attempted',{'tool':'read_file'},call)
emit('run_finalized',{'result_run_id':run,'status':status});emit('run_returned',{'result_run_id':run,'status':status})
(obs/'events.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in observations));sys.exit(3 if mode=='budget' else 2)
'''


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name)
        self.binary=self.root/'fake';self.binary.write_text(FAKE);self.binary.chmod(0o700)
        self.exp=self.root/'experiment';self.dotenv=self.root/'.env';self.dotenv.write_text('DEEPSEEK_API_KEY=unit-not-paid-secret\n')
        self.patches=[mock.patch.object(p.r,'preflight',lambda path:None),mock.patch.object(p.r,'run_checker',lambda root,sample,manifest:c.inspect(sample/'workspace',manifest['files']))]
        for patch in self.patches:patch.start()
    def tearDown(self):
        for patch in reversed(self.patches):patch.stop()
        self.temp.cleanup()
    def init(self):
        source=self.root/'source.json';p.r.write_json(source,[{'path':'cmd/claw/main.go','sha256':p.r.digest(self.binary),'bytes':self.binary.stat().st_size}])
        return p.init_experiment(self.exp,self.binary,self.binary,project_dotenv=self.dotenv,baseline_source_manifest=source,candidate_source_manifest=source)
    def mode(self,value):(self.exp/'binaries/mode').write_text(value)
    def pair(self):self.init();return p.run_one(self.exp),p.run_one(self.exp)
    def review_data(self,name):
        if not (self.exp/'review-map.json').exists():p.export_review(self.exp,self.root/'blind')
        mapping=p.r.read_json(self.exp/'review-map.json');rid,binding=next((rid,value) for rid,value in mapping.items() if value['sample_id']==name)
        artifact=p.artifact_bytes(self.exp,name);claim='# 优化建议';start=artifact.index(claim.encode());oracle=p.r.read_json(self.exp/'harness/oracle.json')
        evidence={e['id']:(s,e) for s in oracle['sources'] for e in s['evidence']};rubrics=p.r.read_json(self.exp/'harness/rubric.json')['rubrics'];findings=[];assessments={}
        for rubric in rubrics:
            identity='F-'+rubric['id'];source,ev=evidence[rubric['evidence_ids'][0]]
            findings.append({'id':identity,'kind':'claim','claim':claim,'start_byte':start,'end_byte':start+len(claim.encode()),'verdict':'supported','rubric_ids':[rubric['id']],
                             'evidence':[{'path':source['path'],'source_sha256':source['sha256'],'start_line':ev['start_line'],'end_line':ev['end_line'],'quote':ev['quote']}]})
            assessments[rubric['id']]={'status':'supported','severity':'none','reason':'仅协议夹具，不是真实语义成绩','finding_ids':[identity]}
        ids=[finding['id'] for finding in findings];order=p.r.read_json(self.exp/'review-order.json');scores=dict.fromkeys(p.SCORES,2)
        return {'review_id':rid,'artifact_sha256':binding['artifact_sha256'],'reviewer':'unit-protocol-fixture-only','method':'source_audit_agent','scores':scores,
                'score_basis':{s:{'reason':'仅控制面测试，不冒充真实质量成绩','finding_ids':ids} for s in p.SCORES},'major_errors':0,'major_error_ids':[],
                'major_omissions':0,'major_omission_ids':[],'findings':findings,'rubric_assessments':assessments,
                'reverse_check':{'first_order':order,'reverse_order':list(reversed(order)),'consistent':True,'initial_scores':scores.copy(),'initial_assessments_sha256':p.assessment_digest(assessments),'reason':'协议夹具模拟反序一致，非真实评审'},
                'quality_checks':{k:{'verdict':'not_applicable','reason':'单元测试无真实模型修正建议','finding_ids':[]} for k in p.SPECIAL}}
    def refresh_reverse(self,data):
        data['reverse_check']['initial_scores']=data['scores'].copy();data['reverse_check']['initial_assessments_sha256']=p.assessment_digest(data['rubric_assessments'])
    def review(self,name,data=None):
        path=self.root/(name+'.review.json');p.r.write_json(path,data or self.review_data(name));return p.record_review(self.exp,path)

    def test_same_build_flags_versions_and_offline_rebuild(self):
        m=self.init();self.assertEqual(m['binaries']['baseline']['sha256'],m['binaries']['candidate']['sha256']);self.assertEqual(m['policies'],{'baseline':2,'candidate':4})
        first,second=p.run_one(self.exp),p.run_one(self.exp)
        for item,on in [(first,False),(second,True)]:
            self.assertTrue(p.mechanical_ok(item));flags=item['command']
            for flag in ('source-views','local-checks','claim-focus'):self.assertIn('-'+flag+'='+('on' if on else 'off'),flags)
            for flag in ('-analysis','-report','-quality','-read-scope','-require-full-read'):self.assertNotIn(flag,flags)
        self.review(p.ORDER[0]);self.review(p.ORDER[1]);self.assertTrue(p.summarize(self.exp)['passed'])
        (self.exp/'state.json').unlink();proc=subprocess.run([sys.executable,str(self.exp/'harness/run.py'),'summarize','--experiment',str(self.exp)],cwd=self.root,text=True,capture_output=True,check=True)
        self.assertTrue(json.loads(proc.stdout)['passed'])
    def test_different_build_or_source_manifest_is_rejected_before_run(self):
        source=self.root/'s.json';p.r.write_json(source,[{'path':'go.mod','sha256':'a'*64}]);other=self.root/'other';other.write_text(FAKE+'\n#different')
        with self.assertRaises(p.EvalError):p.init_experiment(self.exp,self.binary,other,baseline_source_manifest=source,candidate_source_manifest=source)
        other_source=self.root/'s2.json';p.r.write_json(other_source,[{'path':'go.mod','sha256':'b'*64}])
        with self.assertRaises(p.EvalError):p.init_experiment(self.exp,self.binary,self.binary,baseline_source_manifest=source,candidate_source_manifest=other_source)
    def test_business_failure_retains_cost_and_candidate_runs_without_zero_score(self):
        for mode in ('blocked','budget'):
            self.exp=self.root/mode;self.init();self.mode(mode);first=p.run_one(self.exp);self.assertFalse(first['artifact_exists']);self.assertTrue(first['scope_pass']);self.assertFalse(p.r.read_json(self.exp/'state.json')['stopped'])
            self.mode('');p.run_one(self.exp);s=p.summarize(self.exp);self.assertIsNone(s['paired_quality_delta']);self.assertIsNone(s['cost_comparison']['token_reduction'])
    def test_low_baseline_quality_does_not_gate_candidate_and_cost_second(self):
        self.init();p.run_one(self.exp);self.mode('expensive');second=p.run_one(self.exp);self.assertEqual(second['metrics']['model_calls'],13)
        low=self.review_data(p.ORDER[0]);low['scores']=dict.fromkeys(p.SCORES,1);self.refresh_reverse(low);self.review(p.ORDER[0],low);self.review(p.ORDER[1]);s=p.summarize(self.exp)
        self.assertTrue(s['passed']);self.assertLess(s['cost_comparison']['token_reduction'],0)
    def test_export_waits_for_both_samples_and_order_is_anonymous_stable(self):
        self.init();p.run_one(self.exp)
        with self.assertRaises(p.EvalError):p.export_review(self.exp,self.root/'too-early')
        p.run_one(self.exp)
        class Reverse:
            def shuffle(self,items):items.reverse()
        with mock.patch.object(p.random,'SystemRandom',return_value=Reverse()):p.export_review(self.exp,self.root/'blind')
        public=p.r.read_json(self.root/'blind/index.json');mapping=p.r.read_json(self.exp/'review-map.json');self.assertEqual(mapping[public['reviews'][0]['review_id']]['sample_id'],p.ORDER[1])
        self.review(p.ORDER[0]);self.review(p.ORDER[1]);self.assertEqual(p.summarize(self.exp)['paired_quality_delta'],0)
    def test_review_requires_every_rubric_severity_and_reverse_record(self):
        self.pair();good=self.review_data(p.ORDER[0])
        for mode in ['missing_rubric','severity','reverse','digest','bool_major','sha','span','source','basis']:
            data=copy.deepcopy(good)
            if mode=='missing_rubric':data['rubric_assessments'].pop('field_mapping')
            elif mode=='severity':data['rubric_assessments']['field_mapping']['severity']='major'
            elif mode=='reverse':data['reverse_check']['reverse_order']=data['reverse_check']['first_order']
            elif mode=='digest':data['reverse_check']['initial_assessments_sha256']='0'*64
            elif mode=='bool_major':data['major_errors']=True
            elif mode=='sha':data['artifact_sha256']='0'*64
            elif mode=='span':data['findings'][0]['start_byte']+=1
            elif mode=='source':data['findings'][0]['evidence'][0]['source_sha256']='0'*64
            else:data['score_basis']['coverage']['finding_ids']=['missing']
            with self.subTest(mode=mode),self.assertRaises(p.EvalError):self.review(p.ORDER[0],data)
    def test_different_reviewer_or_method_cannot_make_paired_delta(self):
        self.pair();self.review(p.ORDER[0]);data=self.review_data(p.ORDER[1]);data['reviewer']='another-auditor'
        with self.assertRaises(p.EvalError):self.review(p.ORDER[1],data)
        data=self.review_data(p.ORDER[1]);data['method']='human'
        with self.assertRaises(p.EvalError):self.review(p.ORDER[1],data)
    def test_reverse_disagreement_is_preserved_and_quality_delta_unknown(self):
        self.pair();a=self.review_data(p.ORDER[0]);a['reverse_check'].update(consistent=False,initial_scores=dict.fromkeys(p.SCORES,1),reason='复查分数发生变化，保留分歧不挑高分')
        self.review(p.ORDER[0],a);self.review(p.ORDER[1]);s=p.summarize(self.exp);self.assertIsNone(s['paired_quality_delta']);self.assertEqual(s['paired_review_protocol'],'not_run');self.assertIsNone(s['cost_comparison']['token_reduction'])
    def test_major_omission_is_not_hidden_by_correct_citations(self):
        self.pair();self.review(p.ORDER[0]);data=self.review_data(p.ORDER[1]);identity='F-format_presence';finding=next(f for f in data['findings'] if f['id']==identity)
        finding.update(kind='omission',rubric_id='format_presence',reason='遗漏非空与合法性区别');[finding.pop(k,None) for k in ('claim','start_byte','end_byte','verdict','rubric_ids')]
        data['rubric_assessments']['format_presence'].update(status='omitted',severity='major',reason='冻结major遗漏');data['scores']['important_findings']=1;data.update(major_omissions=1,major_omission_ids=[identity]);self.refresh_reverse(data);self.review(p.ORDER[1],data)
        self.assertFalse(p.summarize(self.exp)['passed'])
    def test_infra_and_forbidden_changes_stop_without_retry(self):
        for mode in ('infra','scope'):
            self.exp=self.root/mode;self.init();self.mode(mode);p.run_one(self.exp);self.assertTrue(p.r.read_json(self.exp/'state.json')['stopped'])
            with self.assertRaises(p.EvalError):p.run_one(self.exp)
    def test_env_and_frozen_inputs_do_not_drift(self):
        self.init();p.run_one(self.exp);self.dotenv.write_text('DEEPSEEK_API_KEY=changed-unit-secret\n')
        with mock.patch.object(p.subprocess,'Popen') as popen,self.assertRaises(p.EvalError):p.run_one(self.exp)
        popen.assert_not_called()
    def test_preflight_rejects_missing_flags_and_starts_no_paid_process(self):
        self.binary.write_text(FAKE.replace("'-claim-focus'","'-missing-option'"));self.init()
        with self.assertRaises(p.EvalError):p.run_one(self.exp)
        self.assertFalse((self.exp/'samples').exists())
    def test_wrong_policy_or_features_is_protocol_error(self):
        for mode in ('wrong-policy','wrong-feature'):
            self.exp=self.root/mode;self.init();p.run_one(self.exp);self.mode(mode);item=p.run_one(self.exp);self.assertFalse(item['protocol_valid']);self.assertTrue(p.r.read_json(self.exp/'state.json')['stopped'])
    def test_seals_and_sample_limit_are_enforced(self):
        self.pair()
        with self.assertRaises(p.EvalError):p.run_one(self.exp)
        (self.exp/'harness/anchors.json').write_text('{}')
        with self.assertRaises(p.EvalError):p.summarize(self.exp)

    def test_rubric_cannot_be_checked_with_wrong_source_even_if_quote_is_real(self):
        self.pair();data=self.review_data(p.ORDER[0]);field=next(f for f in data['findings'] if f['id']=='F-field_mapping');other=next(f for f in data['findings'] if f['id']=='F-score_default');field['evidence']=other['evidence']
        with self.assertRaises(p.EvalError):self.review(p.ORDER[0],data)
    def test_same_score_with_major_omission_removed_is_recorded_as_quality_change(self):
        self.pair();a=self.review_data(p.ORDER[0]);identity='F-format_presence';finding=next(f for f in a['findings'] if f['id']==identity)
        finding.update(kind='omission',rubric_id='format_presence',reason='协议夹具major遗漏');[finding.pop(k,None) for k in ('claim','start_byte','end_byte','verdict','rubric_ids')]
        a['rubric_assessments']['format_presence'].update(status='omitted',severity='major');a['scores']['important_findings']=1;a.update(major_omissions=1,major_omission_ids=[identity]);self.refresh_reverse(a);self.review(p.ORDER[0],a)
        b=self.review_data(p.ORDER[1]);b['scores']['important_findings']=1;self.refresh_reverse(b);self.review(p.ORDER[1],b);result=p.summarize(self.exp)
        self.assertEqual(result['paired_quality_delta'],0);self.assertEqual(result['major_omission_delta'],-1);self.assertTrue(result['quality_improved'])
    def test_unverified_major_assertion_cannot_hide_behind_zero_error_count(self):
        self.pair();self.review(p.ORDER[0]);data=self.review_data(p.ORDER[1]);identity='F-hook_boundary';finding=next(f for f in data['findings'] if f['id']==identity);finding['verdict']='unverified'
        data['rubric_assessments']['hook_boundary'].update(status='unverified',severity='major',reason='把静态代码扩大成确定宿主行为');data['scores']['conditions']=1;self.refresh_reverse(data);self.review(p.ORDER[1],data)
        result=p.summarize(self.exp);self.assertEqual(result['candidate_quality_result'],'fail');self.assertFalse(result['passed'])
    def test_full_score_cannot_contradict_confirmed_errors(self):
        self.pair();data=self.review_data(p.ORDER[0]);finding=next(f for f in data['findings'] if f['id']=='F-field_mapping');finding['verdict']='incorrect';data['rubric_assessments']['field_mapping'].update(status='incorrect',severity='ordinary');self.refresh_reverse(data)
        with self.assertRaises(p.EvalError):self.review(p.ORDER[0],data)


class MechanismTests(unittest.TestCase):
    def test_source_views_do_not_fill_physical_coverage(self):
        file={'path':'a.md','sha256':'a'*64,'bytes':10}
        def read(tool,a,b):return {'kind':'action_result','payload':{'last_action':{'tool':tool,'result':{'status':'SUCCEEDED','read':{'path':'a.md','version':'a'*64,'byte_start':a,'byte_end':b,'total_bytes':10}}}}}
        self.assertFalse(p.physical_coverage([read('read_file',0,5),read('read_evidence',0,10)],[file])['complete'])
    def test_unknown_usage_and_new_measurements_are_not_zero(self):
        metrics=p.observation_metrics([], 't',False);self.assertIsNone(metrics['main']['encoded_request_bytes']);self.assertIsNone(metrics['main']['http_attempts']);self.assertIsNone(metrics['source_views']['created']);self.assertIsNone(metrics['local_checks']['completed'])
    def test_no_extra_review_stage(self):
        event={'task_id':'t','event_type':'model_started','model_call_id':'m','payload':{'stage':'quality_review'}}
        with self.assertRaises(p.EvalError):p.observation_metrics([event],'t',True)

    def test_stages_prove_successful_delivery_and_encoded_folding_separately(self):
        events=[]
        def emit(kind,payload,model='',action=''):
            events.append({'task_id':'t','event_type':kind,'model_call_id':model,'action_id':action,'payload':payload})
        emit('run_configured',{'read_features':dict(p.FEATURES,contract='typed_v2')})
        view={'view_evidence_id':'E9','origin_evidence_id':'E1','source_kind':'current','content_bytes':10,'byte_start':0,'byte_end':10}
        emit('source_view_created',view,action='a')
        for model in ('m1','m2'):
            emit('model_started',{'stage':'main'},model);emit('model_finished',{},model)
            emit('source_view_delivered',view,model);emit('source_view_delivered',view,model)
        emit('source_view_processed',view,'m2');emit('model_started',{'stage':'main'},'m3');emit('source_view_folded',view,'m3')
        before=p.observation_metrics(events,'t',True)['source_views']
        self.assertEqual(before['created'],1);self.assertEqual(before['delivered'],1);self.assertEqual(before['delivered_observations'],2);self.assertEqual(before['delivered_cumulative_bytes'],20)
        self.assertEqual(before['processed'],1);self.assertEqual(before['folded'],0);self.assertEqual(before['folded_unencoded_observations'],1)
        emit('model_request_encoded',{'request_encoded_bytes':500,'http_attempt':1},'m3');after=p.observation_metrics(events,'t',True)['source_views']
        self.assertEqual(after['folded'],1);self.assertEqual(after['folded_cumulative_bytes'],10)
    def test_local_tool_receipts_not_run_do_not_become_effective_checks(self):
        events=[{'task_id':'t','event_type':'run_configured','payload':{'read_features':p.FEATURES}},
                {'task_id':'t','event_type':'tool_attempted','action_id':'a','payload':{'tool':'check_evidence'}},
                {'task_id':'t','event_type':'local_check_batch','action_id':'a','payload':{'checks_requested':3}}]
        for i in range(3):events.append({'task_id':'t','event_type':'local_check_completed','payload':{'check_id':'K'+str(i),'check_result':'not_run'}})
        metrics=p.observation_metrics(events,'t',True)['local_checks']
        self.assertEqual(metrics['tool_attempts'],1);self.assertEqual(metrics['checks_requested'],3);self.assertEqual(metrics['receipt_completed'],3);self.assertEqual(metrics['decidable'],0);self.assertEqual(metrics['decidable_rate'],0)
        events[-1]['payload']['check_result']='fail';self.assertEqual(p.observation_metrics(events,'t',True)['local_checks']['decidable_rate'],1/3)
    def test_explicit_disabled_zero_is_different_from_missing_new_instrumentation(self):
        off={'task_id':'t','event_type':'run_configured','payload':{'read_features':{'version':1,'source_views':False,'local_checks':False,'claim_focus':False}}}
        metrics=p.observation_metrics([off],'t',False);self.assertEqual(metrics['source_views']['created'],0);self.assertEqual(metrics['local_checks']['checks_requested'],0)
        missing=p.observation_metrics([],'t',False);self.assertIsNone(missing['source_views']['created']);self.assertIsNone(missing['local_checks']['checks_requested'])
    def test_http_attempt_dedup_and_unknown_materials(self):
        events=[{'task_id':'t','event_type':'model_started','model_call_id':'m','payload':{'stage':'main','local_check_schema_bytes':0,'source_view_schema_bytes':0,'feature_prompt_bytes':0}}]
        for n in (1,1,2):events.append({'task_id':'t','event_type':'model_request_encoded','model_call_id':'m','payload':{'http_attempt':n,'request_encoded_bytes':400}})
        result=p.observation_metrics(events,'t',False);self.assertEqual(result['main']['http_attempts'],2);self.assertEqual(result['main']['encoded_request_bytes'],800)
        self.assertEqual(result['materials']['local_check_schema_bytes'],0);self.assertIsNone(result['materials']['source_bytes']);self.assertEqual(result['unknown_material_calls']['source_bytes'],1)

    def test_disabled_capability_cannot_report_actual_execution(self):
        config={'task_id':'t','event_type':'run_configured','payload':{'read_features':{'version':1,'source_views':False,'local_checks':False,'claim_focus':False}}}
        event={'task_id':'t','event_type':'local_check_completed','payload':{'check_id':'K1','check_result':'pass'}}
        with self.assertRaises(p.EvalError):p.observation_metrics([config,event],'t',False)
    def test_invalid_check_attempt_is_not_a_completed_batch(self):
        events=[{'task_id':'t','event_type':'run_configured','payload':{'read_features':p.FEATURES}}, {'task_id':'t','event_type':'tool_attempted','action_id':'a','payload':{'tool':'check_evidence'}}]
        metric=p.observation_metrics(events,'t',True)['local_checks'];self.assertEqual(metric['tool_attempts'],1);self.assertEqual(metric['validated_batches'],0);self.assertIsNone(metric['checks_requested']);self.assertEqual(metric['receipt_completed'],0);self.assertIsNone(metric['decidable_rate'])

    def test_observation_gap_keeps_partial_counts_without_claiming_exact_zero(self):
        events=[{'task_id':'t','event_type':'run_configured','payload':{'read_features':p.FEATURES}}, {'event_type':'gap','payload':{'dropped':1}}]
        metrics=p.observation_metrics(events,'t',True);self.assertFalse(metrics['recording_complete']);self.assertIsNone(metrics['source_views']['created']);self.assertIsNone(metrics['local_checks']['receipt_completed']);self.assertEqual(metrics['known_observed_source_views']['created'],0)


class CounterexampleTests(unittest.TestCase):
    def test_finite_cases_and_actual_timestamp_objects(self):
        self.assertEqual(f.selfcheck(f.load_cases(p.CASES))['provider_calls'],0);self.assertEqual(f.timestamp_selfcheck(p.TIMESTAMP_CASES)['adaptive_final'],'pass')
    def test_field_mapping_hook_and_format_oracle_are_source_bound(self):
        spec=importlib.util.spec_from_file_location('read_features_oracle',(p.HERE/'quality_oracle.py' if (p.HERE/'quality_oracle.py').is_file() else p.REPO/'scripts/coverage_efficiency_eval/quality_oracle.py'));oracle=importlib.util.module_from_spec(spec);spec.loader.exec_module(oracle)
        data=oracle.load_oracle(p.ORACLE);checked=oracle.inspect_evidence(data,p.DEFAULT_WORKSPACE);self.assertEqual(checked['environment_result'],'pass')
        self.assertEqual(oracle.evaluate_claims([{'text':'organizer的标准JSON只有id,title,source_url,summary,tags,status，因此没有承载标签的字段。'}],data,p.DEFAULT_WORKSPACE)['semantic_result'],'fail')
        self.assertEqual(oracle.evaluate_claims([{'text':'仅凭参考插件代码即可确定当前宿主永远不会把校验输出反馈给模型。'}],data,p.DEFAULT_WORKSPACE)['semantic_result'],'fail')
        self.assertEqual(oracle.evaluate_claims([{'text':'score_format会验证ID格式、URL和状态枚举是否合法。'}],data,p.DEFAULT_WORKSPACE)['semantic_result'],'fail')
    def test_all_eight_rubrics_have_frozen_severity_and_score_anchors(self):
        r=p.r.read_json(p.RUBRIC);a=p.r.read_json(p.ANCHORS);self.assertEqual(len(r['rubrics']),8);self.assertEqual(set(a['score_anchors']),set(p.SCORES));self.assertTrue(all(set(v)=={'0','1','2'} for v in a['score_anchors'].values()))


if __name__=='__main__':unittest.main()
