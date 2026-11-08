"""数值转述、静态推导及格式维度边界：所有固定反例均离线验证。"""
import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('work_notes_quality', ROOT / 'scripts/coverage_efficiency_eval/quality_oracle.py')
q = importlib.util.module_from_spec(spec); spec.loader.exec_module(q)
ORACLE = ROOT / 'eval/fixtures/work_notes_v3/semantic_oracle.json'


class FiniteOracleTests(unittest.TestCase):
    def setUp(self): self.oracle = q.load_oracle(ORACLE)
    def claim(self, identity):
        return next(c['text'] for case in self.oracle['cases'] for c in case['claims'] if c['id'] == identity)
    def test_old_counterexamples_and_new_cases_retain_exact_labels(self):
        self.assertEqual(len(self.oracle['cases']), 9)
        result = q.selfcheck(self.oracle)
        self.assertEqual(result['selfcheck_result'], 'pass', result)
        self.assertEqual(result['semantic_result'], 'not_run')
        self.assertEqual(len(result['checks']), 21)
    def test_restatement_is_not_incorrect_arithmetic_claim(self):
        text = self.claim('sample_number_restatement_0')
        result = q.evaluate_claims([{'text': text}], self.oracle)
        self.assertEqual(result['semantic_result'], 'pass')
        result = q.evaluate_claims([{'text': self.claim('sample_number_restatement_1')}], self.oracle)
        self.assertEqual(result['semantic_result'], 'fail')
        self.assertEqual(q.evaluate_claims([{'text': 'good大概97分，bad29分'}], self.oracle)['semantic_result'], 'not_run')
    def test_decimal_totals_and_default_are_frozen_without_executing_course(self):
        with mock.patch('subprocess.run', side_effect=AssertionError('不得执行课程代码')):
            q.validate_assumptions(self.oracle)
            result = q.evaluate_claims([{'text': self.claim('sample_static_numbers_0')}], self.oracle)
            self.assertEqual(result['semantic_result'], 'pass')
        changed = copy.deepcopy(self.oracle)
        changed['assumptions'][-1]['expected_total'] = '39.5'
        with self.assertRaises(ValueError): q.validate_assumptions(changed)
        self.assertEqual(q.evaluate_claims([{'text': self.claim('sample_static_numbers_0')}], changed)['environment_result'], 'error')
    def test_presence_is_not_semantic_validity_and_not_run_is_not_pass(self):
        self.assertEqual(q.evaluate_claims([{'text': self.claim('score_format_presence_only_0')}], self.oracle)['semantic_result'], 'pass')
        self.assertEqual(q.evaluate_claims([{'text': self.claim('score_format_presence_only_1')}], self.oracle)['semantic_result'], 'fail')
        self.assertEqual(q.evaluate_claims([{'text': self.claim('sample_static_numbers_1')}], self.oracle)['semantic_result'], 'fail')
    def test_source_drift_is_environment_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for source in self.oracle['sources']:
                path = root / source['path']; path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes((q.DEFAULT_WORKSPACE / source['path']).read_bytes())
            source = self.oracle['sources'][0]; path = root / source['path']; path.write_bytes(path.read_bytes()+b'\n')
            result = q.evaluate_claims([{'text': self.claim('sample_number_restatement_0')}], self.oracle, root)
            self.assertEqual(result['environment_result'], 'error'); self.assertEqual(result['semantic_result'], 'not_run')


if __name__ == '__main__': unittest.main()
