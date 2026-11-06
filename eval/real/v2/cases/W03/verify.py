import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


CASE = Path(__file__).resolve().parent
TEST = "TestConcurrentEditRejectsStaleVersion"
TEST_PATH = Path("internal/tools/version_conflict_test.go")
SOURCE = Path("internal/tools/files.go")
PACKAGE = "./internal/tools"
ORIGINAL = """\t\tif err := requireVersion(before, contentVersion(content)); err != nil {
\t\t\treturn fileMutation{}, err
\t\t}
"""
MUTATION = "\t\tbefore = contentVersion(content)\n"


def run_go(workspace, env, overlay=None):
    command = ["go", "test", "-json", "-count=1", "-timeout=20s", "-run", f"^{TEST}$"]
    if overlay is not None:
        command.extend(["-overlay", str(overlay)])
    command.append(PACKAGE)
    result = subprocess.run(
        command,
        cwd=workspace,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        timeout=75,
    )
    actions = []
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Test") == TEST:
            actions.append(event["Action"])
    return result.returncode, actions, result.stdout


def verify(workspace):
    test_file = workspace / TEST_PATH
    if test_file.is_symlink() or not test_file.is_file():
        raise ValueError(f"缺少新增测试 {TEST_PATH}")
    for frozen in (CASE / "workspace").rglob("*"):
        if frozen.is_file():
            relative = frozen.relative_to(CASE / "workspace")
            actual = workspace / relative
            if actual.is_symlink() or not actual.is_file() or actual.read_bytes() != frozen.read_bytes():
                raise ValueError(f"冻结源码发生变化 {relative}")
    with tempfile.TemporaryDirectory(prefix="claw-w03-verify-") as scratch:
        scratch = Path(scratch)
        go = shutil.which("go")
        if go is None:
            raise ValueError("缺少 Go 工具链")
        cache = Path(os.environ.get("GOCACHE") or scratch / "go-cache")
        cache.mkdir(exist_ok=True)
        env = {
            "PATH": str(Path(go).parent) + os.pathsep + os.defpath,
            "GOCACHE": str(cache),
            "GOMODCACHE": str(scratch / "modules"),
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOVCS": "*:off",
            "GOTOOLCHAIN": "local",
            "GOENV": "off",
            "GOWORK": "off",
            "GOFLAGS": "-mod=readonly",
            "CGO_ENABLED": "0",
            "TMPDIR": str(scratch),
        }
        code, actions, output = run_go(workspace, env)
        if code != 0 or "run" not in actions or "pass" not in actions:
            raise ValueError(f"正确实现上必须实际运行并通过 {TEST}\n{output}")
        original = (workspace / SOURCE).read_text()
        if original.count(ORIGINAL) != 1:
            raise ValueError("无法定位缺陷注入点")
        broken = scratch / "files.go"
        broken.write_text(original.replace(ORIGINAL, MUTATION, 1))
        overlay = scratch / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(workspace / SOURCE): str(broken)}}))
        code, actions, output = run_go(workspace, env, overlay)
        if code == 0 or "run" not in actions or "fail" not in actions:
            raise ValueError(f"放行陈旧编辑的缺陷版本上必须由 {TEST} 捕获错误\n{output}")
    print("正确实现通过，放行陈旧编辑的缺陷由同一测试检出")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("用法：python3 verify.py <agent_workspace>")
    try:
        verify(Path(sys.argv[1]).resolve())
    except ValueError as error:
        raise SystemExit(str(error))
    except (OSError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(125)
