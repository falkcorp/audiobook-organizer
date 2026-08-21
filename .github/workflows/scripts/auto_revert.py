#!/usr/bin/env python3
# file: .github/workflows/scripts/auto_revert.py
# version: 1.2.0
# guid: 5c1d8a34-9e02-4b77-8f61-3a4c7d90b2e5
# last-edited: 2026-08-21

"""Pick which commits to revert when the CI gate goes red on the default branch.

This repository deliberately does **not** gate merges on CI, so `main` is verified
*after* the fact. When the gate run fails, this module decides what to restore
`main` to, and refuses to decide whenever the evidence is thin.

Two properties of this repository drive the rules:

* ``ci.yml`` sets ``cancel-in-progress: true``, so back-to-back merges cancel the
  earlier run. Measured on 2026-08-20, the last 100 pushes to ``main`` were 51
  success / 48 cancelled / 0 failure — roughly half of all commits carry a
  ``cancelled`` conclusion, meaning *never verified*, neither green nor red.
* Some commits have no run at all. Skip markers are one cause (the TODO and
  changelog collectors use them), but not the only one: GitHub starts workflows
  only for the *tip* of a push, so every interior commit of a rebase-merged PR
  also lands with zero runs. Treat "no run" as unverified without inferring why.

Both are skipped when looking for the last-green anchor and both stay inside the
reverted span: an unverified commit is a suspect, not an alibi. That is why the
span is "back to last green" rather than "the newest commit" — when the gate
finally fails, any of the unverified commits behind it could be responsible.

Everything here is a pure function over already-fetched data so it can be tested
without touching the network. :func:`main` is the only part that talks to GitHub
Actions, and only through ``GITHUB_OUTPUT``.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from dataclasses import dataclass, field
from pathlib import Path

#: Trailer stamped on every commit this automation creates. Seeing one inside a
#: candidate span means a previous auto-revert did not fix the build, so the
#: automation stops rather than reverting its own revert.
AUTO_REVERT_TRAILER = "Auto-Revert-Of:"

#: Field and record separators for the ``git log`` format below. ASCII unit and
#: record separators, because a commit subject can contain anything else.
_FIELD = "\x1f"
_RECORD = "\x1e"

#: The exact ``git log --format`` string the workflow uses to gather history.
#:
#: This lives here, next to :func:`parse_log` and :func:`render_commit_message`,
#: because those three form one contract: the commit message this module writes
#: must still parse back into a populated ``trailers`` field, or
#: :attr:`Commit.is_auto_revert` silently returns False and the loop guard stops
#: guarding. ``test_auto_revert.py`` round-trips it through real ``git`` rather
#: than asserting the two halves look compatible.
LOG_FORMAT = f"%H{_FIELD}%s{_FIELD}%(trailers:only=true,unfold=true){_RECORD}"

#: Run conclusions that make a commit a usable "last known good" anchor.
GREEN = frozenset({"success"})

#: Conclusions that prove nothing. A cancelled run was killed by the concurrency
#: group before it could judge anything; an empty string means no run was found
#: at all, which has several possible causes and is never evidence of any one
#: of them (see ``_commit_line``).
UNVERIFIED = frozenset({"cancelled", "skipped", "stale", "neutral", ""})

#: Conclusions that count as the gate having actually failed.
RED = frozenset({"failure", "timed_out", "action_required"})


@dataclass(frozen=True)
class Commit:
    """One commit on the default branch. Every list here is newest-first."""

    sha: str
    subject: str = ""
    trailers: str = ""
    pr: int | None = None

    @property
    def short(self) -> str:
        return self.sha[:8]

    @property
    def is_auto_revert(self) -> bool:
        return AUTO_REVERT_TRAILER in self.trailers


def parse_log(raw: str) -> list[Commit]:
    """Parse ``git log --format=LOG_FORMAT`` output into commits, newest first.

    Missing trailing fields are tolerated: a commit with no trailers at all still
    yields two fields rather than three on some git versions.
    """
    commits: list[Commit] = []
    for record in raw.split(_RECORD):
        record = record.strip("\n")
        if not record:
            continue
        sha, subject, trailers = (record.split(_FIELD) + ["", ""])[:3]
        commits.append(Commit(sha=sha, subject=subject, trailers=trailers))
    return commits


def render_commit_message(
    anchor_sha: str,
    failing_sha: str,
    span_shas: list[str],
    run_url: str = "",
) -> str:
    """Build the revert commit message, stamped with one trailer per commit.

    The ``Auto-Revert-Of:`` block **must** be the final paragraph. Git only reads
    trailers from the last paragraph of a message, so moving this block — or
    appending anything after it — makes :attr:`Commit.is_auto_revert` return False
    for every future run and quietly disables the loop guard. Note that
    ``Failing run:`` above is itself trailer-shaped and is correctly ignored
    precisely because it is not in the last paragraph.
    """
    lines = [
        f"revert: restore main to {anchor_sha[:8]} after CI went red",
        "",
        f"The CI gate failed at {failing_sha[:8]}. Reverting {len(span_shas)} commit(s)",
        "to put main back on its last commit with a green gate run.",
    ]
    if run_url:
        lines += ["", f"Failing run: {run_url}"]
    lines.append("")
    lines += [f"{AUTO_REVERT_TRAILER} {sha}" for sha in span_shas]
    return "\n".join(lines) + "\n"


@dataclass
class Decision:
    """What the automation should do, and why."""

    action: str  # "revert" | "report" | "none"
    reason: str
    failing_sha: str = ""
    anchor_sha: str = ""
    span: list[Commit] = field(default_factory=list)
    label: str = ""

    @property
    def span_shas(self) -> list[str]:
        return [c.sha for c in self.span]


def latest_conclusion_by_sha(runs: list[dict]) -> dict[str, str]:
    """Collapse a run list to one conclusion per head SHA, newest run winning.

    A re-run bumps ``run_attempt`` on the same run rather than creating a new one,
    but a SHA can still carry several runs (a ``workflow_dispatch`` alongside the
    ``push``, for instance). Ordering is by ``run_number`` when present so callers
    need not pre-sort, falling back to input order for ties.
    """
    ordered = sorted(
        enumerate(runs),
        key=lambda pair: (pair[1].get("run_number") or 0, pair[0]),
    )
    latest: dict[str, str] = {}
    for _, run in ordered:
        sha = run.get("headSha") or run.get("head_sha") or ""
        if sha:
            latest[sha] = run.get("conclusion") or ""
    return latest


def select_span(
    commits: list[Commit],
    conclusions: dict[str, str],
    max_span: int = 3,
) -> Decision:
    """Choose the commits to revert, given history newest-first from the failure.

    ``commits[0]`` must be the commit whose gate run failed. The returned span is
    every commit from there back to — but not including — the newest commit with a
    green gate run.

    Returns ``action == "report"`` rather than guessing whenever the span would be
    unsafe: no green anchor in the window, a span wider than ``max_span``, or a
    previous auto-revert sitting inside the span.
    """
    if not commits:
        return Decision(action="none", reason="no commits supplied")

    failing = commits[0]

    anchor_index: int | None = None
    for index, commit in enumerate(commits):
        if index == 0:
            # The failing commit cannot anchor itself even if some earlier attempt
            # of its own run happened to be recorded green.
            continue
        if conclusions.get(commit.sha, "") in GREEN:
            anchor_index = index
            break

    if anchor_index is None:
        return Decision(
            action="report",
            reason=(
                f"no green commit in the {len(commits)} behind {failing.short} — "
                "cannot establish a last-known-good state to restore to"
            ),
            failing_sha=failing.sha,
            label="needs-manual-revert",
        )

    span = commits[:anchor_index]
    anchor = commits[anchor_index]

    already = [c for c in span if c.is_auto_revert]
    if already:
        shas = ", ".join(c.short for c in already)
        return Decision(
            action="report",
            reason=(
                f"span already contains an auto-revert ({shas}) — reverting a revert "
                "would loop, so a human needs to look at this"
            ),
            failing_sha=failing.sha,
            anchor_sha=anchor.sha,
            span=span,
            label="needs-manual-revert",
        )

    if len(span) > max_span:
        return Decision(
            action="report",
            reason=(
                f"span is {len(span)} commits (limit {max_span}) — too wide to revert "
                "unattended; the gate has probably been unverified for a while"
            ),
            failing_sha=failing.sha,
            anchor_sha=anchor.sha,
            span=span,
            label="needs-manual-revert",
        )

    return Decision(
        action="revert",
        reason=f"restoring {anchor.short}, the newest commit with a green gate run",
        failing_sha=failing.sha,
        anchor_sha=anchor.sha,
        span=span,
    )


def _commit_line(commit: Commit, conclusions: dict[str, str]) -> str:
    verdict = conclusions.get(commit.sha, "")
    if verdict in GREEN:
        note = "gate green"
    elif verdict in RED:
        note = "**gate red**"
    elif not verdict:
        # Deliberately does NOT name a cause. A commit can have no run because
        # it carries a skip marker, because it was an interior commit of a
        # pushed range (GitHub only starts workflows for the tip), or because
        # CI simply has not started yet. Measured on 2026-08-21: issue #2652
        # blamed a skip marker on three commits that had none — all three were
        # interior commits of one rebase-merged PR. A bug report read at 3am
        # must not assert a cause it cannot know.
        note = "no gate run — never verified"
    else:
        note = f"gate `{verdict}` — never verified"
    pr = f" (#{commit.pr})" if commit.pr else ""
    return f"- `{commit.short}` {commit.subject}{pr} — {note}"


def render_issue_body(
    decision: Decision,
    conclusions: dict[str, str],
    run_url: str = "",
    failed_jobs: list[str] | None = None,
    revert_sha: str = "",
) -> str:
    """Render the Markdown body of the bug filed for a red default branch."""
    failed_jobs = failed_jobs or []
    lines: list[str] = []

    if decision.action == "revert":
        lines.append(
            f"The CI gate failed on `main` at `{decision.failing_sha[:8]}`. "
            f"{len(decision.span)} commit(s) were reverted automatically to restore "
            f"the branch to `{decision.anchor_sha[:8]}`."
        )
    else:
        lines.append(
            f"The CI gate failed on `main` at `{decision.failing_sha[:8]}`, and the "
            "branch was **not** reverted automatically."
        )
    lines += ["", f"**Why:** {decision.reason}", ""]

    if run_url:
        lines.append(f"**Failing run:** {run_url}")
    if failed_jobs:
        lines += ["", "**Failing jobs:**"] + [f"- `{job}`" for job in failed_jobs]
    if revert_sha:
        lines += ["", f"**Revert commit:** `{revert_sha[:8]}`"]
    lines.append("")

    if decision.span:
        heading = "Reverted commits" if decision.action == "revert" else "Suspect commits"
        lines += [f"### {heading}", ""]
        lines += [_commit_line(c, conclusions) for c in decision.span]
        lines.append("")

    if decision.action == "revert":
        lines.append(
            "To re-land: open a new PR with the original changes plus a fix. The "
            "revert is an ordinary commit — `git revert` it to recover the code."
        )
    else:
        lines.append("Someone needs to decide by hand what to restore `main` to.")

    return "\n".join(lines)


def _load_commits(path: Path) -> list[Commit]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    return [
        Commit(
            sha=item["sha"],
            subject=item.get("subject", ""),
            trailers=item.get("trailers", ""),
            pr=item.get("pr"),
        )
        for item in raw
    ]


#: Heredoc delimiter for multi-line ``GITHUB_OUTPUT`` values.
_DELIM = "__AUTOREVERT_EOF__"


def _write_output(name: str, value: str) -> None:
    """Append one value to ``GITHUB_OUTPUT``, safely for multi-line content.

    Commit subjects reach this function unmodified, and a subject is attacker-
    controlled by anyone who can open a PR. A line equal to the heredoc delimiter
    would end the value early and let the rest be parsed as further outputs, so
    any such line is neutralised rather than trusted.
    """
    target = os.environ.get("GITHUB_OUTPUT")
    if not target:
        return
    if "\n" in value:
        safe = "\n".join(
            f"{line} " if line.strip() == _DELIM else line for line in value.split("\n")
        )
        payload = f"{name}<<{_DELIM}\n{safe}\n{_DELIM}\n"
    else:
        payload = f"{name}={value}\n"
    with Path(target).open("a", encoding="utf-8") as handle:
        handle.write(payload)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Auto-revert helper.")
    sub = parser.add_subparsers(dest="mode", required=True)

    decide = sub.add_parser("decide", help="select an auto-revert span")
    decide.add_argument("--commits", type=Path, required=True, help="JSON, newest first")
    decide.add_argument("--runs", type=Path, required=True, help="JSON gate run list")
    decide.add_argument("--max-span", type=int, default=3)
    decide.add_argument("--run-url", default="")
    decide.add_argument("--failed-jobs", default="", help="newline-separated job names")

    # The revert commit message is generated here rather than in the workflow so
    # that the trailer the loop guard reads is produced by tested code.
    msg = sub.add_parser("commit-message", help="render the revert commit message")
    msg.add_argument("--anchor", required=True)
    msg.add_argument("--failing", required=True)
    msg.add_argument("--span", required=True, help="space-separated SHAs")
    msg.add_argument("--run-url", default="")

    args = parser.parse_args(argv)

    if args.mode == "commit-message":
        sys.stdout.write(
            render_commit_message(
                args.anchor, args.failing, args.span.split(), run_url=args.run_url
            )
        )
        return 0

    commits = _load_commits(args.commits)
    conclusions = latest_conclusion_by_sha(json.loads(args.runs.read_text(encoding="utf-8")))
    decision = select_span(commits, conclusions, max_span=args.max_span)

    body = render_issue_body(
        decision,
        conclusions,
        run_url=args.run_url,
        failed_jobs=[j for j in args.failed_jobs.splitlines() if j.strip()],
    )

    _write_output("action", decision.action)
    _write_output("reason", decision.reason)
    _write_output("anchor", decision.anchor_sha)
    _write_output("failing", decision.failing_sha)
    _write_output("span", " ".join(decision.span_shas))
    _write_output("span_count", str(len(decision.span)))
    _write_output("label", decision.label)
    _write_output("issue_body", body)

    print(f"action={decision.action}")
    print(f"reason={decision.reason}")
    print(f"anchor={decision.anchor_sha or '(none)'}")
    print(f"span={' '.join(c.short for c in decision.span) or '(empty)'}")
    print()
    print(body)
    return 0


if __name__ == "__main__":
    sys.exit(main())
