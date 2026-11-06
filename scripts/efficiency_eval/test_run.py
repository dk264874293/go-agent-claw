import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("efficiency_eval_run", Path(__file__).with_name("run.py"))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)

FAKE = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,uuid
args=dict(zip(sys.argv[1::2],sys.argv[2::2]));work=pathlib.Path(args['-dir'])
if os.environ.get('EFFICIENCY_FAKE_MODE')=='infra':
 print('Authentication failure '+os.environ.get('DEEPSEEK_API_KEY',''));sys.exit(1)
task=uuid.uuid4().hex;run=uuid.uuid4().hex;folder=work/'.claw/tasks'/task;folder.mkdir(parents=True)
files=sorted(work.rglob('*.md'));usage={'model_calls':1,'tool_calls':len(files),'turns':1,'elapsed':1000000}
result={'task_id':task,'run_id':run,'status':'ANSWERED','stop_code':'answered','reason':'已答复','final_text':'固定脚本夹具，不衡量模型语义','usage':usage}
state={'version':1,'spec':{'id':task,'goal':args['-prompt'],'kind':'answer','mode':'readonly','plan':False},'session_id':args['-session'],'usage':usage,'pending':{},'last_result':result,'limits':{'MaxModelCalls':60,'MaxToolCalls':120,'MaxTurns':30,'Timeout':600000000000}}
arm=pathlib.Path(__file__).name
if arm=='candidate' and os.environ.get('EFFICIENCY_FAKE_MODE')!='missing_auto':state['spec']['delegation']='auto'
events=[]
for index,path in enumerate(files):
 data=path.read_bytes();end=len(data)
 if os.environ.get('EFFICIENCY_FAKE_MODE')=='incomplete' and index==len(files)-1:end=max(0,end-1)
 read={'path':path.relative_to(work).as_posix(),'version':hashlib.sha256(data).hexdigest(),'byte_start':0,'byte_end':end,'total_bytes':len(data),'first_line':1,'last_line':1}
 state=dict(state,last_action={'id':uuid.uuid4().hex,'tool':'read_file','result':{'status':'SUCCEEDED','read':read}})
 events.append({'version':1,'sequence':len(events)+1,'kind':'action_result','payload':state})
events.append({'version':1,'sequence':len(events)+1,'kind':'finished','payload':state})
(folder/'events.jsonl').write_text(''.join(json.dumps(e,ensure_ascii=False)+'\n' for e in events).replace('\\n','\n'))
obs=pathlib.Path(os.environ['CLAW_OBSERVE_DIR'])/uuid.uuid4().hex;obs.mkdir(parents=True)
input_tokens=1000 if pathlib.Path(__file__).name=='baseline' else 500
observations=[]
configured={'analysis':False}
if arm=='candidate' and os.environ.get('EFFICIENCY_FAKE_MODE')!='missing_configured_auto':configured['delegation']='auto'
if os.environ.get('EFFICIENCY_FAKE_MODE')=='configured_analysis_true':configured['analysis']=True
for kind,payload in [('run_configured',configured),('model_started',{}),('model_usage',{'input_tokens':input_tokens,'output_tokens':50,'cost':None}),('model_finished',{}),('run_returned',{'result_run_id':run,'status':'ANSWERED'})]:
 observations.append({'schema_version':1,'sequence':len(observations)+1,'event_type':kind,'model_call_id':'call1' if kind.startswith('model_') else '', 'task_id':task,'payload':payload})
(obs/'events.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in observations).replace('\\n','\n'))
print(os.environ.get('DEEPSEEK_API_KEY',''))
'''


class EfficiencyRunnerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        for number in range(15):
            (self.source / ("doc%02d.md" % number)).write_text("材料\n")
        (self.source / ".env").write_text("禁止归档的假密钥")
        (self.source / ".claw").mkdir()
        (self.source / ".claw" / "ignored.md").write_text("不属于题面")
        self.binary = self.root / "fake-cli"
        self.binary.write_text(FAKE)
        self.binary.chmod(0o700)
        self.experiment = self.root / "experiment"
        self.env = mock.patch.dict(os.environ, {"DEEPSEEK_API_KEY": "fixture-secret-never-real", "EFFICIENCY_FAKE_MODE": ""})
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.tmp.cleanup()

    def init(self):
        return runner.init_experiment(self.experiment, self.binary, self.binary, self.source)

    def review(self, sample, scores=None):
        runner.record_review(self.experiment, sample, scores or [2] * 5, 0, "离线假二进制，不是语义实测")

    def test_init_freezes_only_markdown_and_rejects_changes(self):
        manifest = self.init()
        self.assertEqual(len(manifest["files"]), 15)
        self.assertFalse((self.experiment / "fixture" / ".env").exists())
        (self.experiment / "fixture" / "doc00.md").write_text("改变")
        with self.assertRaises(runner.EvalError):
            runner.checked_manifest(self.experiment)

    def test_same_trajectory_budget_order_quality_gate_and_summary(self):
        self.init()
        first = runner.run_one(self.experiment)
        second = runner.run_one(self.experiment)
        self.assertEqual([first["sample_id"], second["sample_id"]], runner.ORDER[:2])
        self.assertTrue(first["coverage"]["complete"])
        self.assertIsNone(first["metrics"]["cost"])
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)
        self.review("baseline-1")
        self.review("candidate-1")
        for expected in runner.ORDER[2:]:
            item = runner.run_one(self.experiment)
            self.assertEqual(item["sample_id"], expected)
            self.review(expected)
        report = runner.summarize(self.experiment)
        self.assertTrue(report["passed"])
        self.assertGreater(report["total_token_median_reduction"], .30)
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)
        for path in self.experiment.rglob("*"):
            if path.is_file():
                self.assertNotIn(b"fixture-secret-never-real", path.read_bytes(), str(path))

    def test_candidate_failure_cannot_win_by_stopping_early(self):
        self.init()
        runner.run_one(self.experiment)
        os.environ["EFFICIENCY_FAKE_MODE"] = "incomplete"
        candidate = runner.run_one(self.experiment)
        self.assertFalse(candidate["coverage"]["complete"])
        self.assertFalse(runner.candidate_gate(candidate))
        self.review("baseline-1")
        self.review("candidate-1")
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)
        self.assertFalse(runner.summarize(self.experiment)["passed"])

    def test_infrastructure_failure_stops_without_retry(self):
        self.init()
        os.environ["EFFICIENCY_FAKE_MODE"] = "infra"
        sample = runner.run_one(self.experiment)
        self.assertIn("infrastructure_error", sample)
        self.assertFalse(sample["protocol_valid"])
        self.assertTrue(runner.read_json(self.experiment / "state.json")["stopped"])
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)
        self.assertEqual(len(runner.read_json(self.experiment / "state.json")["samples"]), 1)

    def test_unconfirmed_sample_and_binary_mutation_refuse_run(self):
        self.init()
        state = runner.read_json(self.experiment / "state.json")
        state["samples"]["baseline-1"] = {"state": "running"}
        runner.write_json(self.experiment / "state.json", state)
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)
        (self.experiment / "binaries" / "baseline").write_bytes(b"different")
        with self.assertRaises(runner.EvalError):
            runner.checked_manifest(self.experiment)

    def test_quality_does_not_regress_to_baseline(self):
        self.init()
        runner.run_one(self.experiment)
        runner.run_one(self.experiment)
        self.review("baseline-1")
        self.review("candidate-1", [2, 2, 2, 2, 1])
        with self.assertRaises(runner.EvalError):
            runner.run_one(self.experiment)

    def test_usage_deduplicates_and_unknown_is_null(self):
        events = [{"event_type": name, "model_call_id": "call", "task_id": "task", "payload": payload}
                  for name, payload in [("model_started", {}), ("model_finished", {}),
                                        ("model_usage", {"input_tokens": 10, "output_tokens": 5, "cost": None})]]
        events.append(copy.deepcopy(events[-1]))
        metrics = runner.usage_metrics(events, "task", 1)
        self.assertEqual(metrics["total_tokens"], 15)
        self.assertIsNone(metrics["cost"])
        metrics = runner.usage_metrics(events[:2], "task", 1)
        self.assertIsNone(metrics["total_tokens"])
        self.assertEqual(metrics["unknown_usage_calls"], 1)

    def test_review_cannot_race_a_running_sample(self):
        self.init()
        runner.run_one(self.experiment)
        (self.experiment / "run.lock").write_text("正在运行")
        with self.assertRaises(runner.EvalError):
            self.review("baseline-1")
        self.assertNotIn("review", runner.read_json(self.experiment / "state.json")["samples"]["baseline-1"])

    def test_candidate_effective_configuration_must_be_auto_without_analysis(self):
        for mode in ["missing_auto", "missing_configured_auto", "configured_analysis_true"]:
            with self.subTest(mode=mode):
                self.experiment = self.root / mode
                self.init()
                os.environ["EFFICIENCY_FAKE_MODE"] = ""
                baseline = runner.run_one(self.experiment)
                self.assertTrue(baseline["protocol_valid"])
                self.assertIsNone(baseline["effective_config"]["delegation"])
                os.environ["EFFICIENCY_FAKE_MODE"] = mode
                candidate = runner.run_one(self.experiment)
                self.assertFalse(candidate["protocol_valid"])
                self.assertIn("infrastructure_error", candidate)
                self.assertTrue(runner.read_json(self.experiment / "state.json")["stopped"])


if __name__ == "__main__":
    unittest.main()
