#!/usr/bin/env python3
# file: .github/workflows/scripts/test_core_checkpoint.py
# version: 1.0.0
# guid: 7fa0e3aa-9656-4b8c-83c2-5649dd289161
# last-edited: 2026-11-08

"""Tests for core_checkpoint.py, the script of release-prod.yml's core-checkpoint job.

A workflow cannot be run in CI, so the job's whole decision is a script and these
tests run it against a throwaway repository with a bare local ``origin``: the
ref is recorded and pushed when the constant was bumped, and nothing is
recorded (the issue text is printed, the issue opened or updated through an
injected ``gh``) when it was not.

Stdlib ``unittest`` on purpose, like test_auto_revert.py.
"""

from __future__ import annotations

import contextlib
import io
import pathlib
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).parent))

import core_checkpoint as cc  # noqa: E402


def git(repo: pathlib.Path, *args: str) -> str:
    return subprocess.run(
        [
            "git",
            "-C",
            str(repo),
            "-c",
            "user.name=Bruce Wayne",
            "-c",
            "user.email=bruce@wayne.example",
            *args,
        ],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


class FakeGh:
    """Records gh calls; an open issue is whatever a prior `issue create` made."""

    def __init__(self) -> None:
        self.calls: list[list[str]] = []
        self.open_issue = ""

    def __call__(self, args: list[str]) -> str:
        self.calls.append(args)
        if args[:2] == ["issue", "list"]:
            return self.open_issue + "\n" if self.open_issue else "\n"
        if args[:2] == ["issue", "create"]:
            self.open_issue = "7"
            return "https://github.example/falkcorp/audiobook-organizer/issues/7\n"
        return ""


class CoreCheckpointTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        root = pathlib.Path(self._tmp.name)
        self.origin = root / "origin.git"
        subprocess.run(["git", "init", "-q", "--bare", str(self.origin)], check=True)
        self.work = root / "work"
        self.work.mkdir()
        git(self.work, "init", "-q", "-b", "main")
        git(self.work, "remote", "add", "origin", str(self.origin))

    # -- helpers -----------------------------------------------------------------

    def commit(
        self, version: str | None = None, core_file: str | None = None, doc: str | None = None
    ) -> str:
        if version is not None:
            path = self.work / "core" / "version" / "version.go"
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f'package version\n\nconst Version = "{version}"\n')
        if core_file is not None:
            path = self.work / "core" / "kernel" / "boot.go"
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"package kernel\n\n// {core_file}\n")
        if doc is not None:
            (self.work / "README.md").write_text(doc)
        git(self.work, "add", "-A")
        git(self.work, "commit", "-q", "--allow-empty", "-m", "change")
        return git(self.work, "rev-parse", "HEAD")

    def checkpoint(self, name: str, sha: str, push: bool = True) -> None:
        git(self.work, "update-ref", f"refs/core-checkpoints/{name}", sha)
        if push:
            git(self.work, "push", "-q", "origin", f"refs/core-checkpoints/{name}")

    def remote_checkpoints(self) -> list[str]:
        out = git(
            self.origin,
            "for-each-ref",
            "--format=%(refname) %(objectname)",
            "refs/core-checkpoints/",
        )
        return out.splitlines()

    def local_ref_exists(self, name: str) -> bool:
        res = subprocess.run(
            [
                "git",
                "-C",
                str(self.work),
                "show-ref",
                "--verify",
                "--quiet",
                f"refs/core-checkpoints/{name}",
            ]
        )
        return res.returncode == 0

    def run_job(self, *extra: str, gh: FakeGh | None = None) -> tuple[int, str]:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = cc.main(["--repo", str(self.work), *extra], gh=gh or FakeGh())
        return code, out.getvalue()

    # -- the decision ------------------------------------------------------------

    def test_first_checkpoint_is_recorded_and_pushed(self) -> None:
        head = self.commit(version="0.1.0", core_file="v1")
        code, out = self.run_job()
        self.assertEqual(code, 0, out)
        self.assertEqual(self.remote_checkpoints(), [f"refs/core-checkpoints/v0.1.0 {head}"])
        self.assertIn("recorded", out)

    def test_bumped_constant_records_the_next_checkpoint(self) -> None:
        first = self.commit(version="0.1.0", core_file="v1")
        self.checkpoint("v0.1.0", first)
        head = self.commit(version="0.2.0", core_file="v2")
        code, out = self.run_job()
        self.assertEqual(code, 0, out)
        self.assertEqual(
            self.remote_checkpoints(),
            [f"refs/core-checkpoints/v0.1.0 {first}", f"refs/core-checkpoints/v0.2.0 {head}"],
        )

    def test_unbumped_constant_records_nothing_and_prints_the_issue(self) -> None:
        first = self.commit(version="0.1.0", core_file="v1")
        self.checkpoint("v0.1.0", first)
        self.commit(core_file="v2")  # core/ changed, the constant did not
        gh = FakeGh()
        code, out = self.run_job("--open-issue", gh=gh)
        self.assertEqual(code, 0, out)
        self.assertEqual(self.remote_checkpoints(), [f"refs/core-checkpoints/v0.1.0 {first}"])
        local = git(
            self.work, "for-each-ref", "--format=%(refname)", "refs/core-checkpoints/"
        ).splitlines()
        self.assertEqual(local, ["refs/core-checkpoints/v0.1.0"])
        self.assertIn("bump-needed", out)
        self.assertIn(cc.ISSUE_TITLE, out)
        self.assertIn("core-version-bump-needed", out)
        self.assertIn(
            [
                "issue",
                "create",
                "--title",
                cc.ISSUE_TITLE,
                "--label",
                "core-version-bump-needed",
                "--body",
                cc.issue_body(cc.decide(self.work, "HEAD")),
            ],
            gh.calls,
        )

    def test_second_unbumped_release_updates_the_one_issue(self) -> None:
        first = self.commit(version="0.1.0", core_file="v1")
        self.checkpoint("v0.1.0", first)
        self.commit(core_file="v2")
        gh = FakeGh()
        self.run_job("--open-issue", gh=gh)
        self.commit(core_file="v3")
        self.run_job("--open-issue", gh=gh)
        creates = [c for c in gh.calls if c[:2] == ["issue", "create"]]
        edits = [c for c in gh.calls if c[:2] == ["issue", "edit"]]
        self.assertEqual(len(creates), 1)
        self.assertEqual(len(edits), 1)
        self.assertEqual(edits[0][2], "7")

    def test_issue_is_opened_only_when_asked(self) -> None:
        first = self.commit(version="0.1.0", core_file="v1")
        self.checkpoint("v0.1.0", first)
        self.commit(core_file="v2")
        gh = FakeGh()
        self.run_job(gh=gh)
        self.run_job("--open-issue", "--dry-run", gh=gh)
        self.assertEqual(gh.calls, [])

    def test_unchanged_core_records_nothing_even_with_a_higher_constant_elsewhere(self) -> None:
        first = self.commit(version="0.1.0", core_file="v1")
        self.checkpoint("v0.1.0", first)
        self.commit(doc="docs only")
        code, out = self.run_job("--open-issue")
        self.assertEqual(code, 0, out)
        self.assertIn("none", out)
        self.assertEqual(self.remote_checkpoints(), [f"refs/core-checkpoints/v0.1.0 {first}"])

    def test_dry_run_records_nothing(self) -> None:
        self.commit(version="0.1.0", core_file="v1")
        code, out = self.run_job("--dry-run")
        self.assertEqual(code, 0, out)
        self.assertIn("would record refs/core-checkpoints/v0.1.0", out)
        self.assertEqual(self.remote_checkpoints(), [])

    def test_a_remote_only_checkpoint_is_fetched_before_deciding(self) -> None:
        first = self.commit(version="0.3.0", core_file="v1")
        self.checkpoint("v0.3.0", first)
        git(self.work, "update-ref", "-d", "refs/core-checkpoints/v0.3.0")  # only origin has it now
        self.commit(core_file="v2")
        code, out = self.run_job()
        self.assertEqual(code, 0, out)
        self.assertIn("bump-needed", out)  # without the fetch this would have recorded v0.3.0 again

    def test_checkpoint_is_derived_from_the_constant(self) -> None:
        self.commit(version="0.12.4", core_file="v1")
        self.run_job()
        self.assertEqual(
            [r.split()[0] for r in self.remote_checkpoints()], ["refs/core-checkpoints/v0.12.4"]
        )

    # -- refusals ----------------------------------------------------------------

    def test_a_constant_that_is_not_0_y_z_is_refused(self) -> None:
        for version in ("1.0.0", "0.2.0-rc.1"):
            with self.subTest(version=version):
                self.commit(version=version, core_file=f"v{version}")
                code, out = self.run_job()
                self.assertEqual(code, 1, out)
                self.assertIn("not 0.Y.Z", out)
                self.assertEqual(self.remote_checkpoints(), [])

    def test_a_failed_push_leaves_no_local_ref(self) -> None:
        self.commit(version="0.1.0", core_file="v1")
        git(
            self.work, "remote", "set-url", "--push", "origin", str(self.origin.parent / "gone.git")
        )
        code, out = self.run_job()
        self.assertEqual(code, 1, out)
        self.assertFalse(self.local_ref_exists("v0.1.0"))

    def test_an_unreachable_remote_refuses_to_decide(self) -> None:
        self.commit(version="0.1.0", core_file="v1")
        git(self.work, "remote", "set-url", "origin", str(self.origin.parent / "gone.git"))
        code, out = self.run_job()
        self.assertEqual(code, 1, out)
        self.assertIn("cannot decide", out)

    def test_a_commit_without_the_constant_is_refused(self) -> None:
        self.commit(doc="no core yet")
        code, out = self.run_job()
        self.assertEqual(code, 1, out)
        self.assertIn("no core/version/version.go", out)


if __name__ == "__main__":
    unittest.main()
