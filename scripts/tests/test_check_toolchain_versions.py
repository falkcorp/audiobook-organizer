# file: scripts/tests/test_check_toolchain_versions.py
# version: 1.0.0
# guid: 741ea392-1f28-423c-ae7a-45e56620c26c
# last-edited: 2026-09-12
"""Tests for scripts/check_toolchain_versions.py (CI-04, CI-03).

The inline shell check this replaced truncated go.mod to major.minor, read only
the first ``go-version:`` in ci.yml, never looked at ``.envrc``, the Dockerfiles
or ``.vscode``, and only warned. Each drift test below is a case it let through
green. ``test_security_yml_node_20x_fails`` is the pre-fix ``security.yml``
(CI-03): the new gate must reject it.

Every test copies the real toolchain files into a temp dir and mutates the copy,
so the fixtures track the tree and nothing touches the checkout itself.
"""

from __future__ import annotations

import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "check_toolchain_versions.py"
COPIES = (
    "Makefile",
    ".envrc",
    ".vscode/settings.json",
    "go.mod",
    ".github/repository-config.yml",
)


class CheckToolchainVersionsTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._tmp.name)
        for rel in COPIES:
            dst = self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / rel, dst)
        for src in REPO_ROOT.glob("Dockerfile*"):
            if src.is_file():
                shutil.copy2(src, self.root / src.name)
        wf = self.root / ".github" / "workflows"
        wf.mkdir(parents=True, exist_ok=True)
        for src in (REPO_ROOT / ".github" / "workflows").glob("*.y*ml"):
            shutil.copy2(src, wf / src.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def run_check(self, *extra: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(self.root), *extra],
            capture_output=True, text=True, check=False,
        )

    def mutate(self, rel: str, old: str, new: str, *, occurrence: int = 1) -> None:
        """Replace the Nth occurrence of ``old`` in ``rel``; fail if it is absent."""
        path = self.root / rel
        text = path.read_text(encoding="utf-8")
        idx = -1
        for _ in range(occurrence):
            idx = text.find(old, idx + 1)
            self.assertNotEqual(idx, -1, f"{old!r} occurrence {occurrence} not in {rel}; fixture drifted")
        path.write_text(text[:idx] + new + text[idx + len(old):], encoding="utf-8")

    def assertFails(self, needle: str, *extra: str) -> None:
        res = self.run_check(*extra)
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn("::error", res.stdout)
        self.assertIn(needle, res.stdout)

    def test_current_tree_passes(self) -> None:
        res = self.run_check("--action-node-output", "22")
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("Toolchain versions consistent", res.stdout)

    def test_envrc_patch_drift_fails(self) -> None:
        self.mutate(".envrc", "GOTOOLCHAIN=go1.27.1", "GOTOOLCHAIN=go1.27.2")
        self.assertFails("go1.27.2 != Makefile pin")

    def test_vscode_second_pin_drift_fails(self) -> None:
        self.mutate(".vscode/settings.json", '"go1.27.1"', '"go1.27.0"', occurrence=2)
        self.assertFails("go1.27.0 != Makefile pin")

    def test_dockerfile_patch_drift_fails(self) -> None:
        self.mutate("Dockerfile.build-cgo", "FROM golang:1.27.1-alpine", "FROM golang:1.27.2-alpine")
        self.assertFails("golang:1.27.2 != Makefile pin")

    def test_dockerfile_digest_mismatch_fails(self) -> None:
        self.mutate("Dockerfile", "golang:1.27.1-alpine@sha256:c", "golang:1.27.1-alpine@sha256:0")
        self.assertFails("digests differ")

    def test_gomod_requiring_more_than_pin_fails(self) -> None:
        self.mutate("go.mod", "\ngo 1.27.0\n", "\ngo 1.27.2\n")
        self.assertFails("requires more than the pin")

    def test_gomod_toolchain_directive_fails(self) -> None:
        self.mutate("go.mod", "\ngo 1.27.0\n", "\ngo 1.27.0\n\ntoolchain go1.27.1\n")
        self.assertFails("toolchain")

    def test_later_ci_go_version_drift_fails(self) -> None:
        # The old check read only `head -1` of ci.yml; drift a later job.
        self.mutate(".github/workflows/ci.yml", "go-version: '1.27'", "go-version: '1.28'", occurrence=2)
        self.assertFails("go-version '1.28'")

    def test_makefile_pin_bump_alone_fails(self) -> None:
        self.mutate("Makefile", "export GOTOOLCHAIN := go1.27.1", "export GOTOOLCHAIN := go1.27.3")
        self.assertFails("!= Makefile pin go1.27.3")

    def test_security_yml_node_20x_fails(self) -> None:
        self.mutate(".github/workflows/security.yml", "node-version: '22'", "node-version: '20.x'")
        self.assertFails("node-version '20.x'")

    def test_top_level_versions_node_drift_fails(self) -> None:
        # repository-config.yml has two versions: blocks; the old grep -A1
        # read only the first. Drift the top-level one alone.
        self.mutate(".github/repository-config.yml", "versions:\n  node: ['22']", "versions:\n  node: ['20']")
        self.assertFails("versions.node blocks disagree")

    def test_action_output_mismatch_fails(self) -> None:
        self.assertFails("action node-version '20'", "--action-node-output", "20")

    def test_comments_and_expressions_are_ignored(self) -> None:
        path = self.root / ".github" / "workflows" / "zz-extra.yml"
        path.write_text(
            "jobs:\n  a:\n    steps:\n"
            "      # go-version: '1.99'\n"
            "      - uses: x/y@0000000000000000000000000000000000000000\n"
            "        with:\n          node-version: ${{ inputs.node }}\n",
            encoding="utf-8",
        )
        res = self.run_check()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)


if __name__ == "__main__":
    unittest.main()
