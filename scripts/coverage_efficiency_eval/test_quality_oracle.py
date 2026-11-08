"""六类有限语义反例与场景前提校验；不把机械引用通过当结论正确。"""
import copy
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location("coverage_quality_oracle_v2", Path(__file__).with_name("quality_oracle.py"))
oracle_module = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(oracle_module)


class CoverageQualityOracleTests(unittest.TestCase):
    def setUp(self):
        self.oracle = oracle_module.load_oracle()

    def annotation(self, claim_id):
        for case in self.oracle["cases"]:
            for claim in case["claims"]:
                if claim["id"] == claim_id:
                    return claim
        self.fail("缺少固定标注：" + claim_id)

    def test_six_cases_fourteen_claims_match_actual_frozen_quotes(self):
        result = oracle_module.inspect_evidence(self.oracle)
        self.assertEqual(result["environment_result"], "pass", result)
        self.assertEqual(len(self.oracle["cases"]), 6)
        self.assertEqual(sum(len(x["claims"]) for x in self.oracle["cases"]), 14)
        for check in result["checks"]:
            self.assertEqual(check["version_result"], "pass")
            self.assertEqual(check["quote_result"], "pass")
        for case in self.oracle["cases"]:
            for claim in case["claims"]:
                with self.subTest(claim=claim["id"]):
                    actual = oracle_module.evaluate_claims([{"text": claim["text"]}], self.oracle)
                    self.assertEqual(actual["semantic_result"], claim["expected_result"])
                    self.assertEqual(actual["claims"][0]["annotation_id"], claim["id"])

    def test_wrong_conclusion_fails_even_with_located_valid_citations(self):
        wrong = self.annotation("hook_runtime_overclaim")
        hook = next(s for s in self.oracle["sources"] if s["id"] == "hook")
        result = oracle_module.evaluate_claims([{"text": wrong["text"], "citation_result": "pass", "citations": [{"path": hook["path"], "version": hook["sha256"], **e} for e in hook["evidence"]]}], self.oracle)
        self.assertEqual(result["oracle_evidence"]["environment_result"], "pass")
        self.assertEqual(result["semantic_result"], "fail")

    def test_unknown_paraphrases_and_partial_pass_do_not_earn_semantic_pass(self):
        for text in ["Hook可能不能自动修复。", "顶层score没有也没问题。", "已完整读取15份文件。", "V2 reviewed review 默认分数 5，关键词不能判定对错。"]:
            result = oracle_module.evaluate_claims([{"text": text, "expected_result": "pass"}], self.oracle)
            self.assertEqual(result["semantic_result"], "not_run")
        positive = self.annotation("score_default_mapped")["text"]
        result = oracle_module.evaluate_claims([{"text": positive}, {"text": "未评审的重要主张"}], self.oracle)
        self.assertEqual(result["semantic_result"], "not_run")
        self.assertEqual(result["known_claims"], 1)

    def test_gap_is_synthetic_353_bytes_and_does_not_create_agent_receipts(self):
        premise = next(x for x in self.oracle["assumptions"] if x["id"] == "quality_tail_353")
        self.assertEqual(premise["read_ranges"], [{"start": 0, "end": 6944}, {"start": 6944, "end": 13685}])
        self.assertEqual(premise["expected_missing"], [{"start": 13685, "end": 14038}])
        self.assertEqual(premise["expected_missing_bytes"], 353)
        self.assertFalse(premise["complete_historical_replay"])
        result = oracle_module.evaluate_claims([{"text": self.annotation("tail_full_source_has_cleanup")["text"]}], self.oracle)
        self.assertEqual(result["semantic_result"], "pass")
        self.assertFalse(result["complete_historical_replay"])
        self.assertIn("不是完整历史回放", result["limitations"])

    def test_invalid_gap_or_false_runtime_prerequisite_is_not_a_quality_answer(self):
        for change in ["gap", "runtime", "nested", "version", "identity"]:
            changed = copy.deepcopy(self.oracle)
            if change == "gap":
                changed["assumptions"][1]["read_ranges"][1]["end"] = 14038
            elif change == "runtime":
                changed["assumptions"][0]["host_executed"] = True
            elif change == "nested":
                changed["assumptions"][2]["expected_nested_mapped"] = 12.5
            elif change == "version":
                changed["assumptions"][1]["source_sha256"] = "a" * 64
            else:
                changed["annotation"]["method"] = "human"
            with self.subTest(change=change):
                with self.assertRaises(ValueError):
                    oracle_module.validate_assumptions(changed)
                result = oracle_module.evaluate_claims([{"text": self.annotation("tail_gap_is_not_complete")["text"]}], changed)
                self.assertEqual(result["environment_result"], "error")
                self.assertEqual(result["semantic_result"], "not_run")

    def test_source_version_drift_and_inconsistent_total_size_are_environment_errors(self):
        with tempfile.TemporaryDirectory() as temporary:
            workspace = Path(temporary)
            for source in self.oracle["sources"]:
                target = workspace / source["path"]
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(oracle_module.DEFAULT_WORKSPACE / source["path"], target)
            quality = next(s for s in self.oracle["sources"] if s["id"] == "quality")
            path = workspace / quality["path"]
            path.write_bytes(path.read_bytes() + b"\n")
            result = oracle_module.evaluate_claims([{"text": self.annotation("score_default_mapped")["text"]}], self.oracle, workspace)
            self.assertEqual(result["environment_result"], "error")
            self.assertEqual(result["semantic_result"], "not_run")

    def test_selfcheck_pass_is_not_human_review_or_model_quality_pass(self):
        result = oracle_module.selfcheck(self.oracle)
        self.assertEqual(result["selfcheck_result"], "pass", result)
        self.assertEqual(result["semantic_result"], "not_run")
        self.assertEqual(len(result["checks"]), 15)
        self.assertEqual(result["annotation"]["method"], "source_audit_agent")
        self.assertEqual(result["annotation"]["provider_calls"], 0)
        self.assertFalse(result["annotation"]["independent_human_review"])
        self.assertFalse(result["annotation"]["course_code_executed"])

    def test_old_thirteen_claim_oracle_keeps_its_original_scope(self):
        old = oracle_module._base.load_oracle()
        self.assertEqual(len(old["cases"]), 3)
        self.assertEqual(sum(len(c["claims"]) for c in old["cases"]), 13)
        self.assertEqual(old["source_task"], "6fab055a7f97c1af02c9e5c635b7c6be")
        self.assertEqual(oracle_module._base.selfcheck(old)["selfcheck_result"], "pass")


if __name__ == "__main__":
    unittest.main()
