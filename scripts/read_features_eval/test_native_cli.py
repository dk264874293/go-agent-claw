"""真实CLI和SDK的on/off入口回归；沙箱仅允许回环HTTP，不触发外部付费网关。"""
import errno
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest

spec=importlib.util.spec_from_file_location('read_features_native_profile',Path(__file__).with_name('run.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)

READY=(sys.platform=='darwin' and shutil.which('go') and Path('/usr/bin/sandbox-exec').is_file() and (p.REPO/'cmd/claw/main.go').is_file())


@unittest.skipUnless(READY,'原生回归需要当前Go源码、Go及macOS仅回环沙箱；冻结harness仍可离线汇总')
class NativeCLITests(unittest.TestCase):
    def test_native_cli_off_on_persists_contract_using_only_loopback_gateway(self):
        with tempfile.TemporaryDirectory(prefix='read-features-native-') as temporary:
            root=Path(temporary);binary=root/'claw'
            build_env=dict(os.environ,GOPROXY='off',GOSUMDB='off')
            built=subprocess.run(['go','build','-o',str(binary),'./cmd/claw'],cwd=p.REPO,env=build_env,capture_output=True,text=True,timeout=120)
            self.assertEqual(built.returncode,0,built.stderr)
            captured=[];server_errors=[]
            class Handler(BaseHTTPRequestHandler):
                def log_message(self,*args):pass
                def do_POST(self):
                    try:
                        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                        captured.append({'host':self.headers.get('Host'),'authorization':self.headers.get('Authorization'),'path':self.path,'body':body})
                        response={'id':'offline-native-reply','object':'chat.completion','created':1,'model':body['model'],
                                  'choices':[{'index':0,'finish_reason':'stop','message':{'role':'assistant','content':'已完成本地协议验证。'}}],
                                  'usage':{'prompt_tokens':10,'completion_tokens':5,'total_tokens':15}}
                        data=json.dumps(response,ensure_ascii=False).encode()
                        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
                    except Exception as error:
                        server_errors.append(type(error).__name__);self.send_error(500)
            server=ThreadingHTTPServer(('127.0.0.1',0),Handler);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            profile='(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))'
            sandbox=['/usr/bin/sandbox-exec','-p',profile]
            try:
                # 先证明非回环的IP连出在内核准入被拒绝；不携带任何模型凭据。
                probe='import socket,errno; s=socket.socket(); s.settimeout(1)\ntry:\n s.connect(("203.0.113.1",443)); raise SystemExit("unexpected outbound allowed")\nexcept OSError as e:\n assert e.errno in (errno.EPERM,errno.EACCES), repr(e)\n'
                denied=subprocess.run(sandbox+[sys.executable,'-c',probe],cwd=root,env={'PATH':os.defpath,'LANG':'en_US.UTF-8'},capture_output=True,text=True,timeout=10)
                self.assertEqual(denied.returncode,0,denied.stderr)
                for candidate in (False,True):
                    with self.subTest(features='on' if candidate else 'off'):
                        work=root/('on' if candidate else 'off');work.mkdir();home=work/'home';home.mkdir();tmp=work/'tmp';tmp.mkdir()
                        # 子进程不继承真实key、代理、gateway或项目.env；只能到达本地脚本服务。
                        env={'PATH':os.defpath,'HOME':str(home),'TMPDIR':str(tmp),'LANG':'en_US.UTF-8',
                             'DEEPSEEK_API_KEY':'offline-read-features-key','DEEPSEEK_BASE_URL':'http://127.0.0.1:'+str(server.server_port)+'/v1/',
                             'CLAW_OBSERVE':'on','CLAW_OBSERVE_DIR':str(work/'observations'),'CLAW_REQUEST_DIAGNOSTICS':'off',
                             'HTTP_PROXY':'','HTTPS_PROXY':'','ALL_PROXY':'','http_proxy':'','https_proxy':'','all_proxy':'','NO_PROXY':'127.0.0.1'}
                        command=[str(binary),'-dir',str(work),'-session','native-contract','-mode','readonly','-task-kind','answer',
                                 '-provider','deepseek','-model','deepseek-v4.1-flash-1017','-max-model-calls','2','-max-tool-calls','2','-max-turns','2','-timeout','20s',
                                 '-prompt','本地接口验证，请直接回复完成。',*p.sample_arguments({},candidate)]
                        run=subprocess.run(sandbox+command,cwd=work,env=env,capture_output=True,text=True,timeout=45)
                        self.assertEqual(run.returncode,0,run.stderr+'\n'+run.stdout)
                        task_events=list((work/'.claw/tasks').glob('*/events.jsonl'));self.assertEqual(len(task_events),1)
                        events=[json.loads(row) for row in task_events[0].read_text().splitlines() if row.strip()]
                        state=next(event['payload'] for event in events if event['kind']=='finished');actual=state['spec'];result=state['last_result']
                        self.assertEqual(actual['read_context_version'],4 if candidate else 2)
                        self.assertEqual(actual.get('read_features'),p.FEATURES if candidate else None)
                        self.assertEqual(result['status'],'ANSWERED');self.assertEqual(result['usage']['model_calls'],1);self.assertEqual(result['usage']['tool_calls'],0)
                        # String flag的真实约束不能再由fake CLI的错误假设遮蔽。
                        bad=command.copy();bad[bad.index('-source-views=on' if candidate else '-source-views=off')]='-source-views=true'
                        before=len(captured);invalid=subprocess.run(sandbox+bad,cwd=work,env=env,capture_output=True,text=True,timeout=10)
                        self.assertEqual(invalid.returncode,2);self.assertEqual(len(captured),before)
                self.assertEqual(server_errors,[]);self.assertEqual(len(captured),2)
                for request in captured:
                    self.assertEqual(request['host'],'127.0.0.1:'+str(server.server_port));self.assertEqual(request['authorization'],'Bearer offline-read-features-key')
                    self.assertTrue(request['path'].endswith('/chat/completions'));self.assertEqual(request['body']['model'],'deepseek-v4.1-flash-1017')
            finally:
                server.shutdown();server.server_close();thread.join(timeout=2)


if __name__=='__main__':unittest.main()
