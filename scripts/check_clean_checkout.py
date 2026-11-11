#!/usr/bin/env python3
"""导出已提交源码及显式候选变更，在临时目录运行无模型密钥的检查。"""

import argparse
import io
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile


def git(root, *args, **kwargs):
    return subprocess.run(["git", "-C", str(root), *args], check=True,
                          stdout=subprocess.PIPE, **kwargs).stdout


def snapshot(root, destination, revision, overlays):
    archive = git(root, "archive", "--format=tar", revision)
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        source.extractall(destination, filter="data")
    copied = []
    for raw in overlays:
        relative = Path(raw)
        if (relative.is_absolute() or ".." in relative.parts or not relative.parts
                or relative.parts[0] == "workspace"
                or any(p in {".git", ".claw", ".env"} for p in relative.parts)):
            raise ValueError(f"候选路径必须明确位于源码范围内：{raw}")
        source = root / relative
        if not source.exists() or source.is_symlink():
            raise ValueError(f"候选路径不存在或为符号链接：{raw}")
        current = source.parent
        while current != root:
            if current.is_symlink():
                raise ValueError(f"候选路径的上级包含符号链接：{raw}")
            current = current.parent
        files = sorted(source.rglob("*")) if source.is_dir() else [source]
        for file in files:
            rel = file.relative_to(root)
            if file.name == ".DS_Store":
                continue
            if file.is_symlink():
                raise ValueError(f"候选路径不能含符号链接：{rel}")
            if file.is_dir():
                continue
            if rel.parts[0] == "workspace" or any(p in {".git", ".claw", ".env", "__pycache__"} for p in rel.parts):
                raise ValueError(f"候选路径包含运行数据：{rel}")
            ignored = subprocess.run(["git", "-C", str(root), "check-ignore", "-q", "--", str(rel)])
            if ignored.returncode not in (0, 1):
                raise ValueError(f"无法核对 Git 忽略规则：{rel}")
            if ignored.returncode == 0:
                raise ValueError(f"候选文件仍被 Git 忽略：{rel}")
            target = destination / rel
            # Git 历史中的链接不能改变候选覆盖的目的地。
            current = target
            while current != destination:
                if current.is_symlink():
                    raise ValueError(f"候选目的地包含符号链接：{rel}")
                current = current.parent
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(file, target)
            copied.append(str(rel))
    return copied


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--revision", default="HEAD", help="导出的提交，默认 HEAD")
    parser.add_argument("--overlay", action="append", default=[],
                        help="覆盖指定候选文件或目录，可重复；不复制其他未提交文件，不支持删除变更")
    parser.add_argument("--check", choices=["all", "build", "test", "race"], default="all")
    args = parser.parse_args()
    root = Path(git(Path.cwd(), "rev-parse", "--show-toplevel").decode().strip())
    commands = {
        "build": ["go", "build", "./cmd/...", "./internal/..."],
        "test": ["go", "test", "-count=1", "./cmd/...", "./internal/..."],
        "race": ["go", "test", "-race", "-count=1", "./internal/..."],
    }
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("OPENAI_", "DEEPSEEK_", "ZHIPU_", "ANTHROPIC_", "CLAW_"))}
    with tempfile.TemporaryDirectory(prefix="claw-clean-check-") as directory:
        destination = Path(directory) / "source"
        destination.mkdir()
        copied = snapshot(root, destination, args.revision, args.overlay)
        print(f"隔离源码：{args.revision}，显式候选文件 {len(set(copied))} 个", flush=True)
        for name, command in commands.items():
            if args.check not in ("all", name):
                continue
            print(f"执行 {name}：{' '.join(command)}", flush=True)
            result = subprocess.run(command, cwd=destination, env=environment)
            if result.returncode:
                return result.returncode
    print("隔离源码检查通过（未调用真实模型）", flush=True)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"干净检出检查失败：{error}")
