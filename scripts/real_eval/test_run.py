"""评估结果协议的离线回归，不调用模型。"""
import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('real_eval', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ResultProtocolTests(unittest.TestCase):
    def test_multiple_failure_labels(self):
        row = dict.fromkeys(r.DIMENSIONS, 'pass')
        row.update(scope_result='fail', engine_result='fail', engine_status='BUDGET_EXHAUSTED', exit_code=3,
                   final_text_present=False, pending_action_count=0)
        r.finish_dimensions(row, {'require_final_text': True})
        self.assertIn('scope_failure', row['labels'])
        self.assertIn('budget_exhausted', row['labels'])
        self.assertFalse(row['combined_pass'])
        self.assertEqual(row['artifact_result'], 'pass')

    def test_correct_artifact_does_not_override_budget(self):
        row = dict.fromkeys(r.DIMENSIONS, 'pass')
        row.update(engine_result='fail', engine_status='BUDGET_EXHAUSTED', exit_code=3,
                   final_text_present=True, pending_action_count=0)
        self.assertFalse(r.finish_dimensions(row, {'require_final_text': True})['combined_pass'])

    def test_checker_error_is_separate(self):
        row = dict.fromkeys(r.DIMENSIONS, 'pass')
        row.update(checker_result='error', artifact_result='error', engine_status='NEEDS_VERIFICATION',
                   exit_code=2, final_text_present=True, pending_action_count=0)
        self.assertFalse(r.finish_dimensions(row, {'require_final_text': True})['combined_pass'])
        self.assertIn('checker_failure', row['labels'])
        self.assertNotIn('engine_failure', row['labels'])

    def test_scope_tracks_types_and_deletions(self):
        policy = {'modify': ['a'], 'create_optional': ['new/test.go'], 'delete': []}
        before = {'a': ['file', 420, 'old']}
        after = {'a': ['symlink', '/tmp/escape'], 'new': ['dir', 493], 'new/test.go': ['file', 420, 'x']}
        self.assertEqual(r.scope_result(before, after, policy)[1], ['a'])
        self.assertEqual(r.scope_result(before, {}, policy)[1], ['a'])

    def test_real_read_headers_both_parentheses(self):
        for closing in (')', '）'):
            output = '文件: internal/tools/read_file.go\n版本: ' + 'a' * 64 + '\n字节范围: [0, 42' + closing + '，共 42 字节\n行号范围: [1, 2]\n\nhello'
            events = [{'kind': 'action_result', 'payload': {'last_action': {'tool': 'read_file', 'result': {'status': 'SUCCEEDED', 'output': output}}}}]
            reads = r.read_evidence(events)['reads']
            self.assertEqual(reads[0]['end'], 42)
            self.assertEqual(reads[0]['version'], 'a' * 64)

    def test_event_fallback_and_ambiguous_session(self):
        with tempfile.TemporaryDirectory() as name:
            work = Path(name)
            state = {'version': 1, 'session_id': 'session', 'last_result': {'task_id': 'one', 'status': 'ANSWERED'}}
            task = work / '.claw/tasks/one'; task.mkdir(parents=True)
            (task / 'snapshot.json').write_text('{broken')
            event = {'version': 1, 'sequence': 1, 'kind': 'finished', 'payload': state}
            (task / 'events.jsonl').write_text(json.dumps(event) + '\n' + '{partial')
            self.assertEqual(r.read_task(work, 'session')[0], 'one')
            two = work / '.claw/tasks/two'; two.mkdir()
            state['last_result']['task_id'] = 'two'
            r.write_json(two / 'snapshot.json', {'version': 1, 'sequence': 1, 'state': state})
            with self.assertRaises(ValueError):
                r.read_task(work, 'session')

    def test_runtime_credentials_are_redacted(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            secret = 'private-eval-credential-12345'
            p = root / '.claw/tasks/a/snapshot.json'
            r.write_json(p, {'reason': 'server echoed ' + secret})
            r.redact_runtime(root, secret)
            self.assertNotIn(secret, p.read_text())
            self.assertIn('[REDACTED]', r.read_json(p)['reason'])

    def test_gate_requires_complete_unique_repeats(self):
        rows = []
        for name in r.FIRST:
            for arm in ('baseline', 'candidate'):
                for repeat in (1, 2):
                    rows.append({'case_id': name, 'arm': arm, 'repeat': repeat, 'combined_pass': True,
                                 'forbidden_changes': [], 'engine_status': 'NEEDS_VERIFICATION', 'artifact_result': 'pass'})
        self.assertFalse(r.stage_gate(rows, r.FIRST))
        for name in r.FIRST:
            for arm in ('baseline', 'candidate'):
                rows.append({'case_id': name, 'arm': arm, 'repeat': 3, 'combined_pass': True,
                             'forbidden_changes': [], 'engine_status': 'NEEDS_VERIFICATION', 'artifact_result': 'pass'})
        self.assertTrue(r.stage_gate(rows, r.FIRST))
        rows[-1]['repeat'] = 2
        self.assertFalse(r.stage_gate(rows, r.FIRST))

    def test_summary_rebuild_is_deterministic(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            row = {'case_id': 'W02', 'repeat': 1, 'arm': 'candidate', 'schema_version': 2,
                   'combined_pass': False, 'forbidden_changes': [], 'engine_status': 'BUDGET_EXHAUSTED',
                   'exit_code': 3, 'labels': ['budget_exhausted'], 'model_calls': 8, 'tool_calls': None,
                   'input_tokens': None, 'output_tokens': None, 'artifact_result': 'pass', 'engine_result': 'fail'}
            r.write_json(root / 'runs/W02-1-candidate/result.json', row)
            first = r.summarize(root)
            self.assertIsNone(first['arms']['candidate']['tool_calls'])
            (root / 'summary.json').unlink()
            self.assertEqual(first, r.summarize(root))

    def test_model_control_rejects_unknown_legacy(self):
        with tempfile.TemporaryDirectory() as name:
            binary = Path(name) / 'claw'
            binary.write_text('#!/bin/sh\necho usage\n'); binary.chmod(0o755)
            with self.assertRaises(ValueError):
                r.model_control(binary, 'gpt-6.1-sol')
            binary.write_text('#!/bin/sh\necho "  -model string"\n')
            self.assertEqual(r.model_control(binary, 'gpt-6.1-sol')['method'], 'cli_flag')

    def test_resume_rejects_changed_inputs(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            binary = root / 'binary'; binary.write_text('#!/bin/sh\necho \"  -model string\"\n'); binary.chmod(0o755)
            args = argparse.Namespace(suite=str(r.DEFAULT_SUITE), baseline_bin=str(binary), candidate_bin=str(binary),
                                      resume=None, output=str(root / 'experiment'), candidate_source=None)
            experiment, _, _, _ = r.prepare(args)
            args.resume = str(experiment); args.output = None
            r.prepare(args)
            (experiment / 'suite/suite.json').write_text('{}')
            with self.assertRaises(ValueError):
                r.prepare(args)

    def test_summary_rebuild_and_legacy(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            for i in range(5):
                r.write_json(root / f'runs/old-{i}/result.json', {'status': 'BUDGET_EXHAUSTED', 'category': 'scope_failure' if i < 2 else 'budget_exhausted'})
            summary = r.summarize(root)
            self.assertEqual(summary['labels']['budget_exhausted'], 5)
            self.assertEqual(summary['original_categories']['scope_failure'], 2)


if __name__ == '__main__':
    unittest.main()
