"""新交付流程控制面与评审协议自检；假CLI只测机制，不冒充真实质量。"""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('delivery_review_profile_test',Path(__file__).with_name('run.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
spec=importlib.util.spec_from_file_location('delivery_review_checker_test',Path(__file__).with_name('checker.py'))
c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
spec=importlib.util.spec_from_file_location('delivery_review_cases_test',Path(__file__).with_name('finite_cases.py'))
f=importlib.util.module_from_spec(spec);spec.loader.exec_module(f)

FAKE = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,uuid
if '-h' in sys.argv:
 print('\n'.join(['-read-page-bytes','-output-style','-source-views','-local-checks','-claim-focus','-delivery-review']));sys.exit(0)
args={};i=1
while i<len(sys.argv):
 k=sys.argv[i];i+=1
 if '=' in k:k,v=k.split('=',1)
 else:v=sys.argv[i];i+=1
 args[k]=v
work=pathlib.Path(args['-dir']);candidate=args.get('-delivery-review')=='on'
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
spec={'id':task,'goal':args['-prompt'],'kind':'change','mode':'workspace','plan':False,'read_context_version':4,'read_page_bytes':8000,'output_style':'concise'}
spec['read_features']={'version':1,'source_views':False,'local_checks':True,'claim_focus':False}
if candidate:
 spec['delivery_review']={'version':1}
 result['delivery_review']={'version':1,'status':'completed','admission_count':1,'origin_run_id':run,'documents':len(files),'addressed':len(files),'not_relevant':0,'unverified':0,'undeclared':0,'ready_to_resolve':0}
if mode=='wrong-policy' and candidate:spec['read_context_version']=3
if mode=='wrong-feature' and candidate:spec['read_features']['local_checks']=False
state={'version':3 if candidate else 1,'spec':spec,'session_id':args['-session'],'usage':usage,'pending':{},'last_result':result,'limits':{'MaxModelCalls':60,'MaxToolCalls':120,'MaxTurns':30,'Timeout':600000000000}}
if candidate:state['delivery_review']={'version':1,'status':'completed','evaluated':True,'admission_count':1,'origin_run_id':run}
if mode=='wrong-state' and candidate:state['version']=1
if mode=='reserved-context' and candidate:
 status='BLOCKED';stop='context_limit';result['status']=status;result['stop_code']=stop;usage['model_calls']+=1;usage['turns']+=1
 state['delivery_review'].update(status='failed',turn_consumed=True,model_consumed=False)
 result['delivery_review']['status']='failed'
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
emit('run_configured',{'read_features':dict(spec['read_features'],contract='experimental_v2')})
for n in range(mc):
 call='m'+str(n);emit('model_started',{'model':args['-model'],'stage':'delivery_review' if candidate and mode!='reserved-context' and n==mc-1 else 'main','read_context_version':4},call)
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
    def export(self):
        if not (self.exp/'review-map.json').exists():p.export_review(self.exp,self.root/'blind')
    def draft(self,name):
        self.export();mapping=p.r.read_json(self.exp/'review-map.json');rid,binding=next((rid,value) for rid,value in mapping.items() if value['sample_id']==name)
        artifact=p.artifact_bytes(self.exp,name);claim='# 优化建议';start=artifact.index(claim.encode());oracle=p.r.read_json(self.exp/'harness/oracle.json')
        evidence={e['id']:(s,e) for s in oracle['sources'] for e in s['evidence']};rubrics=p.r.read_json(self.exp/'harness/rubric.json')['rubrics'];findings=[];assessments={}
        categories={'version_status':'version_scope','score_default':'default','numeric_discrepancy':'numeric','format_presence':'validation_semantics','existing_commands':'command_existence','hook_boundary':'runtime_boundary','timestamp_subject':'object_identity','field_mapping':'source_attribution'}
        for rubric in rubrics:
            identity='F-'+rubric['id'];source,ev=evidence[rubric['evidence_ids'][0]]
            findings.append({'id':identity,'kind':'claim','claim':claim,'start_byte':start,'end_byte':start+len(claim.encode()),'verdict':'supported','severity':'none','issue_types':[categories[rubric['id']]],'rubric_ids':[rubric['id']],'reason':'仅协议夹具，不是真实语义判断',
                             'evidence':[{'path':source['path'],'source_sha256':source['sha256'],'start_line':ev['start_line'],'end_line':ev['end_line'],'quote':ev['quote']}]})
            assessments[rubric['id']]={'status':'supported','severity':'none','reason':'仅协议夹具，不是真实语义成绩','finding_ids':[identity]}
        ids=[finding['id'] for finding in findings];scores=dict.fromkeys(p.SCORES,2)
        return {'schema_version':2,'review_id':rid,'artifact_sha256':binding['artifact_sha256'],'reviewer':'unit-protocol-fixture-only','method':'source_audit_agent','scores':scores,
                'score_basis':{s:{'reason':'仅控制面测试，不冒充真实质量成绩','finding_ids':ids} for s in p.SCORES},'findings':findings,'rubric_assessments':assessments}
    def write(self,name,value):
        path=self.root/(name+'.json');p.r.write_json(path,value);return path
    def normalized(self,data):return p.review_protocol(self.exp,p.r.read_json(self.exp/'manifest.json')).normalize(data)
    def seal_and_import(self,first,reverse=None):
        reverse=copy.deepcopy(first) if reverse is None else reverse
        order=p.r.read_json(self.exp/'review-order.json');byid={v['review_id']:v for v in first.values()};reverseids={v['review_id']:v for v in reverse.values()}
        for phase,ids,values in [('first',order,byid),('reverse',list(reversed(order)),reverseids)]:
            for rid in ids:p.seal_review(self.exp,self.write(phase+'-'+rid,values[rid]),phase)
        for name,data in reverse.items():p.record_review(self.exp,self.write('import-'+name,data))
    def reviews(self):return {n:self.draft(n) for n in p.ORDER}
    def omission(self,data,rid):
        identity='F-'+rid;row=next(v for v in data['findings'] if v['id']==identity);severity=next(v for v in p.r.read_json(self.exp/'harness/rubric.json')['rubrics'] if v['id']==rid)['severity_policy']['omitted']
        for key in ('claim','start_byte','end_byte','verdict','rubric_ids'):row.pop(key,None)
        row.update(kind='omission',rubric_id=rid,severity=severity,issue_types=['omission'],reason='协议夹具遗漏')
        data['rubric_assessments'][rid].update(status='omitted',severity=severity);data['scores']['important_findings']=1
    def incorrect(self,data,rid,major=False):
        row=next(v for v in data['findings'] if v['id']=='F-'+rid);row.update(verdict='incorrect',severity='major' if major else 'ordinary')
        data['rubric_assessments'][rid].update(status='incorrect',severity=row['severity']);data['scores']['correctness']=0 if major else 1

    def test_same_build_only_delivery_switch_changes(self):
        m=self.init();self.assertEqual(m['task_state_versions'],{'baseline':1,'candidate':3});a,b=p.run_one(self.exp),p.run_one(self.exp)
        for value,on in [(a,False),(b,True)]:
            self.assertTrue(p.mechanical_ok(value));args=value['command']
            self.assertEqual(value['metrics']['mechanisms']['main']['model_calls'] + value['metrics']['mechanisms']['delivery_review']['model_calls'], value['metrics']['mechanisms']['all_model_requests']['model_calls'])
            for flag in ('-source-views=off','-local-checks=on','-claim-focus=off','-delivery-review='+('on' if on else 'off')):self.assertIn(flag,args)
            for flag in ('-analysis','-report','-quality','-read-scope','-require-full-read'):self.assertNotIn(flag,args)
        self.seal_and_import(self.reviews());self.assertTrue(p.summarize(self.exp)['passed'])
    def test_frozen_harness_rebuilds_without_state_index(self):
        self.pair();self.seal_and_import(self.reviews());(self.exp/'state.json').unlink()
        proc=subprocess.run([sys.executable,str(self.exp/'harness/run.py'),'summarize','--experiment',str(self.exp)],cwd=self.root,text=True,capture_output=True,check=True)
        self.assertTrue(json.loads(proc.stdout)['passed'])
    def test_different_binary_or_source_manifest_rejected(self):
        source=self.root/'s.json';p.r.write_json(source,[{'path':'go.mod','sha256':'a'*64}]);other=self.root/'other';other.write_text(FAKE+'\n#different')
        with self.assertRaises(p.EvalError):p.init_experiment(self.exp,self.binary,other,baseline_source_manifest=source,candidate_source_manifest=source)
        second=self.root/'s2.json';p.r.write_json(second,[{'path':'go.mod','sha256':'b'*64}])
        with self.assertRaises(p.EvalError):p.init_experiment(self.exp,self.binary,self.binary,baseline_source_manifest=source,candidate_source_manifest=second)
    def test_state_three_not_reinterpreted_as_one(self):
        self.init();p.run_one(self.exp);self.mode('wrong-state');item=p.run_one(self.exp)
        self.assertFalse(item['protocol_valid']);self.assertTrue(p.r.read_json(self.exp/'state.json')['stopped'])
    def test_business_failures_continue_unique_candidate(self):
        for mode in ('blocked','budget'):
            self.exp=self.root/mode;self.init();self.mode(mode);first=p.run_one(self.exp);self.assertFalse(first['artifact_exists']);self.assertTrue(first['scope_pass']);self.assertFalse(p.r.read_json(self.exp/'state.json')['stopped'])
            self.mode('');p.run_one(self.exp);value=p.summarize(self.exp);self.assertIsNone(value['paired_quality_delta']);self.assertIsNone(value['cost_comparison']['token_reduction'])
    def test_cost_not_execution_gate(self):
        self.init();p.run_one(self.exp);self.mode('expensive');second=p.run_one(self.exp);self.assertEqual(second['metrics']['model_calls'],13)
        self.seal_and_import(self.reviews());self.assertTrue(p.summarize(self.exp)['passed'])
    def test_export_waits_for_both_and_bindings_ignore_order(self):
        self.init();p.run_one(self.exp)
        with self.assertRaises(p.EvalError):p.export_review(self.exp,self.root/'early')
        p.run_one(self.exp)
        class Reverse:
            def shuffle(self,items):items.reverse()
        with mock.patch.object(p.random,'SystemRandom',return_value=Reverse()):p.export_review(self.exp,self.root/'blind')
        mapping=p.r.read_json(self.exp/'review-map.json');index=p.r.read_json(self.root/'blind/index.json')
        self.assertEqual(mapping[index['reviews'][0]['review_id']]['sample_id'],p.ORDER[1]);self.seal_and_import(self.reviews());self.assertEqual(p.summarize(self.exp)['paired_quality_delta'],0)
    def test_read_only_validate_no_model_or_files_changed(self):
        self.pair();data=self.draft(p.ORDER[0]);path=self.write('draft',data);before={str(f):f.read_bytes() for f in self.exp.rglob('*') if f.is_file()}
        with mock.patch.object(p.subprocess,'Popen') as call:value=p.validate_review_draft(self.exp,path)
        call.assert_not_called();self.assertEqual(value['paid_calls'],0);self.assertEqual(before,{str(f):f.read_bytes() for f in self.exp.rglob('*') if f.is_file()})
    def test_full_rubric_and_source_sha_span_validated(self):
        self.pair();good=self.draft(p.ORDER[0])
        for mode in ('rubric','severity','sha','span','source','basis','category'):
            value=copy.deepcopy(good)
            if mode=='rubric':value['rubric_assessments'].pop('field_mapping')
            elif mode=='severity':value['rubric_assessments']['field_mapping']['severity']='major'
            elif mode=='sha':value['artifact_sha256']='0'*64
            elif mode=='span':value['findings'][0]['start_byte']+=1
            elif mode=='source':value['findings'][0]['evidence'][0]['source_sha256']='0'*64
            elif mode=='basis':value['score_basis']['coverage']['finding_ids']=['missing']
            else:value['findings'][0]['issue_types']=['unknown']
            with self.subTest(mode=mode),self.assertRaises(p.EvalError):self.normalized(value)
    def test_derived_subject_identity_cannot_hide_source_attribution_error(self):
        self.pair();value=self.draft(p.ORDER[1]);self.incorrect(value,'field_mapping');derived=self.normalized(value)
        self.assertEqual(derived['quality_checks']['subject_identity']['verdict'],'incorrect');self.assertEqual(derived['major_errors'],0)
        value['quality_checks']=derived['quality_checks'];value['quality_checks']['subject_identity']['verdict']='supported'
        with self.assertRaises(p.EvalError):self.normalized(value)
    def test_ordinary_omission_reduces_score_not_extra_hard_gate(self):
        self.pair();values=self.reviews();self.omission(values[p.ORDER[1]],'existing_commands');self.seal_and_import(values);s=p.summarize(self.exp)
        self.assertTrue(s['candidate_quality_result']=='pass');self.assertEqual(s['samples'][p.ORDER[1]]['review']['major_omissions'],0)
    def test_major_omission_is_derived_and_fails(self):
        self.pair();values=self.reviews();self.omission(values[p.ORDER[1]],'format_presence');self.seal_and_import(values);s=p.summarize(self.exp)
        self.assertEqual(s['samples'][p.ORDER[1]]['review']['major_omissions'],1);self.assertEqual(s['candidate_quality_result'],'fail');self.assertFalse(s['quality_improved'])
    def test_major_error_forces_zero_correctness(self):
        self.pair();value=self.draft(p.ORDER[1]);self.incorrect(value,'numeric_discrepancy',True);self.assertEqual(self.normalized(value)['major_errors'],1);value['scores']['correctness']=1
        with self.assertRaises(p.EvalError):self.normalized(value)
    def test_major_unverified_does_not_evade_gate(self):
        self.pair();values=self.reviews();v=values[p.ORDER[1]];row=next(f for f in v['findings'] if f['id']=='F-version_status');row.update(verdict='unverified',severity='major');v['rubric_assessments']['version_status'].update(status='unverified',severity='major');v['scores']['conditions']=1
        self.seal_and_import(values);s=p.summarize(self.exp);self.assertEqual(s['candidate_quality_result'],'fail');self.assertEqual(s['samples'][p.ORDER[1]]['review']['major_unverified'],1)
    def test_rising_score_with_worsening_major_cannot_be_improved(self):
        self.pair();values=self.reviews();values[p.ORDER[0]]['scores']=dict.fromkeys(p.SCORES,1);self.omission(values[p.ORDER[1]],'version_status');self.seal_and_import(values);s=p.summarize(self.exp)
        self.assertGreater(s['paired_quality_delta'],0);self.assertFalse(s['quality_improved']);self.assertEqual(s['major_omission_delta'],1)
    def test_wording_and_order_changes_do_not_fake_semantic_disagreement(self):
        self.pair();values=self.reviews();reverse=copy.deepcopy(values)
        for v in reverse.values():
            v['findings'].reverse()
            for item in v['findings']:item['reason']='复查措辞变化';item['evidence'].reverse();item['issue_types'].reverse()
            for item in v['rubric_assessments'].values():item['reason']='只改措辞';item['finding_ids'].reverse()
        self.seal_and_import(values,reverse);self.assertEqual(p.summarize(self.exp)['paired_review_protocol'],'pass')
    def test_genuine_verdict_change_preserves_full_first_and_makes_delta_unknown(self):
        self.pair();first=self.reviews();second=copy.deepcopy(first);self.incorrect(second[p.ORDER[1]],'field_mapping');self.seal_and_import(first,second);s=p.summarize(self.exp)
        self.assertIsNone(s['paired_quality_delta']);self.assertEqual(s['paired_review_protocol'],'not_run');rid=first[p.ORDER[1]]['review_id'];sealed=p.r.read_json(self.exp/'reviews/first'/(rid+'.json'));self.assertEqual(sealed['findings'][-1]['verdict'],'supported')
    def test_final_import_reasons_are_from_frozen_reverse(self):
        self.pair();first=self.reviews();second=copy.deepcopy(first)
        for v in second.values():v['findings'][0]['reason']='这是反序已封存理由'
        self.seal_and_import(first,second)
        for name in p.ORDER:self.assertEqual(p.r.read_json(self.exp/'samples'/name/'review.json')['findings'][0]['reason'],'这是反序已封存理由')
    def test_admission_is_not_actual_review_execution(self):
        summary={'version':1,'status':'completed','admission_count':1,'origin_run_id':'run'}
        state={'last_result':{'delivery_review':summary},'delivery_review':dict(summary)}
        with self.assertRaises(p.EvalError):p.validate_delivery_review_outcome(state,{'run_id':'run','metrics':{'phases':{'delivery_review':{'model_calls':0}}}})
        with self.assertRaises(p.EvalError):p.validate_delivery_review_outcome(state,{'run_id':'other','metrics':{'phases':{'delivery_review':{'model_calls':1}}}})
    def test_reserved_before_http_is_budget_admission_not_actual_request(self):
        self.init();p.run_one(self.exp);self.mode('reserved-context');value=p.run_one(self.exp)
        self.assertTrue(value['protocol_valid']);self.assertEqual(value['metrics']['budget_model_admissions'],4);self.assertEqual(value['metrics']['model_calls'],4);self.assertEqual(value['metrics']['actual_model_requests'],3);self.assertEqual(value['metrics']['unissued_reserved_model_admissions'],1);self.assertEqual(value['metrics']['phases']['delivery_review']['model_calls'],0);self.assertFalse(p.r.read_json(self.exp/'state.json')['stopped'])
    def test_unexplained_missing_model_request_is_protocol_error(self):
        state={'version':3,'spec':{'delivery_review':{'version':1}},'last_result':{'run_id':'r','usage':{'model_calls':1}},'delivery_review':{'version':1,'admission_count':1,'origin_run_id':'other','turn_consumed':True,'model_consumed':True,'status':'failed'}}
        with self.assertRaises(p.EvalError):p.model_admission_accounting(state,[],'t')
    def reserved_state(self, **flags):
        d={'version':1,'admission_count':1,'origin_run_id':'r','status':'failed'};d.update(flags)
        return {'version':3,'spec':{'delivery_review':{'version':1}},'last_result':{'run_id':'r','usage':{'model_calls':1}},'delivery_review':d}
    def test_cancel_reserved_omitempty_flags_are_known_false(self):
        value=p.model_admission_accounting(self.reserved_state(),[],'t')
        self.assertEqual(value,{'budget_model_admissions':1,'actual_model_requests':0,'unissued_reserved_model_admissions':1})
    def test_cancel_after_take_model_before_provider_is_still_unissued(self):
        state=self.reserved_state(turn_consumed=True,model_consumed=True,status='outcome_unknown');state['last_result']['usage']['model_calls']=2
        observed=[{'task_id':'t','event_type':'model_started','model_call_id':'main','payload':{'stage':'main'}}]
        value=p.model_admission_accounting(state,observed,'t');self.assertEqual(value['budget_model_admissions'],2);self.assertEqual(value['actual_model_requests'],1);self.assertEqual(value['unissued_reserved_model_admissions'],1)
    def test_invalid_consumption_flag_types_and_order_rejected(self):
        for key in ('turn_consumed','model_consumed'):
            for value in (None,0,1,'true'):
                with self.subTest(key=key,value=value),self.assertRaises(p.EvalError):p.model_admission_accounting(self.reserved_state(**{key:value}),[],'t')
        with self.assertRaises(p.EvalError):p.model_admission_accounting(self.reserved_state(model_consumed=True),[],'t')
        with self.assertRaises(p.EvalError):p.model_admission_accounting(self.reserved_state(turn_consumed=False,model_consumed=True),[],'t')
    def test_existing_delivery_stage_cannot_explain_missing_main_request(self):
        state=self.reserved_state(turn_consumed=True,model_consumed=True);state['last_result']['usage']['model_calls']=2
        observed=[{'task_id':'t','event_type':'model_started','model_call_id':'review','payload':{'stage':'delivery_review'}}]
        with self.assertRaises(p.EvalError):p.model_admission_accounting(state,observed,'t')
    def test_normal_success_count_is_unchanged(self):
        state=self.reserved_state(turn_consumed=True,model_consumed=True,status='completed')
        observed=[{'task_id':'t','event_type':'model_started','model_call_id':'review','payload':{'stage':'delivery_review'}}]
        self.assertEqual(p.model_admission_accounting(state,observed,'t'),{'budget_model_admissions':1,'actual_model_requests':1,'unissued_reserved_model_admissions':0})
    def test_real_sanitized_summary_note_nil_slice_is_valid(self):
        fixture=p.r.read_json(Path(__file__).with_name('go_nil_slices.json'));before=copy.deepcopy(fixture)
        item={'run_id':'offline-run','_source_files':[],'metrics':{'mechanisms':{'local_checks':{'tool_attempts':0,'checks_requested':0,'receipt_completed':0,'results':{'pass':0,'fail':0,'error':0,'not_run':0},'decidable':0}}}}
        value=p.delivery_mechanisms(fixture['events'],fixture['state'],item)
        self.assertEqual(value['source_bound_findings']['current'],fixture['expected']['source_findings']);self.assertEqual(value['result'],fixture['expected']['mechanism_result']);self.assertEqual(fixture,before)
    def test_known_go_slices_allow_null_but_reject_other_types(self):
        self.assertEqual(p.recorded_slice({},'findings'),[]);self.assertEqual(p.recorded_slice({'findings':None},'findings'),[]);self.assertEqual(p.recorded_slice({'findings':[]},'findings'),[])
        for value in (False,0,'',{},'not-an-array'):
            with self.subTest(value=value),self.assertRaises(p.EvalError):p.recorded_slice({'findings':value},'findings')
    def test_cancel_reservation_requires_typed_same_run_configuration(self):
        for kind in ('ledger_version','spec_version','admission_bool','state_version','wrong_run'):
            state=self.reserved_state()
            if kind=='ledger_version':state['delivery_review']['version']=True
            elif kind=='spec_version':state['spec']['delivery_review']['version']=True
            elif kind=='admission_bool':state['delivery_review']['admission_count']=True
            elif kind=='state_version':state['version']=3.0
            else:state['delivery_review']['origin_run_id']='other'
            with self.subTest(kind=kind),self.assertRaises(p.EvalError):p.model_admission_accounting(state,[],'t')
    def test_completed_is_not_adoption_or_semantic_success(self):
        self.pair();value=p.r.read_json(self.exp/'samples'/p.ORDER[1]/'result.json');m=value['delivery_review_mechanisms'];self.assertEqual(m['result'],'not_achieved');self.assertEqual(m['local_checks']['declared_adopted'],0);self.assertEqual(m['semantic_result'],'not_run')
    def test_source_ready_and_check_adoption_are_separate_observations(self):
        sha='a'*64;e={'run_id':'r','agent_id':'a','receipt':{'path':'a.md','version':sha}};finding={'id':'F1','statement':'来源中的可检查事实','status':'evidence_valid','evidence':[{'evidence_id':'E1'}]}
        before={'read_memory':{'evidence':{'E1':e}},'delivery_review':{'summary':{'unresolved':[{'id':'w','kind':'ready_to_resolve','path':'a.md','version':sha}]}}}
        after={'read_memory':{'evidence':{'E1':e},'notes':{'a':{'findings':[finding]}},'source_reviews':[{'run_id':'r','agent_id':'a','path':'a.md','version':sha,'evidence_ids':['E1'],'finding_ids':['F1'],'check_ids':['K1'],'disposition':'addressed'}]},'local_checks':[{'id':'K1','origin_run_id':'r','agent_id':'a','result':'fail','sources':[{'path':'a.md','version':sha}]}]}
        events=[{'kind':'delivery_review_updated','sequence':1,'payload':before},{'kind':'analysis_updated','sequence':2,'payload':after},{'kind':'source_reviews_updated','sequence':3,'payload':after}]
        item={'run_id':'r','_source_files':[{'path':'a.md','sha256':sha}],'delivery_review':{'status':'completed','addressed':1,'undeclared':0},'metrics':{'mechanisms':{'local_checks':{'tool_attempts':1,'checks_requested':1,'receipt_completed':1,'decidable':1,'results':{'pass':0,'fail':1,'error':0,'not_run':0}}}}}
        value=p.delivery_mechanisms(events,after,item);self.assertEqual(value['result'],'achieved');self.assertEqual(value['source_bound_findings']['current'],1);self.assertEqual(value['ready_disposition_changes']['declared_dispositioned'],1);self.assertEqual(value['local_checks']['declared_adopted_results']['fail'],1);self.assertEqual(value['local_checks']['counterexample_correction_result'],'not_run');self.assertEqual(after['local_checks'][0]['result'],'fail')
    def test_delivery_prompt_bytes_are_separate_known_zero_or_unknown(self):
        event={'task_id':'t','model_call_id':'m','event_type':'model_started','payload':{'stage':'main','feature_prompt_bytes':9,'delivery_review_prompt_bytes':0}}
        value=p.observation_metrics([event],'t',False);self.assertEqual(value['materials']['feature_prompt_bytes'],9);self.assertEqual(value['materials']['delivery_review_prompt_bytes'],0)
        event['payload'].pop('delivery_review_prompt_bytes');value=p.observation_metrics([event],'t',False);self.assertIsNone(value['materials']['delivery_review_prompt_bytes']);self.assertEqual(value['unknown_material_calls']['delivery_review_prompt_bytes'],1)
    def test_phase_order_and_same_reviewer_enforced(self):
        self.pair();values=self.reviews();order=p.r.read_json(self.exp/'review-order.json');by={v['review_id']:v for v in values.values()}
        with self.assertRaises(p.EvalError):p.seal_review(self.exp,self.write('bad',by[order[0]]),'reverse')
        p.seal_review(self.exp,self.write('first',by[order[0]]),'first');v=copy.deepcopy(by[order[1]]);v['reviewer']='other'
        with self.assertRaises(p.EvalError):p.seal_review(self.exp,self.write('other',v),'first')
    def test_sealed_snapshot_cannot_be_modified(self):
        self.pair();values=self.reviews();order=p.r.read_json(self.exp/'review-order.json');value=next(v for v in values.values() if v['review_id']==order[0]);p.seal_review(self.exp,self.write('draft',value),'first');(self.exp/'reviews/first'/(order[0]+'.json')).write_text('{}')
        with self.assertRaises(p.EvalError):p.review_protocol(self.exp,p.r.read_json(self.exp/'manifest.json')).journal()
    def test_manual_consistent_and_major_counts_cannot_override(self):
        self.pair();values=self.reviews();self.seal_and_import(values);value=values[p.ORDER[0]];value['reverse_check']={'consistent':True}
        with self.assertRaises(p.EvalError):p.validate_review(self.exp,p.r.read_json(self.exp/'manifest.json'),p.ORDER[0],value)
        value=copy.deepcopy(values[p.ORDER[0]]);value['major_errors']=1
        with self.assertRaises(p.EvalError):self.normalized(value)
        value['major_errors']=False
        with self.assertRaises(p.EvalError):self.normalized(value)
    def test_import_requires_both_sealed_phases(self):
        self.pair();value=self.draft(p.ORDER[0])
        with self.assertRaises(p.EvalError):p.record_review(self.exp,self.write('early',value))
    def test_infra_scope_fail_stop_no_retry(self):
        for mode in ('infra','scope'):
            self.exp=self.root/mode;self.init();self.mode(mode);p.run_one(self.exp);self.assertTrue(p.r.read_json(self.exp/'state.json')['stopped'])
            with self.assertRaises(p.EvalError):p.run_one(self.exp)
    def test_dotenv_and_frozen_policy_drift_stop(self):
        self.init();p.run_one(self.exp);self.dotenv.write_text('DEEPSEEK_API_KEY=changed-unit-secret\n')
        with mock.patch.object(p.subprocess,'Popen') as popen,self.assertRaises(p.EvalError):p.run_one(self.exp)
        popen.assert_not_called()
    def test_preflight_missing_flag_starts_nothing(self):
        self.binary.write_text(FAKE.replace("'-delivery-review'","'-missing'"));self.init()
        with self.assertRaises(p.EvalError):p.run_one(self.exp)
        self.assertFalse((self.exp/'samples').exists())
    def test_maximum_two_and_frozen_anchors_enforced(self):
        self.pair()
        with self.assertRaises(p.EvalError):p.run_one(self.exp)
        (self.exp/'harness/anchors.json').write_text('{}')
        with self.assertRaises(p.EvalError):p.summarize(self.exp)
    def test_wrong_source_for_rubric_rejected(self):
        self.pair();v=self.draft(p.ORDER[0]);v['findings'][-1]['evidence']=v['findings'][0]['evidence']
        with self.assertRaises(p.EvalError):self.normalized(v)
    def test_finite_counterexamples_and_timestamp_subjects(self):
        f.selfcheck(f.load_cases(p.CASES));f.timestamp_selfcheck(p.TIMESTAMP_CASES,p.DEFAULT_WORKSPACE)
    def test_core_unverified_cannot_be_downgraded_to_ordinary(self):
        self.pair();v=self.draft(p.ORDER[1]);row=next(f for f in v['findings'] if f['id']=='F-version_status');row.update(verdict='unverified',severity='ordinary');v['rubric_assessments']['version_status'].update(status='unverified',severity='ordinary');v['scores']['conditions']=1
        with self.assertRaises(p.EvalError):self.normalized(v)
    def test_manual_consistent_is_rejected_before_seal(self):
        self.pair();v=self.draft(p.ORDER[0]);v['reverse_check']={'consistent':True}
        with self.assertRaises(p.EvalError):p.validate_review_draft(self.exp,self.write('manual',v))
    def test_semantic_stability_catches_category_change_with_same_score(self):
        self.pair();first=self.reviews();second=copy.deepcopy(first);second[p.ORDER[1]]['findings'][-1]['issue_types']=['version_scope'];self.seal_and_import(first,second)
        value=p.summarize(self.exp);self.assertEqual(value['paired_review_protocol'],'not_run');self.assertIsNone(value['paired_quality_delta'])
    def test_phase_metrics_count_delivery_stage_once_and_keep_unknown_usage(self):
        events=[]
        for identity,stage in [('m1','main'),('m2','delivery_review')]:
            events += [{'task_id':'t','model_call_id':identity,'event_type':'model_started','payload':{'stage':stage}}, {'task_id':'t','model_call_id':identity,'event_type':'model_finished','payload':{'duration_ms':20}}]
        value=p.phase_metrics(events,'t');self.assertEqual(value['main']['model_calls'],1);self.assertEqual(value['delivery_review']['model_calls'],1);self.assertIsNone(value['delivery_review']['input_tokens']);self.assertIsNone(value['delivery_review']['cost'])
        events += [{'task_id':'t','model_call_id':'m3','event_type':'model_started','payload':{'stage':'delivery_review'}}]
        with self.assertRaises(p.EvalError):p.phase_metrics(events,'t')
    def test_missing_artifact_stays_unscored_not_zero(self):
        self.init();self.mode('blocked');p.run_one(self.exp);self.mode('');p.run_one(self.exp);self.export();mapping=p.r.read_json(self.exp/'review-map.json');self.assertEqual(len(mapping),1)
        v=self.draft(p.ORDER[1]);self.seal_and_import({p.ORDER[1]:v});s=p.summarize(self.exp);self.assertIsNone(s['paired_quality_delta']);self.assertEqual(s['candidate_quality_result'],'pass')
    def test_view_never_fills_physical_read_gap(self):
        file={'path':'a.md','sha256':'a'*64,'bytes':10}
        def read(tool,a,b):return {'kind':'action_result','payload':{'last_action':{'tool':tool,'result':{'status':'SUCCEEDED','read':{'path':'a.md','version':'a'*64,'byte_start':a,'byte_end':b,'total_bytes':10}}}}}
        self.assertFalse(p.physical_coverage([read('read_file',0,5),read('read_evidence',0,10)],[file])['complete'])
    def test_delivery_review_admission_cap_and_summary_match(self):
        state={'last_result':{'delivery_review':{'version':1,'admission_count':2}},'delivery_review':{'version':1,'admission_count':2}}
        with self.assertRaises(p.EvalError):p.validate_delivery_review_outcome(state,{})

if __name__=='__main__':unittest.main()
