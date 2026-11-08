# file: scripts/tests/test_check_commits_build.py
# version: 1.1.0
# guid: 6f66fba8-e2b8-4472-80c2-7f0130d4b25d
# last-edited: 2026-11-08
"""Tests for scripts/ci/check_commits_build.py (09 §9.2.1 extraction-gates; MOD9-5).

Every case builds a throwaway git repository holding two tiny Go modules, a root
module that requires ``example.com/core`` through ``replace => ./core`` (never a
go.work), and a short history, then runs the real script against it:

  a. every commit builds, a file is renamed into core/ at 100 % similarity: exit 0;
  b. a middle commit breaks the root build: exit 1 naming that commit;
  c. the move commit rewrites the file below 90 % similarity: exit 1 naming the file;
  d. a middle commit breaks core's build: exit 1 naming that commit;
  e. the file is deleted in one commit and added under core/ in the next (git
     cannot see a rename in either): exit 1 naming the file;
  f. no rename into core/ (an edit, and a plain add under core/): exit 0 and the
     "nothing to check" line, with no build run.

Needs git and go on PATH; skipped without them. GOTOOLCHAIN=local keeps the
fixture's go directive from fetching a toolchain.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/ci/check_commits_build.py"

ROOT_GO_MOD = """module example.com/root

go 1.21

require example.com/core v0.0.0

replace example.com/core => ./core
"""
CORE_GO_MOD = "module example.com/core\n\ngo 1.21\n"
MAIN_GO = """package main

import (
	"fmt"

	"example.com/core/lib"
)

func main() { fmt.Println(lib.Hello()) }
"""
LIB_GO = 'package lib\n\nfunc Hello() string { return "hello" }\n'
# A twenty-line file: a five-line rewrite keeps it above git's 50 % rename
# floor and below the 90 % threshold the script enforces.
MOVER_GO = "package mover\n\n" + "".join(f"// Line {n} of the mover.\n" for n in range(1, 19))
MOVER_GO_EDITED = "package mover\n\n" + "".join(
    f"// Line {n} of the mover.\n" if n > 5 else f"// Rewritten line {n}.\n" for n in range(1, 19)
)

ENV = {
    **os.environ,
    "GOTOOLCHAIN": "local",
    "GOWORK": "off",
    "GOFLAGS": "",
    "GIT_CONFIG_GLOBAL": os.devnull,
    "GIT_CONFIG_SYSTEM": os.devnull,
}


@unittest.skipUnless(shutil.which("git") and shutil.which("go"), "needs git and go on PATH")
class CheckCommitsBuildTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.write("go.mod", ROOT_GO_MOD)
        self.write("main.go", MAIN_GO)
        self.write("core/go.mod", CORE_GO_MOD)
        self.write("core/lib/lib.go", LIB_GO)
        self.write("internal/mover/mover.go", MOVER_GO)
        self.base = self.commit("base")

    # -- helpers -------------------------------------------------------------

    def git(self, *args: str) -> str:
        res = subprocess.run(
            ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", *args],
            cwd=self.repo,
            env=ENV,
            capture_output=True,
            text=True,
            check=True,
        )
        return res.stdout.strip()

    def write(self, rel: str, text: str) -> None:
        path = self.repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self, message: str) -> str:
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)
        return self.git("rev-parse", "HEAD")

    def move(self) -> None:
        (self.repo / "core/mover").mkdir(parents=True, exist_ok=True)
        self.git("mv", "internal/mover/mover.go", "core/mover/mover.go")

    def run_script(self, *extra: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--base", self.base, *extra],
            cwd=self.repo,
            env=ENV,
            capture_output=True,
            text=True,
        )

    # -- cases ---------------------------------------------------------------

    def test_all_commits_build_and_pure_rename_passes(self):
        self.move()
        self.commit("move the mover into core")
        self.write("main.go", MAIN_GO + "\n// touched\n")
        self.commit("touch main")
        self.write("main.go", MAIN_GO + "\n// touched again\n")
        self.commit("touch main again")
        res = self.run_script()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertNotIn("FAIL", res.stdout)

    def test_breaking_middle_commit_is_named(self):
        self.move()
        self.commit("move the mover into core")
        self.write("main.go", MAIN_GO + "\nfunc broken( {\n")
        bad = self.commit("break the root build")
        self.write("main.go", MAIN_GO)
        self.commit("repair the root build")
        res = self.run_script()
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn(bad[:12], res.stdout)
        self.assertIn("root", res.stdout)

    def test_breaking_core_commit_is_named(self):
        self.move()
        self.commit("move the mover into core")
        self.write("core/lib/lib.go", LIB_GO + "\nfunc broken( {\n")
        bad = self.commit("break the core build")
        self.write("core/lib/lib.go", LIB_GO)
        self.commit("repair the core build")
        res = self.run_script()
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn(bad[:12], res.stdout)
        self.assertIn("core", res.stdout)

    def test_move_commit_below_90_percent_similarity_names_the_file(self):
        self.move()
        self.write("core/mover/mover.go", MOVER_GO_EDITED)
        moved = self.commit("move the mover into core and rewrite it")
        res = self.run_script()
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn("core/mover/mover.go", res.stdout)
        self.assertIn(moved[:12], res.stdout)
        self.assertIn("90", res.stdout)

    def test_delete_then_add_is_not_a_detected_move(self):
        self.git("rm", "-q", "internal/mover/mover.go")
        self.commit("delete the mover")
        self.write("core/mover/mover.go", MOVER_GO)
        self.commit("add the mover under core")
        res = self.run_script()
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn("core/mover/mover.go", res.stdout)

    def test_no_rename_into_core_has_nothing_to_check(self):
        self.write("main.go", MAIN_GO + "\nfunc broken( {\n")  # would fail a build
        self.commit("break main, no move in this PR")
        self.write("core/lib/extra.go", "package lib\n")  # a plain add under core/
        self.commit("add a file under core")
        res = self.run_script()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("no rename into core/: nothing to check", res.stdout)

    def test_temporary_worktrees_are_removed(self):
        self.move()
        self.commit("move the mover into core")
        self.assertEqual(self.run_script().returncode, 0)
        listed = self.git("worktree", "list", "--porcelain")
        self.assertEqual(listed.count("worktree "), 1, listed)

    def test_shards_split_the_commits_and_only_one_reports_the_bad_commit(self):
        self.move()
        self.commit("move the mover into core")
        self.write("main.go", MAIN_GO + "\n// touched\n")
        self.commit("touch main")
        self.write("main.go", MAIN_GO + "\nfunc broken( {\n")
        bad = self.commit("break the root build")
        self.write("main.go", MAIN_GO)
        self.commit("repair the root build")
        first = self.run_script("--shard", "1/2")
        second = self.run_script("--shard", "2/2")
        self.assertEqual(first.returncode, 0, first.stdout + first.stderr)
        self.assertIn("2 of 4", first.stdout)
        self.assertEqual(second.returncode, 1, second.stdout + second.stderr)
        self.assertIn(bad[:12], second.stdout)

    def test_similarity_is_reported_by_shard_one_only(self):
        self.move()
        self.write("core/mover/mover.go", MOVER_GO_EDITED)
        self.commit("move the mover into core and rewrite it")
        self.write("main.go", MAIN_GO + "\n// touched\n")
        self.commit("touch main")
        first = self.run_script("--shard", "1/2")
        second = self.run_script("--shard", "2/2")
        self.assertEqual(first.returncode, 1, first.stdout + first.stderr)
        self.assertIn("core/mover/mover.go", first.stdout)
        self.assertEqual(second.returncode, 0, second.stdout + second.stderr)
        self.assertNotIn("FAIL", second.stdout)

    def test_more_shards_than_commits_leaves_spare_shards_empty(self):
        self.move()
        self.commit("move the mover into core")
        res = self.run_script("--shard", "3/3")
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("no commits to build", res.stdout)


class ShardSliceTests(unittest.TestCase):
    def test_slices_are_contiguous_near_equal_and_cover_every_commit(self):
        sys.path.insert(0, str(SCRIPT.parent))
        try:
            import check_commits_build as ccb
        finally:
            sys.path.pop(0)
        for total in range(0, 12):
            commits = [f"c{i}" for i in range(total)]
            for n in range(1, 6):
                parts = [ccb.shard_slice(commits, k, n) for k in range(1, n + 1)]
                self.assertEqual(sum(parts, []), commits, (total, n))
                sizes = [len(p) for p in parts]
                self.assertLessEqual(max(sizes) - min(sizes), 1, (total, n))

    def test_bad_shard_values_are_rejected(self):
        sys.path.insert(0, str(SCRIPT.parent))
        try:
            import check_commits_build as ccb
        finally:
            sys.path.pop(0)
        for bad in ("0/4", "5/4", "1/0", "x/4", "4"):
            with self.assertRaises(Exception, msg=bad):
                ccb.parse_shard(bad)


if __name__ == "__main__":
    unittest.main()
