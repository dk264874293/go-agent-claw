"""真实CLI+SDK+本地HTTP脚本服务的兼容性回归；不访问付费模型网关。"""
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest

spec = importlib.util.spec_from_file_location('quality_native_profile', Path(__file__).with_name('run.py'))
p = importlib.util.module_from_spec(spec); spec.loader.exec_module(p)


@unittest.skipUnless(sys.platform == 'darwin' and shutil.which('go') and Path('/usr/bin/sandbox-exec').is_file(), '需要Go及macOS只读禁网checker沙箱')
class NativeCLITests(unittest.TestCase):
    def test_current_cli_actual_events_and_review_failure_costs(self):
        (p.REPO / '.claw').mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=p.REPO / '.claw', prefix='quality-native-test-') as temporary:
            root = Path(temporary); binary = root / 'claw'
            subprocess.run(['go', 'build', '-o', str(binary), './cmd/claw'], cwd=p.REPO, check=True, capture_output=True)
            source_manifest = root / 'source.json'
            p.r.write_json(source_manifest, [{'path': 'cmd/claw/main.go', 'sha256': p.r.digest(p.REPO / 'cmd/claw/main.go')}])
            source = p.REPO / 'eval/fixtures/write_efficiency_v1/workspace'
            files = p.r.file_list(source, only_markdown=True); pages = []
            for file in files:
                raw = (source / file['path']).read_bytes(); offset = 0
                while offset < len(raw):
                    end = min(offset + 8000, len(raw))
                    while True:
                        try: raw[offset:end].decode('utf-8'); break
                        except UnicodeDecodeError: end -= 1
                    pages.append({'path': file['path'], 'offset': offset, 'limit': 8000}); offset = end
            server_state = {'failure': '', 'requests': 0}
            class Handler(BaseHTTPRequestHandler):
                def log_message(self, *args): pass
                def do_POST(self):
                    request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                    server_state['requests'] += 1
                    names = {tool['function']['name'] for tool in request.get('tools', [])}
                    messages = request['messages']; system = messages[0]['content']; tool_calls = None
                    def call(name, identity, value):
                        return {'id': identity, 'type': 'function', 'function': {'name': name, 'arguments': json.dumps(value, ensure_ascii=False)}}
                    if '一次定向复核' in system:
                        if server_state['failure'] == 'http':
                            data = b'{"error":{"message":"offline scripted transport failure","type":"server_error"}}'
                            self.send_response(503); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data); return
                        packet = json.loads(messages[1]['content'])
                        text = 'invalid-json' if server_state['failure'] in ('json', 'json_partial') else json.dumps({'schema_version': 1, 'findings': [{'finding_id': identity, 'verdict': 'supported', 'reason': '仅离线协议回归，不是实际质量评审'} for identity in packet['review_required']]})
                    elif '唯一一次作者修订' in system:
                        packet = json.loads(messages[1]['content']); text = None
                        tool_calls = [call('write_report', 'publish', {'draft_id': packet['draft_id'], 'corrections': []})]
                    elif not any(message.get('role') == 'tool' for message in messages):
                        text = None; tool_calls = [call('read_file', 'read' + str(n), value) for n, value in enumerate(pages)]
                    else:
                        results = {m['tool_call_id']: m['content'] for m in messages if m.get('role') == 'tool'}
                        findings = []
                        for number, file in enumerate(files[:3]):
                            index = next(n for n, page in enumerate(pages) if page['path'] == file['path'] and page['offset'] == 0)
                            evidence = re.search(r'证据: (E\d+)', results['read' + str(index)]).group(1)
                            quote = (source / file['path']).read_text().splitlines()[0]
                            findings.append({'id': 'F' + str(number), 'kind': 'fact' if server_state['failure'] == 'json_partial' and number == 0 else 'inference', 'statement': quote,
                                             'context': '离线协议样例；V1/V2、JSON默认行为仍待核实，未执行脚本',
                                             'evidence': [{'evidence_id': evidence, 'start_line': 1, 'end_line': 1, 'quote': quote}]})
                        report = {'schema_version': 1, 'findings': findings, 'overview_ids': [f['id'] for f in findings]}
                        text = None; tool_calls = [call('verify_findings' if 'verify_findings' in names else 'write_report', 'draft', report)]
                    response = {'id': 'offline-response', 'object': 'chat.completion', 'created': 1, 'model': request['model'],
                                'choices': [{'index': 0, 'finish_reason': 'tool_calls' if tool_calls else 'stop', 'message': {'role': 'assistant', 'content': text, **({'tool_calls': tool_calls} if tool_calls else {})}}],
                                'usage': {'prompt_tokens': 100, 'completion_tokens': 10, 'total_tokens': 110}}
                    data = json.dumps(response, ensure_ascii=False).encode()
                    self.send_response(200); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
            server = ThreadingHTTPServer(('127.0.0.1', 0), Handler); thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
            try:
                dotenv = root / '.env'; dotenv.write_text('DEEPSEEK_API_KEY=offline-only-not-a-real-key\nDEEPSEEK_BASE_URL=http://127.0.0.1:' + str(server.server_port) + '/v1/\n')
                for failure in ('', 'http', 'json', 'json_partial'):
                    server_state['failure'] = failure
                    experiment = root / ('experiment-' + (failure or 'ok'))
                    p.init_experiment(experiment, binary, binary, project_dotenv=dotenv, baseline_source_manifest=source_manifest, candidate_source_manifest=source_manifest)
                    baseline = p.run_one(experiment)
                    self.assertTrue(baseline['protocol_valid'], baseline)
                    candidate = p.run_one(experiment)
                    self.assertTrue(candidate['protocol_valid'], candidate)
                    self.assertTrue(candidate['coverage']['complete']); self.assertTrue(candidate['scope_pass'])
                    self.assertEqual(candidate['metrics']['phases']['review']['model_calls'], 1)
                    if failure == 'http':
                        self.assertEqual(candidate['status'], 'FAILED'); self.assertEqual(candidate['metrics']['model_calls'], 3)
                        self.assertTrue(p.r.read_json(experiment / 'state.json')['stopped'])
                        self.assertIsNone(candidate['metrics']['phases']['review']['input_tokens'])
                        self.assertEqual(candidate['metrics']['phases']['revision']['model_calls'], 0)
                    else:
                        self.assertEqual(candidate['metrics']['phases']['revision']['model_calls'], 1)
                        self.assertEqual(candidate['metrics']['model_calls'], 4)
                        self.assertEqual(candidate['metrics']['phases']['review']['total_tokens'], 110)
                        self.assertFalse(p.r.read_json(experiment / 'state.json')['stopped'])
                        self.assertEqual(candidate['status'], 'BLOCKED' if failure == 'json' else 'NEEDS_VERIFICATION')
                        self.assertEqual(candidate['quality_publication_check']['result'], 'not_run' if failure == 'json' else 'pass')
                        if failure == 'json_partial':
                            self.assertEqual(candidate['quality']['review_status'], 'invalid')
                            self.assertFalse(candidate.get('infrastructure_error')); self.assertEqual(candidate['citation_check']['citations'], 1)
                    # 每次SDK真实HTTP编码都记录字节；不能把内部阶段漏计或重复计费。
                    self.assertEqual(candidate['metrics']['http_attempts'], candidate['metrics']['model_calls'])
                    self.assertGreater(candidate['metrics']['phases']['review']['encoded_request_bytes'], 0)
                self.assertEqual(server_state['requests'], 23)
            finally:
                server.shutdown(); server.server_close(); thread.join(timeout=2)


if __name__ == '__main__': unittest.main()
