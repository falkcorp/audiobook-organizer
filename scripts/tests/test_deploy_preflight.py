# file: scripts/tests/test_deploy_preflight.py
# version: 1.0.0
# guid: 9c4b2e1a-6d3f-4f8b-a1c7-5e0d8b7a2f13
# last-edited: 2026-09-11
"""Tests for scripts/deploy-preflight.sh (todo.d CI-01).

The guard the deploy targets used to run inline, ``git merge-base
--is-ancestor origin/main HEAD``, passes when HEAD is strictly AHEAD of
origin/main, so unpushed, unreviewed commits could ship. ``test_ahead_is_refused``
is the case that guard got wrong; ``test_legacy_is_ancestor_guard_accepted_ahead``
pins why it was replaced, so the regression stays legible.

Each test builds a real bare "origin" and a clone in a temp dir, so the script's
``git fetch`` runs for real; nothing here touches the checkout the tests run in.
"""

from __future__ import annotations

import os
import pathlib
import re
import subprocess
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "deploy-preflight.sh"
MAKEFILE_EXAMPLE = REPO_ROOT / "Makefile.local.example"

GIT_ENV = {
    **os.environ,
    "GIT_AUTHOR_NAME": "t",
    "GIT_AUTHOR_EMAIL": "t@example.com",
    "GIT_COMMITTER_NAME": "t",
    "GIT_COMMITTER_EMAIL": "t@example.com",
    "GIT_CONFIG_GLOBAL": "/dev/null",
    "GIT_CONFIG_NOSYSTEM": "1",
}


def _git(cwd: pathlib.Path, *args: str) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, env=GIT_ENV, check=True, capture_output=True, text=True
    ).stdout.strip()


def _commit(cwd: pathlib.Path, name: str) -> None:
    (cwd / name).write_text(name + "\n")
    _git(cwd, "add", name)
    _git(cwd, "commit", "-q", "-m", name)


class _Repos:
    """A bare origin with one commit on main, a clone at that commit, and a
    second clone used only to push commits the first clone has not pulled."""

    def __init__(self, tmp: pathlib.Path) -> None:
        self.origin = tmp / "origin.git"
        self.local = tmp / "local"
        self.other = tmp / "other"
        _git(tmp, "init", "-q", "--bare", "-b", "main", str(self.origin))
        _git(tmp, "clone", "-q", str(self.origin), str(self.local))
        _git(self.local, "checkout", "-q", "-b", "main")
        _commit(self.local, "base")
        _git(self.local, "push", "-q", "-u", "origin", "main")
        _git(tmp, "clone", "-q", str(self.origin), str(self.other))

    def push_from_other(self, name: str) -> None:
        _commit(self.other, name)
        _git(self.other, "push", "-q", "origin", "main")


def _preflight(cwd: pathlib.Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT)], cwd=cwd, env=GIT_ENV, capture_output=True, text=True
    )


def _legacy_guard(cwd: pathlib.Path) -> int:
    """The pre-CI-01 inline check, verbatim from Makefile.local.example."""
    subprocess.run(["git", "fetch", "-q", "origin", "main"], cwd=cwd, env=GIT_ENV, check=True)
    return subprocess.run(
        ["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"], cwd=cwd, env=GIT_ENV
    ).returncode


class DeployPreflightTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repos = _Repos(pathlib.Path(self._tmp.name))

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_exactly_origin_main_passes(self) -> None:
        r = _preflight(self.repos.local)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("HEAD == origin/main", r.stdout)

    def test_ahead_is_refused(self) -> None:
        """An unpushed local commit must NOT ship. This is the case the old
        is-ancestor guard let through."""
        _commit(self.repos.local, "unpushed")
        r = _preflight(self.repos.local)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertRegex(r.stderr, r"1 commit\(s\) ahead of and 0 commit\(s\) behind origin/main")
        self.assertIn("never went through review or CI", r.stderr)

    def test_behind_is_refused(self) -> None:
        self.repos.push_from_other("remote-only")
        r = _preflight(self.repos.local)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertRegex(r.stderr, r"0 commit\(s\) ahead of and 1 commit\(s\) behind origin/main")
        self.assertIn("Pull first", r.stderr)

    def test_diverged_is_refused(self) -> None:
        self.repos.push_from_other("remote-only")
        _commit(self.repos.local, "unpushed")
        r = _preflight(self.repos.local)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertRegex(r.stderr, r"1 commit\(s\) ahead of and 1 commit\(s\) behind origin/main")

    def test_fetches_before_comparing(self) -> None:
        """A stale origin/main ref must not make a behind checkout look current:
        the script has to fetch, not trust the tracking ref it finds."""
        self.repos.push_from_other("remote-only")
        # Sanity: the local tracking ref still equals HEAD until something fetches.
        self.assertEqual(_git(self.repos.local, "rev-parse", "HEAD"), _git(self.repos.local, "rev-parse", "origin/main"))
        r = _preflight(self.repos.local)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)

    def test_legacy_is_ancestor_guard_accepted_ahead(self) -> None:
        """Documents the bug: the inline guard the targets ran before CI-01
        passed with an unpushed commit on HEAD. If git ever changes this, the
        script above is still the check to keep."""
        _commit(self.repos.local, "unpushed")
        self.assertEqual(_legacy_guard(self.repos.local), 0)

    def test_bad_ref_argument_is_a_usage_error(self) -> None:
        r = subprocess.run(
            ["bash", str(SCRIPT), "nomain"], cwd=self.repos.local, env=GIT_ENV, capture_output=True, text=True
        )
        self.assertEqual(r.returncode, 2)


class MakefileExampleWiringTests(unittest.TestCase):
    """Both deploy targets must call the script; an inline guard is how the
    one-sided check crept in, so a re-inlined one fails here."""

    def _target_body(self, name: str) -> str:
        text = MAKEFILE_EXAMPLE.read_text()
        m = re.search(rf"^{re.escape(name)}:.*?\n((?:\t.*\n)+)", text, re.M)
        if m is None:
            self.fail(f"target {name} not found in Makefile.local.example")
        return m.group(1)

    def test_deploy_targets_call_the_script(self) -> None:
        for target in ("deploy", "deploy-debug"):
            body = self._target_body(target)
            self.assertIn("scripts/deploy-preflight.sh", body, target)
            self.assertNotIn("--is-ancestor", body, target)


if __name__ == "__main__":
    unittest.main()
