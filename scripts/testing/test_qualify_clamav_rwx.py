#!/usr/bin/env python3
"""Local self-test only; does not qualify RWX or a Kubernetes backend."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import uuid

SCRIPT = Path(__file__).with_name("qualify-clamav-rwx.py")


class LocalQualificationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nchat-rwx-selftest-")
        self.addCleanup(self.temp.cleanup)
        self.command = [sys.executable, str(SCRIPT), "--root", self.temp.name, "--run", uuid.uuid4().hex]
        self.invoke("init")

    def invoke(self, action, *arguments, expected=0):
        result = subprocess.run(self.command + ["--action", action, *arguments], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result

    def holder(self, action="hold"):
        process = subprocess.Popen(self.command + ["--action", action, "--shared", "--seconds", "0.3"],
                                   text=True, stdout=subprocess.PIPE)
        self.addCleanup(process.stdout.close)
        self.addCleanup(process.wait, timeout=5)
        self.assertEqual(json.loads(process.stdout.readline())["event"], "lock_acquired")
        return process

    def test_shared_readers_exclude_writer_and_release_on_crash(self):
        holder = self.holder()
        self.invoke("probe", "--shared")
        self.invoke("probe", expected=2)
        holder.kill()
        holder.wait(timeout=5)
        self.invoke("probe")

    def test_transaction_and_prune_exclude_snapshot(self):
        self.invoke("publish", "--sequence", "1")
        self.invoke("publish", "--sequence", "2")
        reader = self.holder("snapshot")
        self.assertEqual(json.loads(reader.stdout.readline())["event"], "snapshot_selected")
        self.invoke("prune", expected=2)
        reader.wait(timeout=5)
        self.invoke("snapshot", "--shared")
        self.invoke("prune")
        self.invoke("snapshot", "--shared")
        self.invoke("init", expected=1)

    def test_prune_never_follows_a_directory_link(self):
        self.invoke("publish")
        external = Path(self.temp.name) / "outside-run"
        external.mkdir()
        sentinel = external / "main"
        sentinel.write_text("preserve")
        root = Path(self.temp.name) / (".nchat-rwx-" + self.command[-1])
        (root / "generation-2").symlink_to(external, target_is_directory=True)
        self.invoke("prune", expected=1)
        self.assertEqual(sentinel.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
