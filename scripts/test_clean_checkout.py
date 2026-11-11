"""核对快照只包含提交与显式候选，防止本机文件掩盖缺失夹具。"""

import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("clean_checkout", Path(__file__).with_name("check_clean_checkout.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SnapshotTest(unittest.TestCase):
    def test_only_committed_and_selected_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            module.git(root, "init", "-q")
            (root / "tracked.txt").write_text("已提交")
            (root / ".gitignore").write_text("secret.txt\n")
            module.git(root, "add", "tracked.txt", ".gitignore")
            module.git(root, "-c", "user.name=测试", "-c", "user.email=test@example.invalid",
                       "commit", "-qm", "合成测试输入")
            (root / "tracked.txt").write_text("本机改动")
            (root / "candidate.txt").write_text("候选夹具")
            (root / "secret.txt").write_text("不得复制")
            (root / "other.txt").write_text("无关未跟踪文件")
            (root / "fixtures/workspace").mkdir(parents=True)
            (root / "fixtures/workspace/input.txt").write_text("合成工作区")
            destination = Path(directory) / "snapshot"
            destination.mkdir()
            module.snapshot(root, destination, "HEAD", ["candidate.txt", "fixtures"])
            self.assertEqual((destination / "tracked.txt").read_text(), "已提交")
            self.assertEqual((destination / "candidate.txt").read_text(), "候选夹具")
            self.assertFalse((destination / "secret.txt").exists())
            self.assertFalse((destination / "other.txt").exists())
            self.assertEqual((destination / "fixtures/workspace/input.txt").read_text(), "合成工作区")
            for path in ["secret.txt", "../outside", ".", ".env", "workspace"]:
                with self.subTest(path=path), self.assertRaises(ValueError):
                    module.snapshot(root, destination, "HEAD", [path])
            (root / "linked").symlink_to(root / "fixtures", target_is_directory=True)
            with self.assertRaises(ValueError):
                module.snapshot(root, destination, "HEAD", ["linked/workspace/input.txt"])
            (destination / "candidate.txt").unlink()
            (destination / "candidate.txt").symlink_to(root / "other.txt")
            with self.assertRaises(ValueError):
                module.snapshot(root, destination, "HEAD", ["candidate.txt"])


if __name__ == "__main__":
    unittest.main()
