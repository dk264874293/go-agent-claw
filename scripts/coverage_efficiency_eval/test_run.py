"""只使用本地脚本二进制验证 v2 批次协议，不发送模型请求。"""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


wrapper = module("coverage_efficiency_v2", Path(__file__).with_name("run.py"))
r = wrapper.r
previous_tests = module("write_efficiency_v1_test_fixtures", r.REPO / "scripts/write_efficiency_eval/test_run.py")
c = previous_tests.c

# 复用已验收的 v1 本地假二进制，仅扩展 v2 新增的重复参数和结果身份。
old_parser = "args=dict(zip(sys.argv[1::2],sys.argv[2::2]));work=pathlib.Path(args['-dir']);mode=os.environ.get('WRITE_EVAL_FAKE','')"
new_parser = """args={};scopes=[];i=1
while i<len(sys.argv):
 key=sys.argv[i];i+=1
 if key=='-require-full-read':args[key]=True;continue
 value=sys.argv[i];i+=1
 if key=='-read-scope':scopes.append(value)
 else:args[key]=value
work=pathlib.Path(args['-dir']);mode=os.environ.get('WRITE_EVAL_FAKE','')"""
assert old_parser in previous_tests.FAKE
FAKE = previous_tests.FAKE.replace(old_parser, new_parser)
FAKE = FAKE.replace("'input_tokens':1000 if pathlib.Path(__file__).name=='baseline' else 500", "'input_tokens':1000 if pathlib.Path(__file__).name=='baseline' or mode=='no-reduction' else 500")
FAKE = FAKE.replace(
    "events=[]\nfor i,p in enumerate(files):",
    """if pathlib.Path(__file__).name=='candidate':
 state['spec']['output_style']=args.get('-output-style')
 if mode!='missing-scope':state['spec']['read_scope']={'version':1,'paths':sorted(scopes),'name_glob':'*','require_complete':args.get('-require-full-read') is True}
 scope_files=[]
 for p in files:
  data=p.read_bytes();scope_files.append({'path':p.relative_to(work).as_posix(),'version':hashlib.sha256(data).hexdigest(),'total_bytes':len(data),'read_bytes':len(data),'complete':True,'missing':[]})
 result['reading']={'scope_kind':'declared','scope_id':hashlib.sha256(b'fixture-scope').hexdigest(),'scope_revision':hashlib.sha256(b'fixture-revision').hexdigest(),'manifest_ids':[],'status':'complete','files':scope_files,'complete_files':15,'total_files':15}
 if mode=='false-coverage':result['reading']['complete_files']=3;result['reading']['total_files']=3;result['reading']['files']=scope_files[:3]
events=[]
for i,p in enumerate(files):""",
)


class CoverageRunnerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        for i in range(15):
            (self.source / ("doc%02d.md" % i)).write_text("原文证据\n默认score为5\nV2合法状态review\n")
        (self.source / ".env").write_text("DEEPSEEK_API_KEY=must-not-copy")
        self.binary = self.root / "fake"
        self.binary.write_text(FAKE)
        self.binary.chmod(0o700)
        self.experiment = self.root / "experiment"
        self.env = mock.patch.dict(os.environ, {"DEEPSEEK_API_KEY": "unit-fixture-not-real", "WRITE_EVAL_FAKE": ""})
        self.env.start()
        self.preflight = mock.patch.object(r, "preflight", lambda root: None)
        self.preflight.start()
        self.checker = mock.patch.object(r, "run_checker", lambda root, sample, m: c.inspect(sample / "workspace", m["files"]))
        self.checker.start()

    def tearDown(self):
        self.checker.stop()
        self.preflight.stop()
        self.env.stop()
        self.tmp.cleanup()

    def init(self):
        return r.init_experiment(self.experiment, self.binary, self.binary, self.source)

    def review(self, name, scores=None):
        data = {
            "reviewer": "unit-test-fixture", "method": "source_audit_agent",
            "scores": dict(zip(r.SCORES, scores or [2] * 5)),
            "score_basis": dict.fromkeys(r.SCORES, "仅验证本地夹具的评测协议，不是实际语义成绩"),
            "major_errors": 0,
            "findings": [{"claim": "单元夹具原文", "verdict": "supported", "evidence": [
                {"path": "doc00.md", "start_line": 1, "end_line": 1, "quote": "原文证据"}
            ]}],
        }
        path = self.root / "review.json"
        r.write_json(path, data)
        return r.record_review(self.experiment, name, path)

    def pair(self):
        return r.run_one(self.experiment), r.run_one(self.experiment)

    def test_four_samples_240_admissions_and_pairwise_modes(self):
        manifest = self.init()
        self.assertEqual(manifest["schema_version"], 2)
        self.assertEqual(manifest["max_samples"], 4)
        self.assertEqual(manifest["max_model_admissions"], 240)
        self.assertEqual(manifest["order"], ["baseline-ordinary-1", "candidate-ordinary-1", "candidate-report-1", "baseline-report-1"])
        self.assertIn("精简", manifest["goal"])
        self.assertIn("doc00.md", manifest["goal"])
        first, second = self.pair()
        self.assertTrue(r.mechanical_ok(first))
        self.assertTrue(r.candidate_gate(second))
        self.assertIsNone(second["metrics"]["cost"])
        self.assertNotIn("-read-scope", first["command"])
        self.assertNotIn("-report", second["command"])
        self.assertEqual(second["command"].count("-read-scope"), 15)
        self.assertIn("-require-full-read", second["command"])
        for item in [first, second]:
            self.assertEqual(item["command"][item["command"].index("-prompt") + 1], manifest["goal"])
        self.review(r.ORDER[0])
        self.review(r.ORDER[1])
        for name in r.ORDER[2:]:
            item = r.run_one(self.experiment)
            self.assertEqual(item["sample_id"], name)
            self.assertIn("-report", item["command"])
            self.assertEqual(item["report_check"], "pass")
            self.review(name)
        summary = r.summarize(self.experiment)
        self.assertTrue(summary["passed"])
        for group in ["ordinary", "report"]:
            self.assertEqual(summary["groups"][group]["baseline"]["samples"], 1)
            self.assertEqual(summary["groups"][group]["candidate"]["samples"], 1)
            self.assertAlmostEqual(summary["groups"][group]["token_median_reduction"], 1 - 550 / 1050)
        with self.assertRaises(r.EvalError):
            r.run_one(self.experiment)

    def test_declared_scope_is_required_on_candidate(self):
        self.init()
        r.run_one(self.experiment)
        os.environ["WRITE_EVAL_FAKE"] = "missing-scope"
        item = r.run_one(self.experiment)
        self.assertFalse(item.get("protocol_valid"))
        self.assertTrue(item.get("infrastructure_error"))
        self.assertTrue(r.read_json(self.experiment / "state.json")["stopped"])
        self.assertEqual(item["metrics"]["model_calls"], 1)
        with self.assertRaises(r.EvalError):
            r.run_one(self.experiment)

    def test_false_coverage_display_is_protocol_error_and_keeps_usage(self):
        for mode in ["false-coverage", "incomplete"]:
            self.experiment = self.root / mode
            self.init()
            os.environ["WRITE_EVAL_FAKE"] = ""
            r.run_one(self.experiment)
            os.environ["WRITE_EVAL_FAKE"] = mode
            item = r.run_one(self.experiment)
            self.assertFalse(item.get("protocol_valid"))
            self.assertTrue(item.get("infrastructure_error"))
            self.assertEqual(item["metrics"]["model_calls"], 1)
            self.assertEqual(item["metrics"]["input_tokens"], 500)
            self.assertEqual(item["metrics"]["output_tokens"], 50)
            self.assertEqual(item["checker"]["artifact_result"], "pass")
            with self.assertRaises(r.EvalError):
                r.run_one(self.experiment)
        os.environ["WRITE_EVAL_FAKE"] = ""

    def test_delivery_recheck_is_offline_and_does_not_rewrite_originals(self):
        self.init()
        item = r.run_one(self.experiment)
        sample = self.experiment / "samples" / item["sample_id"]
        protected = [self.experiment / "manifest.json", self.experiment / "state.json", sample / "result.json"]
        before = {str(p):r.digest(p) for p in protected}
        with mock.patch.object(r.subprocess, "Popen") as popen:
            checked = wrapper.recheck_delivery(self.experiment, item["sample_id"])
            popen.assert_not_called()
        self.assertTrue(checked["rechecked_receipt_pass"])
        self.assertTrue(checked["original_records_unchanged"])
        self.assertEqual(checked["paid_calls"], 0)
        self.assertEqual(before, {str(p):r.digest(p) for p in protected})
        with self.assertRaises(r.EvalError):
            wrapper.recheck_delivery(self.experiment, item["sample_id"])

    def test_first_pair_review_gate_blocks_strict_samples(self):
        self.init()
        self.pair()
        with mock.patch.object(r.subprocess, "Popen") as popen:
            with self.assertRaises(r.EvalError):
                r.run_one(self.experiment)
            popen.assert_not_called()
        self.review(r.ORDER[0])
        self.review(r.ORDER[1], [2, 2, 2, 2, 1])
        with self.assertRaises(r.EvalError):
            r.run_one(self.experiment)
        self.assertEqual(len(r.read_json(self.experiment / "state.json")["samples"]), 2)

    def test_infrastructure_failure_stops_without_retry_and_redacts_output(self):
        self.init()
        os.environ["WRITE_EVAL_FAKE"] = "infra"
        item = r.run_one(self.experiment)
        self.assertTrue(item.get("infrastructure_error"))
        with mock.patch.object(r.subprocess, "Popen") as popen:
            with self.assertRaises(r.EvalError):
                r.run_one(self.experiment)
            popen.assert_not_called()
        log = (self.experiment / "samples" / r.ORDER[0] / "stdout.log").read_text()
        self.assertNotIn("unit-fixture-not-real", log)

    def test_frozen_inputs_and_profile_prevent_old_batch_resume(self):
        manifest = self.init()
        self.assertFalse((self.experiment / "fixture/.env").exists())
        for name in ["run.py", "common.py", "checker.py", "helpers.py"]:
            self.assertIn(name, manifest["harness"])
        previous = previous_tests.r
        with self.assertRaises(previous.EvalError):
            previous.checked_manifest(self.experiment)
        frozen = self.experiment / "fixture/doc00.md"
        frozen.write_text("改变的资料")
        with mock.patch.object(r.subprocess, "Popen") as popen:
            with self.assertRaises(r.EvalError):
                r.run_one(self.experiment)
            popen.assert_not_called()

    def test_summary_rebuilds_from_samples_without_state_or_model(self):
        self.init()
        self.pair()
        self.review(r.ORDER[0])
        self.review(r.ORDER[1])
        for name in r.ORDER[2:]:
            r.run_one(self.experiment)
            self.review(name)
        (self.experiment / "state.json").unlink()
        with mock.patch.object(r.subprocess, "Popen") as popen:
            summary = r.summarize(self.experiment)
            popen.assert_not_called()
        self.assertTrue(summary["passed"])
        self.assertEqual(len(summary["samples"]), 4)

    def test_report_and_ordinary_efficiency_targets_are_not_pooled(self):
        self.init()
        self.pair()
        self.review(r.ORDER[0])
        self.review(r.ORDER[1])
        for name in r.ORDER[2:]:
            os.environ["WRITE_EVAL_FAKE"] = "no-reduction" if name.startswith("candidate") else ""
            r.run_one(self.experiment)
            self.review(name)
        summary = r.summarize(self.experiment)
        self.assertTrue(summary["completed"])
        self.assertEqual(summary["quality_result"], "pass")
        self.assertGreater(summary["groups"]["ordinary"]["token_median_reduction"], .30)
        self.assertEqual(summary["groups"]["report"]["token_median_reduction"], 0)
        self.assertFalse(summary["passed"])

    def test_archived_runner_can_summarize_without_original_working_copy(self):
        self.init()
        r.run_one(self.experiment)
        env = {"PATH": os.defpath, "HOME": str(self.root), "PYTHONDONTWRITEBYTECODE": "1"}
        command = [r.sys.executable, str(self.experiment / "harness/run.py"), "summarize", "--experiment", str(self.experiment)]
        result = r.subprocess.run(command, cwd=self.root, env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        summary = json.loads(result.stdout)
        self.assertEqual(list(summary["samples"]), [r.ORDER[0]])
        self.assertFalse(summary["passed"])

    def test_preflight_failure_starts_no_agent(self):
        self.init()
        with mock.patch.object(r, "preflight", side_effect=r.EvalError("沙箱预检失败")), mock.patch.object(r.subprocess, "Popen") as popen:
            with self.assertRaises(r.EvalError):
                r.run_one(self.experiment)
            popen.assert_not_called()
        self.assertTrue(r.read_json(self.experiment / "state.json")["stopped"])


if __name__ == "__main__":
    unittest.main()
