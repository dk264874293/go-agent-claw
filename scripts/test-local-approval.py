#!/usr/bin/env python3
"""使用伪网关与伪终端验收本地审批；不读取项目密钥，不调用真实 LLM。"""

import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    if sys.platform not in ("darwin", "linux"):
        raise RuntimeError("本地终端验收仅支持 macOS/Linux")
    repository = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="claw-approval-test-") as directory:
        binary = str(Path(directory) / "claw")
        subprocess.run(["go", "build", "-o", binary, "./cmd/claw"],
                       cwd=repository, check=True, timeout=60)
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                number = len(requests)
                message = {"role": "assistant", "content": "完成"}
                if number in (1, 3):
                    arguments = {"path": "approved.txt" if number == 1 else "cancelled.txt", "content": "测试写入"}
                    message["tool_calls"] = [{"id": str(number), "type": "function", "function": {
                        "name": "write_file", "arguments": json.dumps(arguments, ensure_ascii=False)}}]
                body = json.dumps({"id": str(number), "object": "chat.completion", "created": 1,
                                   "model": "mock", "choices": [{"index": 0, "message": message,
                                   "finish_reason": "tool_calls" if number in (1, 3) else "stop"}],
                                   "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        master, slave = os.openpty()
        # 新进程设置控制终端，避免在含 HTTP 线程的进程中运行 fork 后回调。
        helper = ("import os,sys,fcntl,termios; fcntl.ioctl(0,termios.TIOCSCTTY,0); "
                  "os.execve(sys.argv[1],sys.argv[1:],dict(os.environ))")
        args = [binary, "-dir", directory, "-model", "mock", "-base-url",
                f"http://127.0.0.1:{server.server_port}", "-tool-timeout", "2s"]
        environment = {"PATH": os.environ["PATH"], "HOME": directory, "OPENAI_API_KEY": "local-test-key"}
        process = subprocess.Popen([sys.executable, "-c", helper, *args], stdin=slave, stdout=slave,
                                   stderr=slave, env=environment, cwd=directory, start_new_session=True)
        os.close(slave)
        pending = b""

        def until(marker, timeout=10):
            nonlocal pending
            deadline = time.monotonic() + timeout
            while marker not in pending:
                if time.monotonic() > deadline:
                    raise AssertionError("终端响应超时：" + pending.decode(errors="replace")[-1000:])
                readable, _, _ = select.select([master], [], [], 0.2)
                if readable:
                    pending += os.read(master, 65536)
            _, pending = pending.split(marker, 1)

        try:
            until(b"claw>")
            os.write(master, "批准写入\n".encode())
            until("输入 yes 批准本次调用，其他输入拒绝: ".encode())
            os.write(master, b"yes\n")
            until(b"claw>")
            assert Path(directory, "approved.txt").read_text() == "测试写入", "批准后没有写入"
            os.write(master, "等待审批超时\n".encode())
            until("输入 yes 批准本次调用，其他输入拒绝: ".encode())
            until(b"claw>")
            assert not Path(directory, "cancelled.txt").exists(), "超时后仍写入"
            assert len(requests) == 4, "请求数异常"
            assert "context deadline exceeded" in json.dumps(requests[3], ensure_ascii=False), "审批超时未回传"
            os.write(master, b"/exit\n")
            # 退出期间仍排空伪终端，避免输出缓冲阻塞子进程收尾。
            deadline = time.monotonic() + 5
            while process.poll() is None and time.monotonic() < deadline:
                readable, _, _ = select.select([master], [], [], 0.1)
                if readable:
                    try:
                        os.read(master, 65536)
                    except OSError:
                        break
            assert process.wait(timeout=2) == 0, "CLI 未正常退出"
            print("本地终端验收通过：REPL 与审批互斥、yes 写入、无人响应超时拒绝、恢复输入和退出。")
        finally:
            os.close(master)
            if process.poll() is None:
                process.kill()
                process.wait(timeout=3)
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    main()
