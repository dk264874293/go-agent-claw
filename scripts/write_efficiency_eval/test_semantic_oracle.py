"""固定反例的独立语义标注回归，不把模型输出或格式当作语义答案。"""
import contextlib
import copy
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest

import semantic_oracle as oracle_module


class FiniteSemanticOracleTests(unittest.TestCase):
    def setUp(self):
        self.oracle = oracle_module.load_oracle()

    def annotated(self, claim_id):
        for case in self.oracle["cases"]:
            for claim in case["claims"]:
                if claim["id"] == claim_id:
                    return claim
        self.fail("测试引用了不存在的固定标注")

    def evaluate(self, *texts):
        return oracle_module.evaluate_claims([{"text": text} for text in texts], self.oracle)

    def test_actual_frozen_source_versions_and_all_exact_quotes(self):
        checked = oracle_module.inspect_evidence(self.oracle)
        self.assertEqual(checked["environment_result"], "pass", checked)
        self.assertEqual(len(checked["checks"]), 5)
        for check in checked["checks"]:
            self.assertEqual(check["version_result"], "pass")
            self.assertEqual(check["quote_result"], "pass")
        self.assertFalse(self.oracle["annotation"]["independent_human_review"])
        self.assertFalse(self.oracle["annotation"]["course_code_executed"])

    def test_each_known_positive_and_negative_matches_explicit_annotation(self):
        count = 0
        for case in self.oracle["cases"]:
            for claim in case["claims"]:
                with self.subTest(claim_id=claim["id"]):
                    result = self.evaluate(claim["text"])
                    self.assertEqual(result["semantic_result"], claim["expected_result"])
                    self.assertEqual(result["claims"][0]["annotation_id"], claim["id"])
                    self.assertEqual(result["claims"][0]["case_id"], case["id"])
                    self.assertEqual(result["known_claims"], 1)
                count += 1
        self.assertEqual(count, 13)

    def test_valid_located_citations_do_not_make_known_wrong_conclusion_true(self):
        # 与引用checker独立：同一份真实版本、真实行号和摘录，可以被错误解释。
        proof = self.oracle["sources"][0]
        supplied_citations = [{"path": proof["path"], "version": proof["sha256"], **item} for item in proof["evidence"]]
        result = oracle_module.evaluate_claims([{
            "text": self.annotated("v2_review_invalid")["text"],
            "citations": supplied_citations,
            "citation_result": "pass",
            "status": "evidence_valid",
        }], self.oracle)
        self.assertEqual(result["oracle_evidence"]["environment_result"], "pass")
        self.assertEqual(result["semantic_result"], "fail")

    def test_valid_json_and_self_declared_status_do_not_grant_pass(self):
        payload = json.loads('{"claims":[{"text":"格式合法但未经标注的正确性主张","semantic_result":"pass"}]}')
        result = oracle_module.evaluate_claims(payload["claims"], self.oracle)
        self.assertEqual(result["checker_result"], "pass")
        self.assertEqual(result["semantic_result"], "not_run")

    def test_keywords_and_equivalent_paraphrases_remain_unknown(self):
        for text in [
            "review reviewed draft V1 V2，这些关键词不能决定结论。",
            "数据中没有score时会用5，换算后技术深度是12.5分。",
            "python3也许能在某些电脑上运行，但本机没有验证。",
            "据说整个质量脚本没有任何默认值。",
        ]:
            with self.subTest(text=text):
                self.assertEqual(self.evaluate(text)["semantic_result"], "not_run")

    def test_label_id_and_case_id_cannot_replace_claim_text(self):
        result = oracle_module.evaluate_claims([{"text": "未知说法", "claim_id": "score_default_mapped", "case_id": "optional_score_default"}], self.oracle)
        self.assertEqual(result["semantic_result"], "not_run")
        wrong = self.annotated("score_missing_raises")["text"]
        result = oracle_module.evaluate_claims([{"text": wrong, "claim_id": "score_default_mapped", "expected_result": "pass"}], self.oracle)
        self.assertEqual(result["semantic_result"], "fail")

    def test_partial_known_pass_does_not_hide_unknown_claims(self):
        positive = self.annotated("score_default_mapped")["text"]
        result = self.evaluate(positive, "未标注的重要结论")
        self.assertEqual(result["semantic_result"], "not_run")
        self.assertEqual(result["known_claims"], 1)
        self.assertEqual(result["unknown_claims"], 1)
        wrong = self.annotated("score_missing_raises")["text"]
        result = self.evaluate(wrong, "另一个未知主张")
        self.assertEqual(result["semantic_result"], "fail")
        self.assertEqual(result["unknown_claims"], 1)

    def test_empty_or_invalid_claim_input_cannot_pass(self):
        self.assertEqual(oracle_module.evaluate_claims([], self.oracle)["semantic_result"], "not_run")
        for bad in [None, {}, [{"id": "score_default_mapped"}], [{"text": True}]]:
            with self.subTest(bad=bad):
                result = oracle_module.evaluate_claims(bad, self.oracle)
                self.assertEqual(result["checker_result"], "error")
                self.assertEqual(result["semantic_result"], "not_run")

    def test_source_version_drift_is_environment_error_not_a_semantic_answer(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for source in self.oracle["sources"]:
                target = root / source["path"]
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(oracle_module.DEFAULT_WORKSPACE / source["path"], target)
            target = root / self.oracle["sources"][0]["path"]
            target.write_bytes(target.read_bytes() + b"\n")
            result = oracle_module.evaluate_claims([{"text": self.annotated("v2_status_supported")["text"]}], self.oracle, root)
            self.assertEqual(result["environment_result"], "error")
            self.assertEqual(result["semantic_result"], "not_run")

    def test_tampered_frozen_quote_hash_is_rejected(self):
        changed = copy.deepcopy(self.oracle)
        changed["sources"][0]["evidence"][0]["quote"] += "伪造"
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "oracle.json"
            target.write_text(json.dumps(changed, ensure_ascii=False), encoding="utf-8")
            with self.assertRaises(ValueError):
                oracle_module.load_oracle(target)

    def test_command_spelling_and_default_arithmetic_are_frozen_as_actual_source(self):
        evidence = {item["id"]: item["quote"] for source in self.oracle["sources"] for item in source["evidence"]}
        self.assertIn('VALID_STATUSES = {"draft", "review", "published", "archived"}', evidence["status_enum"])
        self.assertIn('data.get("score", 5)', evidence["score_default"])
        self.assertIn('mapped = (article_score / 10) * max_score', evidence["score_default"])
        for name in ["usage_commands", "test_commands"]:
            self.assertIn("python hooks/check_quality.py", evidence[name])
            self.assertNotIn("python3 hooks/check_quality.py", evidence[name])
        value = next(c["derived_value"] for c in self.oracle["cases"] if c["id"] == "optional_score_default")
        self.assertEqual(value["expected_mapped"], 12.5)

    def test_selfcheck_is_regression_pass_not_model_semantic_pass(self):
        result = oracle_module.selfcheck(self.oracle)
        self.assertEqual(result["selfcheck_result"], "pass")
        self.assertEqual(result["semantic_result"], "not_run")
        self.assertEqual(len(result["checks"]), 14)
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            code = oracle_module.main(["selfcheck"])
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output.getvalue())["semantic_result"], "not_run")


if __name__ == "__main__":
    unittest.main()
