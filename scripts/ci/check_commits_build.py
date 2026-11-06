#!/usr/bin/env python3
# file: scripts/ci/check_commits_build.py
# version: 1.0.0
# guid: 7d26abe4-c5f0-4c24-b79b-29c26b8ed14f
# last-edited: 2026-11-06
"""Every commit of a move PR builds, and every move is detectable (09 §9.2.1; MOD9-5).

The repository merges by rebase, so each commit of a PR lands on ``main`` and is
seen by ``git bisect`` and ``auto-revert.yml``. A move PR that renames a file into
``core/`` in a commit that cannot compile would break both. For a PR whose diff
renames a file into ``core/`` this script, for each commit of ``<base>..<head>``:

  1. checks the commit out in a temporary detached worktree and runs
     ``go build ./...`` then ``go -C core build ./...`` (the root's ``./...`` never
     reaches the nested ``core/`` module), and reports every commit that fails;
  2. reports every moved file whose first-commit rename similarity is below 90 %
     (12 §12.8.1's threshold). Core's history split maps an origin path only where
     git detects a rename, so a move commit that also rewrites the file too much
     (squashing ``go fix`` into it) silently starts the file's history in ``core/``.
     A file git cannot see renamed in its arrival commit at all (deleted in one
     commit, added under ``core/`` in the next) counts as 0 % and is reported.

Moved files are the destinations of renames into ``core/`` that git detects in a
commit (``git diff-tree -M``, its default 50 % floor) plus the destinations of
``git diff --name-status -M90% --diff-filter=R <base>...<head>`` renames into
``core/``. With none of either the script prints "no rename into core/: nothing to
check" and exits 0 without building anything.

Exit codes: 0 nothing to check, or every commit builds and every move is
detectable; 1 a commit fails to build or a move is below the threshold; 2 the
script could not run (bad ref, git failure).

The loop is sequential on purpose: it walks the commits of one PR (a handful),
and every build shares the one Go build cache, so a worker pool would add
contention and no speed. Stdlib only.
"""

from __future__ import annotations

import argparse
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass

THRESHOLD = 90
CORE_PREFIX = "core/"
# Lines of build output kept per failing build; the full log is in the CI step.
TAIL_LINES = 25


class GitError(RuntimeError):
    pass


@dataclass(frozen=True)
class Arrival:
    """How a path first appeared under core/ in the PR's commits."""

    commit: str
    status: str  # "R" (rename detected) or "A" (added, no rename detected)
    score: int  # similarity percentage; 0 for "A"
    source: str  # pre-move path for "R", else ""


def git(args: list[str], cwd: pathlib.Path, check: bool = True) -> str:
    res = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, check=False)
    if check and res.returncode != 0:
        raise GitError(f"git {' '.join(args)}: {res.stderr.strip()}")
    return res.stdout


def resolve_base(base: str | None, repo: pathlib.Path) -> str:
    candidates = [base] if base else ["origin/main", "main"]
    for ref in candidates:
        res = subprocess.run(
            ["git", "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}"],
            cwd=repo,
            capture_output=True,
            text=True,
            check=False,
        )
        if res.returncode == 0:
            return res.stdout.strip()
    raise GitError(f"cannot resolve base ref {', '.join(map(repr, candidates))}")


def pr_commits(base: str, head: str, repo: pathlib.Path) -> list[str]:
    """Oldest first. Merge commits are skipped: a rebase merge drops them."""
    out = git(["rev-list", "--reverse", "--no-merges", f"{base}..{head}"], repo)
    return out.split()


def parse_status_line(line: str) -> tuple[str, int, list[str]] | None:
    """``R095\\told\\tnew`` -> ("R", 95, [old, new]); ``A\\tp`` -> ("A", 0, [p])."""
    parts = line.split("\t")
    if len(parts) < 2 or not parts[0]:
        return None
    status = parts[0][0]
    score = int(parts[0][1:]) if parts[0][1:].isdigit() else 0
    return status, score, parts[1:]


def commit_arrivals(commit: str, repo: pathlib.Path) -> dict[str, Arrival]:
    """Paths under core/ that ``commit`` adds, with how git sees each arrival."""
    out = git(
        ["diff-tree", "--root", "-r", "-M", "--name-status", "--no-commit-id", commit],
        repo,
    )
    found: dict[str, Arrival] = {}
    for line in out.splitlines():
        parsed = parse_status_line(line)
        if parsed is None:
            continue
        status, score, paths = parsed
        if status == "R" and len(paths) == 2 and paths[1].startswith(CORE_PREFIX):
            found[paths[1]] = Arrival(commit, "R", score, paths[0])
        elif status == "A" and paths[0].startswith(CORE_PREFIX):
            found[paths[0]] = Arrival(commit, "A", 0, "")
    return found


def net_renames_into_core(base: str, head: str, repo: pathlib.Path) -> dict[str, str]:
    """{destination: source} of renames into core/ in the PR's whole diff."""
    out = git(
        ["diff", "--name-status", "-M90%", "--diff-filter=R", f"{base}...{head}"],
        repo,
    )
    moved: dict[str, str] = {}
    for line in out.splitlines():
        parsed = parse_status_line(line)
        if parsed and len(parsed[2]) == 2 and parsed[2][1].startswith(CORE_PREFIX):
            moved[parsed[2][1]] = parsed[2][0]
    return moved


def find_moves(
    base: str, head: str, commits: list[str], repo: pathlib.Path
) -> tuple[dict[str, Arrival], dict[str, str]]:
    """First arrival of every path under core/, and the net renames into core/."""
    first: dict[str, Arrival] = {}
    for commit in commits:
        for path, arrival in commit_arrivals(commit, repo).items():
            first.setdefault(path, arrival)
    return first, net_renames_into_core(base, head, repo)


def similarity_problems(first: dict[str, Arrival], net: dict[str, str]) -> list[str]:
    problems = []
    moved = {p for p, a in first.items() if a.status == "R"} | set(net)
    for path in sorted(moved):
        arrival = first.get(path)
        if arrival is None:
            continue  # the net rename came from a commit outside the walked range
        short = arrival.commit[:12]
        if arrival.status == "R" and arrival.score >= THRESHOLD:
            continue
        if arrival.status == "R":
            problems.append(
                f"FAIL moved file {path} (from {arrival.source}): first-commit similarity "
                f"{arrival.score}% is below {THRESHOLD}% in {short}; keep the move commit a "
                f"pure rename plus the edits it needs to build, and put go fix or any other "
                f"rewrite in a later commit"
            )
        else:
            problems.append(
                f"FAIL moved file {path} (from {net.get(path, 'an earlier path')}): git sees no "
                f"rename in its first commit {short} (0% similarity), so its history would "
                f"start in core/; move it with git mv in one commit"
            )
    return problems


def build_env() -> dict[str, str]:
    env = dict(os.environ)
    env["GOWORK"] = "off"  # never a go.work (CLAUDE.md); the replace directive joins the modules
    return env


def run_build(cmd: list[str], cwd: pathlib.Path) -> tuple[bool, str]:
    res = subprocess.run(cmd, cwd=cwd, env=build_env(), capture_output=True, text=True, check=False)
    if res.returncode == 0:
        return True, ""
    tail = (res.stdout + res.stderr).strip().splitlines()[-TAIL_LINES:]
    return False, "\n".join(f"    {ln}" for ln in tail)


def build_commit(commit: str, repo: pathlib.Path, scratch: pathlib.Path) -> list[str]:
    """Build both modules at ``commit``; returns one message per failing module."""
    wt = scratch / f"wt-{commit[:12]}"
    git(["worktree", "add", "--detach", "--quiet", str(wt), commit], repo)
    try:
        failures = []
        subject = git(["log", "-1", "--format=%s", commit], repo).strip()
        ok, log = run_build(["go", "build", "./..."], wt)
        if not ok:
            failures.append(f"FAIL commit {commit[:12]} ({subject}): root go build ./...\n{log}")
        if (wt / "core" / "go.mod").is_file():
            ok, log = run_build(["go", "-C", "core", "build", "./..."], wt)
            if not ok:
                failures.append(
                    f"FAIL commit {commit[:12]} ({subject}): core go -C core build ./...\n{log}"
                )
        return failures
    finally:
        git(["worktree", "remove", "--force", str(wt)], repo, check=False)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--base", help="base ref of the PR (default: origin/main, then main)")
    ap.add_argument("--head", default="HEAD", help="head ref of the PR (default: HEAD)")
    args = ap.parse_args(argv)
    repo = pathlib.Path.cwd()

    try:
        base = resolve_base(args.base, repo)
        head = git(["rev-parse", "--verify", f"{args.head}^{{commit}}"], repo).strip()
        commits = pr_commits(base, head, repo)
        first, net = find_moves(base, head, commits, repo)
    except GitError as err:
        print(f"check_commits_build: {err}", file=sys.stderr)
        return 2

    if not net and not any(a.status == "R" for a in first.values()):
        print("no rename into core/: nothing to check")
        return 0

    problems = similarity_problems(first, net)
    scratch = pathlib.Path(tempfile.mkdtemp(prefix="check-commits-build-"))
    try:
        for commit in commits:
            print(f"building {commit[:12]} ...", flush=True)
            try:
                problems.extend(build_commit(commit, repo, scratch))
            except GitError as err:
                print(f"check_commits_build: {err}", file=sys.stderr)
                return 2
    finally:
        git(["worktree", "prune"], repo, check=False)
        shutil.rmtree(scratch, ignore_errors=True)

    if problems:
        print()
        print("\n".join(problems))
        print(f"\n{len(problems)} problem(s) in {len(commits)} commit(s)")
        return 1
    print(f"ok: {len(commits)} commit(s) build in both modules; every move is a detected rename")
    return 0


if __name__ == "__main__":
    sys.exit(main())
