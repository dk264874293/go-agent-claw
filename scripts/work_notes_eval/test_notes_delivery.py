"""双样本控制与独立反例；假CLI仅验协议，不是真实模型采纳或质量成绩。"""
import copy
import importlib.util
from pathlib import Path
import json
import subprocess
import sys
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('delivery_profile_tests', Path(__file__).with_name('test_run.py'))
old = importlib.util.module_from_spec(spec); spec.loader.exec_module(old)
r = old.r
spec = importlib.util.spec_from_file_location('delivery_cases', Path(__file__).with_name('delivery_cases.py'))
cases = importlib.util.module_from_spec(spec); spec.loader.exec_module(cases)


class NotesDeliveryTests(unittest.TestCase):
    setUp = old.ProfileTests.setUp
    tearDown = old.ProfileTests.tearDown
    mode = old.ProfileTests.mode
    review = old.ProfileTests.review

    def init(self):
        fake = old.FAKE.replace("'read_context_version':1", "'read_context_version':2")
        fake = fake.replace('events=[]', '''
if pathlib.Path(__file__).name=='candidate' and mode!='no-notes':
 state['read_memory']={'version':1,'evidence':{'E1':{'receipt':{'path':files[0].relative_to(work).as_posix()}}},'processed_reads':{'agent':['E1']},'summaries':{'agent':{'E1':'仅为单测的摘要'}}}
events=[]''')
        fake = fake.replace("{'model':args['-model']}", "{'model':args['-model'],'folded_source_bytes':50 if pathlib.Path(__file__).name=='candidate' and mode not in ('no-notes','no-fold') else 0}")
        self.binary.write_text(fake)
        return r.init_experiment(self.exp, self.binary, self.binary, profile='notes_delivery_v1')

    def review_data(self, name):
        data = old.ProfileTests.review_data(self, name)
        data['quality_checks'] = {key:dict(verdict='supported',reason='仅验证评审绑定的单测占位',finding_ids=['f1']) for key in r.QUALITY_CHECKS}
        return data

    def test_two_samples_are_separate_from_default_stopped_history(self):
        manifest = self.init()
        self.assertEqual(manifest['profile'], 'notes_delivery_v1')
        self.assertEqual(manifest['order'], r.DEFAULT_ORDER[:2]); self.assertEqual(manifest['max_model_admissions'], 120)
        self.assertIn('delivery_cases.json', manifest['harness'])
        for source in manifest['files']: self.assertNotIn(source['path'], manifest['goal'])
        self.assertNotIn('15', manifest['goal']); self.assertEqual(len(manifest['files']), 15)
        baseline, candidate = r.run_one(self.exp), r.run_one(self.exp)
        self.assertNotIn('infrastructure_error', baseline); self.assertEqual(baseline['metrics']['summary_count'], 0)
        self.assertEqual(candidate['metrics']['processed_summary_sources'], 1)
        self.review(r.r.ORDER[0]); self.review(r.r.ORDER[1])
        report = r.summarize(self.exp)
        self.assertTrue(report['passed'], report); self.assertTrue(report['notes_gate_pass']); self.assertEqual(report['max_model_admissions'],120)
        frozen = subprocess.run([sys.executable,str(self.exp/'harness/run.py'),'summarize','--experiment',str(self.exp)],capture_output=True,text=True,check=True,cwd=self.root)
        self.assertEqual(json.loads(frozen.stdout)['max_model_admissions'],120)
        with self.assertRaises(r.EvalError): r.run_one(self.exp)
        # 默认profile仍是原6例；停止记录不可被新profile接续。
        self.exp = self.root / 'default'; manifest = r.init_experiment(self.exp, self.binary, self.binary)
        self.assertEqual(manifest['order'], r.DEFAULT_ORDER); self.assertEqual(manifest['max_model_admissions'],360)
        state = r.r.read_json(self.exp / 'state.json'); state['stopped']=True; r.r.write_json(self.exp/'state.json',state)
        with self.assertRaises(r.EvalError): r.run_one(self.exp)

    def test_missing_notes_or_missing_fold_stops_without_retry(self):
        for mode in ('no-notes','no-fold'):
            self.exp=self.root/mode;self.init();r.run_one(self.exp);self.mode(mode);item=r.run_one(self.exp)
            self.assertFalse(r.candidate_gate(item));self.assertTrue(r.r.read_json(self.exp/'state.json')['stopped'])
            self.assertFalse(r.summarize(self.exp)['notes_gate_pass'])
            with self.assertRaises(r.EvalError):r.run_one(self.exp)

    def test_call_counts_cannot_exceed_baseline_and_independent_quality_is_required(self):
        self.init();r.run_one(self.exp);r.run_one(self.exp)
        self.assertEqual(r.summarize(self.exp)['quality_result'],'not_run')
        self.assertFalse(r.within_baseline_budget({'metrics':{'model_calls':2,'tool_calls':3}}, {'metrics':{'model_calls':1,'tool_calls':4}}))
        self.assertFalse(r.within_baseline_budget({'metrics':{'model_calls':1,'tool_calls':5}}, {'metrics':{'model_calls':1,'tool_calls':4}}))
        data=self.review_data(r.r.ORDER[1]);data['quality_checks']['formula_endpoints']['verdict']='incorrect'
        self.review(r.r.ORDER[0]);self.review(r.r.ORDER[1],data)
        self.assertEqual(r.summarize(self.exp)['quality_result'],'fail');self.assertTrue(r.r.read_json(self.exp/'state.json')['stopped'])

    def test_notes_mechanism_pass_and_efficiency_failure_are_separate(self):
        self.init();r.run_one(self.exp);r.run_one(self.exp)
        self.review(r.r.ORDER[0]);self.review(r.r.ORDER[1])
        _,manifest=r.checked_manifest(self.exp)
        samples=copy.deepcopy(r.check_history(self.exp,manifest))
        baseline,candidate=samples[r.r.ORDER[0]],samples[r.r.ORDER[1]]
        baseline['metrics'].update(model_calls=11,tool_calls=52,total_tokens=412210)
        candidate['metrics'].update(model_calls=15,tool_calls=83,total_tokens=730329)
        self.assertTrue(r.notes_mechanism_ok(candidate))
        self.assertFalse(r.candidate_gate(candidate))
        # 只替换临时测试副本，不改逐例档案或冻结harness。
        with mock.patch.object(r,'check_history',return_value=samples):report=r.summarize(self.exp)
        self.assertTrue(report['notes_gate_pass'])
        self.assertEqual(report['quality_result'],'pass')
        self.assertEqual(report['efficiency_result'],'fail')
        self.assertFalse(report['passed'])

    def test_response_adoption_uses_unique_opportunities_not_tool_missing(self):
        events=[]
        def emit(kind,payload,call):events.append(dict(task_id='task',event_type=kind,payload=payload,model_call_id=call))
        accepted=dict(notes_candidates=2,notes_opportunity=True,notes_response_accepted=True,error_class='accepted',notes_processed_sources=1)
        emit('work_notes_response',accepted,'m1');emit('work_notes_response',accepted,'m1')
        emit('work_notes_response',dict(notes_opportunity=True,error_class='missing'),'m2')
        emit('work_notes_response',dict(notes_opportunity=True,error_class='duplicate',notes_response_accepted=False),'m3')
        emit('work_notes_response',dict(notes_opportunity=False,error_class='no_eligible_tool'),'m4')
        for _ in range(20):emit('work_notes_processed',dict(notes_missing=1),'m1')
        metric=r.derive_metrics(events,[],'task',True,15)
        self.assertEqual(metric['notes_response_count'],4);self.assertEqual(metric['notes_acceptance_denominator'],3)
        self.assertEqual(metric['notes_accepted_responses'],1);self.assertEqual(metric['notes_acceptance_rate'],1/3)
        self.assertEqual(metric['missing_notes'],20)
        emit('work_notes_response',dict(notes_opportunity=False),'m1')
        with self.assertRaises(r.EvalError):r.derive_metrics(events,[],'task',True,15)


class DeliveryCaseTests(unittest.TestCase):
    def test_classification_endpoints_and_bad_link_regressions_are_finite(self):
        fixture=cases.load_cases();result=cases.selfcheck(fixture)
        self.assertEqual(result['selfcheck_result'],'pass');self.assertEqual(result['provider_calls'],0)
        self.assertEqual(cases.evaluate_claim('新措辞，不能靠关键词判定',fixture),'not_run')
        self.assertEqual(len(fixture['claims']),6)
        for field in ('expected_classification','formulas','sources','sanitized_symptom'):
            changed=copy.deepcopy(fixture)
            if field=='expected_classification':changed[field]['丙']['教程']=1
            elif field=='formulas':changed[field][0]['expected_minimum_input']='0'
            elif field=='sources':changed[field][0]['content']+='伪造正文'
            else:changed[field]['complete_historical_replay']=True
            with self.subTest(field=field),self.assertRaises(ValueError):
                cases.inspect_cases(changed)


if __name__=='__main__':unittest.main()
