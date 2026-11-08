import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location("write_efficiency_run",Path(__file__).with_name("run.py"));r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
spec=importlib.util.spec_from_file_location("write_efficiency_checker",Path(__file__).with_name("checker.py"));c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
FAKE=r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,uuid
args=dict(zip(sys.argv[1::2],sys.argv[2::2]));work=pathlib.Path(args['-dir']);mode=os.environ.get('WRITE_EVAL_FAKE','')
if mode=='infra':print(os.environ.get('DEEPSEEK_API_KEY',''));sys.exit(1)
task=uuid.uuid4().hex;run=uuid.uuid4().hex;folder=work/'.claw/tasks'/task;folder.mkdir(parents=True)
files=sorted(work.rglob('*.md'));content='# 优化建议\n版本V1/V2与默认score需同步迁移，JSON契约需统一，运行结果待实测。\n'*15
for f in files[:3]:content+='引用：'+f.relative_to(work).as_posix()+':1\n'
(work/'优化汇总.md').write_text(content)
output=hashlib.sha256(content.encode()).hexdigest();mc=13 if mode=='expensive' else 1
usage={'model_calls':mc,'tool_calls':len(files)+1,'turns':mc,'elapsed':1000000}
effect={'action_id':'write-action','tool':'write_report' if '-report' in args else 'write_file','status':'SUCCEEDED','effect':'CONFIRMED','file':{'path':'优化汇总.md','before_version':'missing','after_version':output}}
result={'task_id':task,'run_id':run,'status':'NEEDS_VERIFICATION','stop_code':'needs_verification','reason':'没有验收','usage':usage,'delivery':{'history_complete':True,'effects':[effect]}}
state={'version':1,'spec':{'id':task,'goal':args['-prompt'],'kind':'change','mode':'workspace','plan':False},'session_id':args['-session'],'usage':usage,'pending':{},'last_result':result,'limits':{'MaxModelCalls':60,'MaxToolCalls':120,'MaxTurns':30,'Timeout':600000000000}}
if pathlib.Path(__file__).name=='candidate':state['spec']['read_context_version']=1;state['spec']['read_page_bytes']=8000
if '-report' in args:
 state['spec']['report']={'target_path':'优化汇总.md','create_only':True,'version':1,'format':'evidence_v1'}
 state['report']={'status':'published','report_id':'report-fixture','output_version':output,'reference_result':'pass','semantic_result':'not_run'}
 result['report']=dict(state['report'],target_path='优化汇总.md',origin_run_id=run)
events=[]
for i,p in enumerate(files):
 data=p.read_bytes();end=len(data)
 if mode=='incomplete' and i==len(files)-1:end-=1
 read={'path':p.relative_to(work).as_posix(),'version':hashlib.sha256(data).hexdigest(),'byte_start':0,'byte_end':end,'total_bytes':len(data)}
 state=dict(state,last_action={'id':uuid.uuid4().hex,'tool':'read_file','result':{'status':'SUCCEEDED','read':read}})
 events.append({'version':1,'sequence':len(events)+1,'kind':'action_result','payload':state})
if mode=='scope':files[0].write_text('非法修改')
events.append({'version':1,'sequence':len(events)+1,'kind':'finished','payload':state})
(folder/'events.jsonl').write_text(''.join(json.dumps(x,ensure_ascii=False)+'\n' for x in events))
obs=pathlib.Path(os.environ['CLAW_OBSERVE_DIR'])/uuid.uuid4().hex;obs.mkdir(parents=True)
observations=[]
def emit(kind,payload,call=''):
 observations.append({'schema_version':1,'sequence':len(observations)+1,'event_type':kind,'model_call_id':call,'task_id':task,'payload':payload})
for n in range(mc):
 call='call'+str(n);emit('model_started',{'model':args['-model']},call);emit('model_usage',{'input_tokens':1000 if pathlib.Path(__file__).name=='baseline' else 500,'output_tokens':50,'cost':None},call);emit('model_finished',{},call)
emit('run_finalized',{'result_run_id':run,'status':'NEEDS_VERIFICATION'});emit('run_returned',{'result_run_id':run,'status':'NEEDS_VERIFICATION'})
(obs/'events.jsonl').write_text(''.join(json.dumps(x)+'\n' for x in observations));sys.exit(2)
'''

class RunnerTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.source=self.root/'source';self.source.mkdir()
  for i in range(15):(self.source/('doc%02d.md'%i)).write_text('原文证据\n默认score为5\nV2合法状态review\n')
  (self.source/'.env').write_text('DEEPSEEK_API_KEY=must-not-copy')
  self.binary=self.root/'fake';self.binary.write_text(FAKE);self.binary.chmod(0o700);self.experiment=self.root/'experiment'
  self.env=mock.patch.dict(os.environ,{'DEEPSEEK_API_KEY':'unit-fixture-not-real','WRITE_EVAL_FAKE':''});self.env.start()
  self.preflight=mock.patch.object(r,'preflight',lambda root:None);self.preflight.start()
  self.checker=mock.patch.object(r,'run_checker',lambda root,sample,m:c.inspect(sample/'workspace',m['files']));self.checker.start()
 def tearDown(self):self.checker.stop();self.preflight.stop();self.env.stop();self.tmp.cleanup()
 def init(self):return r.init_experiment(self.experiment,self.binary,self.binary,self.source)
 def review(self,name,scores=None):
  data={'reviewer':'unit-test-fixture','method':'source_audit_agent','scores':dict(zip(r.SCORES,scores or [2]*5)),'score_basis':dict.fromkeys(r.SCORES,'单元夹具身份与流程验证，不是实际语义评分'),'major_errors':0,'findings':[{'claim':'单元夹具原文','verdict':'supported','evidence':[{'path':'doc00.md','start_line':1,'end_line':1,'quote':'原文证据'}]}]}
  p=self.root/'review.json';r.write_json(p,data);return r.record_review(self.experiment,name,p)
 def test_freeze_and_changed_input(self):
  m=self.init();self.assertEqual(len(m['files']),15);self.assertFalse((self.experiment/'fixture/.env').exists())
  (self.experiment/'fixture/doc00.md').write_text('改变')
  with self.assertRaises(r.EvalError):r.checked_manifest(self.experiment)
 def test_full_order_review_gate_and_separate_groups(self):
  self.init();first=r.run_one(self.experiment);second=r.run_one(self.experiment)
  self.assertTrue(r.mechanical_ok(first));self.assertTrue(r.candidate_gate(second));self.assertIsNone(first['metrics']['cost'])
  with self.assertRaises(r.EvalError):r.run_one(self.experiment)
  self.review(r.ORDER[0]);self.review(r.ORDER[1])
  for name in r.ORDER[2:]:
   item=r.run_one(self.experiment);self.assertEqual(item['sample_id'],name);self.review(name)
  out=r.summarize(self.experiment);self.assertTrue(out['passed']);self.assertEqual(out['groups']['ordinary']['candidate']['samples'],2);self.assertEqual(out['groups']['report']['candidate']['samples'],1)
  self.assertAlmostEqual(out['groups']['ordinary']['token_median_reduction'],1-550/1050)
  with self.assertRaises(r.EvalError):r.run_one(self.experiment)
  # 汇总不依赖可重建索引；保留逐例文件即可离线重算。
  (self.experiment/'state.json').unlink();self.assertTrue(r.summarize(self.experiment)['passed'])
 def test_scope_failure_stops_and_checker_still_runs(self):
  self.init();os.environ['WRITE_EVAL_FAKE']='scope';item=r.run_one(self.experiment)
  self.assertFalse(item['scope_pass']);self.assertEqual(item['checker']['scope_result'],'fail');self.assertTrue(r.read_json(self.experiment/'state.json')['stopped'])
  with self.assertRaises(r.EvalError):r.run_one(self.experiment)
 def test_incomplete_coverage_and_expensive_candidate_cannot_expand(self):
  for mode in ['incomplete','expensive']:
   self.experiment=self.root/mode;self.init();r.run_one(self.experiment);os.environ['WRITE_EVAL_FAKE']=mode;item=r.run_one(self.experiment)
   self.assertFalse(r.candidate_gate(item))
   with self.assertRaises(r.EvalError):r.run_one(self.experiment)
   os.environ['WRITE_EVAL_FAKE']=''
 def test_review_requires_real_identity_and_actual_source_quote(self):
  self.init();r.run_one(self.experiment)
  bad={'reviewer':'audit','method':'human','scores':dict.fromkeys(r.SCORES,2),'score_basis':dict.fromkeys(r.SCORES,'依据'),'major_errors':0,'findings':[{'claim':'错误摘录','verdict':'supported','evidence':[{'path':'doc00.md','start_line':1,'end_line':1,'quote':'并不存在'}]}]}
  p=self.root/'bad-review.json';r.write_json(p,bad)
  with self.assertRaises(r.EvalError):r.record_review(self.experiment,r.ORDER[0],p)
  self.assertNotIn('review',r.read_json(self.experiment/'state.json')['samples'][r.ORDER[0]])
 def test_infrastructure_no_retry_and_unconfirmed_no_replay(self):
  self.init();os.environ['WRITE_EVAL_FAKE']='infra';item=r.run_one(self.experiment);self.assertTrue(item.get('infrastructure_error'))
  with self.assertRaises(r.EvalError):r.run_one(self.experiment)
  stored=(self.experiment/'samples'/r.ORDER[0]/'stdout.log').read_text();self.assertNotIn('unit-fixture-not-real',stored)
 def test_unknown_usage_and_cost_not_zero(self):
  ev=[{'event_type':k,'model_call_id':'a','task_id':'t','payload':{}} for k in ['model_started','model_finished']]
  x=r.usage_metrics(ev,'t',1);self.assertIsNone(x['total_tokens']);self.assertIsNone(x['cost'])
 def test_checker_rejects_historical_wrong_file_line(self):
  files=r.file_list(self.source,only_markdown=True)
  target=self.source/r.OUTPUT;target.write_text('# 优化建议\n版本V1和默认score，JSON契约仍待实测。\n'*20+'doc00.md:1\ndoc01.md:1\ndoc02.md:1\n')
  self.assertEqual(c.inspect(self.source,files)['artifact_result'],'fail') # 源工作区.env不是许可产物。
  (self.source/'.env').unlink();self.assertEqual(c.inspect(self.source,files)['artifact_result'],'pass')
  target.write_text(target.read_text()+'doc00.md:161-175\n');self.assertEqual(c.inspect(self.source,files)['artifact_result'],'fail')
 def test_major_error_or_quality_regression_blocks_expansion(self):
  self.init();r.run_one(self.experiment);r.run_one(self.experiment);self.review(r.ORDER[0]);self.review(r.ORDER[1],[2,2,2,2,1])
  with self.assertRaises(r.EvalError):r.run_one(self.experiment)
 def test_checker_unknown_citation_cannot_hide_among_valid_ones(self):
  (self.source/'.env').unlink();files=r.file_list(self.source,only_markdown=True)
  text='# 优化建议\n版本V1和默认score，JSON契约仍待实测。\n'*20+'引用：doc00.md:1\n`doc01.md:1`\ndoc02.md:1\n'
  out=self.source/r.OUTPUT
  for extra in ['引用：fake/path.md:1\n','`ghost.md:1`\n','invented/doc00.md:1\n','doc00.md:0\n']:
   out.write_text(text+extra);checked=c.inspect(self.source,files)
   self.assertEqual(checked['artifact_result'],'fail');self.assertFalse(checked['checks']['citation_ranges'])
  out.write_text(text);self.assertEqual(c.inspect(self.source,files)['artifact_result'],'pass')
 def test_checker_handles_renderer_escaped_path(self):
  (self.source/'.env').unlink();(self.source/'doc00.md').rename(self.source/'doc_00.md');files=r.file_list(self.source,only_markdown=True)
  text='# 优化建议\n版本V1和默认score，JSON契约仍待实测。\n'*20+'  引用：doc\\_00.md:1\n  引用：doc01.md:1\n  引用：doc02.md:1\n'
  (self.source/r.OUTPUT).write_text(text);self.assertEqual(c.inspect(self.source,files)['artifact_result'],'pass')
 def test_preflight_failure_starts_no_paid_process(self):
  self.init()
  with mock.patch.object(r,'preflight',side_effect=r.EvalError('沙箱预检失败')),mock.patch.object(r.subprocess,'Popen') as popen:
   with self.assertRaises(r.EvalError):r.run_one(self.experiment)
   popen.assert_not_called()
  self.assertTrue(r.read_json(self.experiment/'state.json')['stopped'])
 def test_checker_complete_filename_can_contain_comma_and_multiple_md(self):
  (self.source/'.env').unlink();name='docs/Memory 工程，AGENTS.md 的重写（V2）.md'
  path=self.source/name;path.parent.mkdir();(self.source/'doc00.md').rename(path)
  files=r.file_list(self.source,only_markdown=True)
  text='# 优化建议\n版本V1和默认score，JSON契约仍待实测。\n'*20+'引用：'+name+':1\n引用：doc01.md:1\n引用：doc02.md:1\n'
  out=self.source/r.OUTPUT;out.write_text(text);self.assertEqual(c.inspect(self.source,files)['artifact_result'],'pass')
  out.write_text(text+'引用：invented/'+name+':1\n');self.assertEqual(c.inspect(self.source,files)['artifact_result'],'fail')
 def test_summary_allows_updated_working_harness_but_never_frozen_tampering(self):
  self.init();r.run_one(self.experiment)
  updated=self.root/'updated-current-run.py';updated.write_text(Path(r.__file__).read_text()+'\n# 当前工作代码已更新\n')
  inputs=r.harness_inputs();inputs['run.py']=updated
  with mock.patch.object(r,'harness_inputs',lambda:inputs),mock.patch.object(r.subprocess,'Popen') as popen:
   summary=r.summarize(self.experiment)
   self.assertEqual(list(summary['samples']),['baseline-ordinary-1']);self.assertFalse(summary['passed'])
   with self.assertRaises(r.EvalError):r.run_one(self.experiment)
   popen.assert_not_called()
   frozen=self.experiment/'harness/run.py';frozen.write_text(frozen.read_text()+'\n# 归档被篡改\n')
   with self.assertRaises(r.EvalError):r.summarize(self.experiment)
   with self.assertRaises(r.EvalError):r.run_one(self.experiment)
   popen.assert_not_called()

@unittest.skipUnless(r.sys.platform == 'darwin' and Path('/usr/bin/sandbox-exec').exists(), '需要真实macOS沙箱')
class HomeSandboxTests(unittest.TestCase):
 def setUp(self):
  base=Path.home()/'.claw';base.mkdir(exist_ok=True)
  self.tmp=tempfile.TemporaryDirectory(prefix='write-eval-sandbox-test-',dir=base)
  self.base=Path(self.tmp.name);self.root=self.base/'experiment'
  binary=self.base/'unused-binary';binary.write_text('#!/bin/sh\nexit 99\n');binary.chmod(0o700)
  self.manifest=r.init_experiment(self.root,binary,binary)
 def tearDown(self):self.tmp.cleanup()
 def test_home_preflight_executes_frozen_checker_and_keeps_ancestor_data_denied(self):
  r.preflight(self.root)
  checked=r.read_json(self.root/'checker-runtime/isolation-preflight.json')
  self.assertEqual(checked['returncode'],0);self.assertEqual(checked['ancestor_metadata'],'literal-only');self.assertEqual(checked['ancestor_data'],'denied')
  results=list((self.root/'checker-runtime').glob('preflight-*/checker.json'))
  self.assertEqual(len(results),1);self.assertEqual(r.read_json(results[0])['artifact_result'],'pass')
  command=r.read_json(results[0].with_name('checker-command.json'))
  self.assertIn('(allow file-read-metadata ',command['profile'])
  self.assertNotIn('(allow file-read* (subpath '+json.dumps(str(Path.home()))+')',command['profile'])
 def test_socket_success_does_not_hide_frozen_checker_failure(self):
  checker=self.root/'harness/checker.py';checker.write_text('raise RuntimeError("frozen checker was executed")\n')
  # 仅修改本测试临时实验，使预检确实执行其冻结输入；不触碰真实失败批次。
  self.manifest['harness']['checker.py']=r.digest(checker);r.write_json(self.root/'manifest.json',self.manifest)
  with self.assertRaises(r.EvalError):r.preflight(self.root)
  self.assertEqual(r.read_json(self.root/'checker-runtime/isolation-preflight.json')['returncode'],0)
  errors=list((self.root/'checker-runtime').glob('preflight-*/checker.stderr.log'))
  self.assertTrue(errors);self.assertIn('frozen checker was executed',errors[0].read_text())
 def test_offline_original_recheck_preserves_stopped_batch_and_frozen_checker(self):
  sample=self.root/'samples/baseline-ordinary-1';shutil=r.shutil
  shutil.copytree(self.root/'fixture',sample/'workspace')
  text='# 离线重检夹具\n版本V1/V2、默认score、JSON契约的优化建议均待实测。\n'*20
  for file in self.manifest['files'][:3]:text+='引用：'+file['path']+':1\n'
  (sample/'workspace'/r.OUTPUT).write_text(text)
  original={'state':'completed','sample_id':'baseline-ordinary-1','infrastructure_error':'原checker PermissionError'}
  r.write_json(sample/'result.json',original);r.write_json(self.root/'state.json',{'stopped':True,'stop_reason':'原基础设施失败','samples':{'baseline-ordinary-1':original}})
  (sample/'checker.stderr.log').write_text('原PermissionError证据')
  # 模拟归档旧runner与当前修复源码摘要不同，正常续跑应拒绝，离线审计只允许冻结checker。
  old=self.root/'harness/run.py';old.write_text(old.read_text()+'\n# frozen old runner\n')
  self.manifest['harness']['run.py']=r.digest(old);r.write_json(self.root/'manifest.json',self.manifest)
  with self.assertRaises(r.EvalError):r.checked_manifest(self.root)
  protected={p:p.read_bytes() for p in [self.root/'manifest.json',self.root/'state.json',sample/'result.json',sample/'checker.stderr.log']}
  result=r.recheck_original(self.root,'baseline-ordinary-1')
  self.assertEqual(result['artifact_result'],'pass');self.assertEqual(result['semantic_result'],'not_run')
  self.assertTrue(all(p.read_bytes()==data for p,data in protected.items()))
  evidence=r.read_json(sample/'checker-repaired.evidence.json')
  self.assertTrue(evidence['original_records_unchanged']);self.assertEqual(evidence['paid_calls'],0);self.assertFalse(evidence['agent_started'])
  self.assertEqual(evidence['checker_sha256'],self.manifest['harness']['checker.py'])
  self.assertTrue(r.read_json(self.root/'state.json')['stopped'])
  with self.assertRaises(r.EvalError):r.recheck_original(self.root,'baseline-ordinary-1')

if __name__=='__main__':unittest.main()
