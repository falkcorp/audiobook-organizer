#!/usr/bin/env python3
# file: .github/workflows/scripts/test_auto_revert.py
# version: 1.2.0
# guid: 8b2f6019-4d5a-4c88-b3e7-16f9a0c7d243
# last-edited: 2026-08-20

"""Tests for the auto-revert span selector.

Stdlib ``unittest`` on purpose: no pip step, so this runs identically on a laptop
and on a bare CI runner. A test that needs a dependency installed is a test that
eventually stops running.

The fixtures mirror the shapes actually measured on ``main`` on 2026-08-20 —
``cancelled`` runs from the ``cancel-in-progress`` concurrency group, and
``[skip ci]`` collector commits with no run at all — because those, not clean
red/green alternation, are what the selector will really see.
"""

from __future__ import annotations

import os
import pathlib
import shutil
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from auto_revert import (  # noqa: E402
    AUTO_REVERT_TRAILER,
    Commit,
    latest_conclusion_by_sha,
    parse_log,
    render_commit_message,
    render_issue_body,
    select_span,
)

F = "f" * 40  # the commit whose gate run failed
G = "g" * 40  # a commit with a green gate run
C = "c" * 40  # a commit whose run was cancelled — never verified
S = "s" * 40  # a `[skip ci]` commit with no run at all
R = "r" * 40  # a previous auto-revert


def c(sha: str, subject: str = "some change", **kw: object) -> Commit:
    return Commit(sha=sha, subject=subject, **kw)  # type: ignore[arg-type]


class LatestConclusionBySha(unittest.TestCase):
    def test_latest_run_per_sha_wins(self):
        runs = [
            {"headSha": "aaa", "conclusion": "failure", "run_number": 10},
            {"headSha": "aaa", "conclusion": "success", "run_number": 11},
            {"headSha": "bbb", "conclusion": "cancelled", "run_number": 9},
        ]
        self.assertEqual(
            latest_conclusion_by_sha(runs), {"aaa": "success", "bbb": "cancelled"}
        )

    def test_snake_case_head_sha_is_accepted(self):
        """`gh run list --json` emits headSha; the REST API emits head_sha."""
        self.assertEqual(
            latest_conclusion_by_sha([{"head_sha": "aaa", "conclusion": "success"}]),
            {"aaa": "success"},
        )

    def test_null_conclusion_becomes_empty_string(self):
        """An in-progress run has conclusion null. It must not read as green."""
        self.assertEqual(
            latest_conclusion_by_sha([{"headSha": "aaa", "conclusion": None}]), {"aaa": ""}
        )


class SelectSpanHappyPaths(unittest.TestCase):
    def test_single_bad_commit_on_top_of_green(self):
        d = select_span([c(F, "broke it"), c(G, "was fine")], {G: "success"})
        self.assertEqual(d.action, "revert")
        self.assertEqual(d.span_shas, [F])
        self.assertEqual(d.anchor_sha, G)

    def test_cancelled_commits_are_swept_into_the_span(self):
        """A cancelled run judged nothing, so its commit stays a suspect."""
        d = select_span(
            [c(F, "red"), c(C, "cancelled"), c(G, "green")],
            {C: "cancelled", G: "success"},
        )
        self.assertEqual(d.action, "revert")
        self.assertEqual(d.span_shas, [F, C])
        self.assertEqual(d.anchor_sha, G)

    def test_skip_ci_commit_is_not_an_anchor_but_is_in_the_span(self):
        """The TODO collector's `[skip ci]` commit has no run; it vouches for nothing."""
        d = select_span(
            [c(F, "red"), c(S, "docs(todo): collect [skip ci]"), c(G, "green")],
            {G: "success"},  # deliberately no entry for S
        )
        self.assertEqual(d.action, "revert")
        self.assertEqual(d.span_shas, [F, S])
        self.assertEqual(d.anchor_sha, G)

    def test_failing_commit_cannot_anchor_itself(self):
        """Even if the failing SHA carries a green record, it is not the anchor."""
        d = select_span([c(F, "red"), c(G, "green")], {F: "success", G: "success"})
        self.assertEqual(d.anchor_sha, G)
        self.assertEqual(d.span_shas, [F])


class SelectSpanRefusals(unittest.TestCase):
    def test_loop_guard_refuses_to_revert_a_revert(self):
        d = select_span(
            [
                c(F, "still red"),
                c(R, "revert: bad thing", trailers=f"{AUTO_REVERT_TRAILER} deadbeef"),
                c(G, "green"),
            ],
            {G: "success"},
        )
        self.assertEqual(d.action, "report")
        self.assertEqual(d.label, "needs-manual-revert")
        self.assertIn("would loop", d.reason)

    def test_span_wider_than_max_is_refused(self):
        commits = [c(x * 40) for x in "fabc"] + [c(G)]
        d = select_span(commits, {G: "success"}, max_span=3)
        self.assertEqual(d.action, "report")
        self.assertIn("limit 3", d.reason)
        # The span is still reported so the issue can name the suspects.
        self.assertEqual(len(d.span), 4)

    def test_span_exactly_at_max_is_allowed(self):
        commits = [c(x * 40) for x in "fab"] + [c(G)]
        d = select_span(commits, {G: "success"}, max_span=3)
        self.assertEqual(d.action, "revert")
        self.assertEqual(len(d.span), 3)

    def test_no_green_anchor_in_window_is_refused(self):
        d = select_span(
            [c(F), c(C), c("d" * 40)], {C: "cancelled", "d" * 40: "cancelled"}
        )
        self.assertEqual(d.action, "report")
        self.assertIn("no green commit", d.reason)
        self.assertEqual(d.span, [])

    def test_empty_history_does_nothing(self):
        self.assertEqual(select_span([], {}).action, "none")


class IssueBody(unittest.TestCase):
    def test_revert_body_names_every_commit_and_its_verdict(self):
        commits = [c(F, "broke it", pr=99), c(C, "unverified"), c(G, "green")]
        conclusions = {F: "failure", C: "cancelled", G: "success"}
        body = render_issue_body(
            select_span(commits, conclusions),
            conclusions,
            run_url="https://example/run/1",
            failed_jobs=["Minimal CI"],
        )
        for expected in (
            "Reverted commits",
            "(#99)",
            "**gate red**",
            "never verified",
            "https://example/run/1",
            "`Minimal CI`",
            "git revert",
        ):
            self.assertIn(expected, body)

    def test_refusal_body_says_it_did_not_revert(self):
        body = render_issue_body(select_span([c(F), c(C)], {C: "cancelled"}), {})
        self.assertIn("**not** reverted automatically", body)
        self.assertIn("by hand", body)
        self.assertNotIn("Reverted commits", body)

    def test_skip_ci_commits_are_labelled_as_such(self):
        commits = [c(F, "red"), c(S, "collect [skip ci]"), c(G, "green")]
        conclusions = {F: "failure", G: "success"}
        body = render_issue_body(select_span(commits, conclusions), conclusions)
        self.assertIn("no gate run (`[skip ci]`)", body)


class GithubOutputEncoding(unittest.TestCase):
    """A commit subject is attacker-controlled by anyone who can open a PR."""

    def _write(self, name: str, value: str) -> str:
        import tempfile

        import auto_revert

        with tempfile.NamedTemporaryFile("w+", delete=False) as fh:
            path = fh.name
        os.environ["GITHUB_OUTPUT"] = path
        try:
            auto_revert._write_output(name, value)
            return pathlib.Path(path).read_text()
        finally:
            os.environ.pop("GITHUB_OUTPUT", None)
            pathlib.Path(path).unlink(missing_ok=True)

    def test_single_line_value_is_plain(self):
        self.assertEqual(self._write("action", "revert"), "action=revert\n")

    def test_multiline_value_uses_a_heredoc(self):
        written = self._write("body", "line one\nline two")
        self.assertIn("body<<__AUTOREVERT_EOF__\n", written)
        self.assertTrue(written.endswith("__AUTOREVERT_EOF__\n"))

    def test_a_commit_subject_cannot_close_the_heredoc_early(self):
        """Without the guard this would inject an arbitrary second output."""
        evil = "harmless\n__AUTOREVERT_EOF__\nadmin=true\nmore"
        written = self._write("body", evil)
        # Exactly two delimiter lines: the opener's and the real terminator.
        bare = [ln for ln in written.split("\n") if ln == "__AUTOREVERT_EOF__"]
        self.assertEqual(len(bare), 1, f"delimiter leaked into the value:\n{written}")
        self.assertIn("admin=true", written)  # still present, just inert


class TrailerRoundTripThroughRealGit(unittest.TestCase):
    """The loop guard's only input crosses a git boundary — so cross it for real.

    ``render_commit_message`` writes the trailer and ``Commit.is_auto_revert``
    reads it back, but git's trailer parser sits between them and it only reads
    the *last paragraph* of a message. Fixtures that hand ``trailers=`` straight
    to :class:`Commit` test both halves and never the join. If the join breaks,
    the guard silently stops guarding and the failure mode is a revert loop on
    ``main``, so these tests shell out to the real thing.
    """

    def _repo(self) -> pathlib.Path:
        import subprocess
        import tempfile

        root = pathlib.Path(tempfile.mkdtemp()) / "repo"
        root.mkdir()
        self.addCleanup(shutil.rmtree, root.parent, ignore_errors=True)
        # Deliberately outside the work tree: a message file inside it would be
        # swept up by the next `git add -A` and end up in the committed tree,
        # which the tree-equality assertion below would then be comparing by luck.
        self.msg_path = root.parent / "message.txt"
        self.git = lambda *a: subprocess.run(
            ["git", *a], cwd=root, capture_output=True, text=True, check=True
        )
        self.git("init", "-q", "-b", "main", ".")
        self.git("config", "user.email", "t@example.invalid")
        self.git("config", "user.name", "T")
        return root

    def _commit(self, root: pathlib.Path, text: str, message: str) -> str:
        (root / "f.txt").write_text(text)
        self.git("add", "-A")
        self.msg_path.write_text(message)
        self.git("commit", "-q", "--file", str(self.msg_path))
        return self.git("rev-parse", "HEAD").stdout.strip()

    def _log(self, n: int = 5) -> list[Commit]:
        """Read history back exactly the way the workflow does."""
        from auto_revert import LOG_FORMAT

        return parse_log(self.git("log", f"--format={LOG_FORMAT}", "-n", str(n)).stdout)

    def test_generated_message_parses_back_as_an_auto_revert(self):
        root = self._repo()
        self._commit(root, "base\n", "green base")
        message = render_commit_message(
            "a" * 40, "b" * 40, ["c" * 40, "d" * 40],
            run_url="https://github.com/o/r/actions/runs/1",
        )
        self._commit(root, "reverted\n", message)

        commits = self._log()
        self.assertTrue(
            commits[0].is_auto_revert,
            f"loop guard would not fire; git returned trailers={commits[0].trailers!r}",
        )
        # The `Failing run:` line is trailer-shaped too. It must NOT be folded in,
        # or a future edit that reorders the paragraphs goes unnoticed.
        self.assertNotIn("Failing run", commits[0].trailers)
        for sha in ("c" * 40, "d" * 40):
            self.assertIn(sha, commits[0].trailers)

    def test_an_ordinary_commit_does_not_look_like_an_auto_revert(self):
        """Negative control: without this, a parser returning the whole body passes."""
        root = self._repo()
        self._commit(root, "base\n", "feat: something\n\nFailing run: https://x\n")
        self.assertFalse(self._log()[0].is_auto_revert)

    def test_message_without_a_run_url_still_carries_trailers(self):
        root = self._repo()
        self._commit(root, "base\n", "green")
        self._commit(root, "x\n", render_commit_message("a" * 40, "b" * 40, ["c" * 40]))
        self.assertTrue(self._log()[0].is_auto_revert)

    def test_reverting_a_span_restores_the_anchor_tree_exactly(self):
        """`git revert --no-commit ANCHOR..FAILING` is the apply step's core call."""
        root = self._repo()
        self._commit(root, "base\n", "green base")
        anchor = self.git("rev-parse", "HEAD").stdout.strip()
        anchor_tree = self.git("rev-parse", "HEAD^{tree}").stdout.strip()
        for n in (1, 2, 3):
            self._commit(root, f"base\nline {n}\n", f"commit {n}")
        failing = self.git("rev-parse", "HEAD").stdout.strip()

        self.git("revert", "--no-commit", f"{anchor}..{failing}")
        self._commit(root, (root / "f.txt").read_text(),
                     render_commit_message(anchor, failing, [failing]))
        self.assertEqual(
            self.git("rev-parse", "HEAD^{tree}").stdout.strip(),
            anchor_tree,
            "revert did not restore the anchor's tree",
        )

    def test_a_conflicting_revert_aborts_back_to_a_clean_tree(self):
        """The apply step runs `git revert --abort` and then files an issue."""
        import subprocess

        root = self._repo()
        self._commit(root, "base\n", "green")
        self._commit(root, "base\nalpha\n", "A: add alpha")
        a = self.git("rev-parse", "HEAD").stdout.strip()
        self._commit(root, "base\nalphaX\n", "B: edit alpha")
        before = self.git("rev-parse", "HEAD").stdout.strip()

        conflicted = subprocess.run(
            ["git", "revert", "--no-commit", a], cwd=root, capture_output=True
        )
        self.assertNotEqual(conflicted.returncode, 0, "expected a content conflict")
        self.git("revert", "--abort")
        self.assertEqual(self.git("rev-parse", "HEAD").stdout.strip(), before)
        self.assertEqual(self.git("status", "--porcelain").stdout.strip(), "")


if __name__ == "__main__":
    unittest.main(verbosity=2)
