#!/usr/bin/env python3
# file: scripts/ci/check_commits_build.py
# version: 1.2.0
# guid: 7d26abe4-c5f0-4c24-b79b-29c26b8ed14f
# last-edited: 2026-11-08
"""Every commit of a move PR builds, and every move is detectable (09 §9.2.1; MOD9-5).

The repository merges by rebase, so each commit of a PR lands on ``main`` and is
seen by ``git bisect`` and ``auto-revert.yml``. A move PR that renames a file into
``core/`` in a commit that cannot compile would break both. For a PR whose diff
renames a file into ``core/`` this script, for each commit of ``<base>..<head>``:

  1. checks the commit out in one temporary detached worktree (reused for every commit) and runs
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

Within one run the loop is sequential on purpose: every build shares the one Go
build cache and ``go build`` already uses every core, so a worker pool would add
contention and no speed. A long PR is split across machines instead:
``--shard K/N`` builds only the K-th of N contiguous slices of the commit list
(contiguous, so each commit is an incremental build on top of the one before it)
and reports the similarity check only in shard 1. #4453 (48 commits) ran past the
job's 30-minute cap, which 09 §9.2.10 fixes for every extraction-gates job, so
CI runs this as a matrix of shards rather than raising the timeout. Stdlib only.
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


def build_commit(commit: str, repo: pathlib.Path, wt: pathlib.Path) -> list[str]:
    """Build both modules at ``commit`` in worktree ``wt``; one message per failing module.

    Every commit is built in the same worktree path (checked out in turn), not a
    fresh ``wt-<sha>`` directory per commit: Go's build cache keys include the
    package directory, so a new path per commit recompiled everything and a
    33-commit PR (#4453) ran past the job's 30-minute timeout at ~1m48s a commit.
    """
    git(["checkout", "--quiet", "--force", "--detach", commit], wt)
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


def parse_shard(text: str) -> tuple[int, int]:
    """``"2/4"`` -> (2, 4); raises ``argparse.ArgumentTypeError`` unless 1 <= K <= N."""
    try:
        k, n = (int(x) for x in text.split("/", 1))
    except ValueError:
        raise argparse.ArgumentTypeError(f"--shard wants K/N, got {text!r}") from None
    if n < 1 or not 1 <= k <= n:
        raise argparse.ArgumentTypeError(f"--shard {text}: need 1 <= K <= N")
    return k, n


def shard_slice(commits: list[str], k: int, n: int) -> list[str]:
    """The K-th of N contiguous, near-equal slices of ``commits`` (oldest first)."""
    size, extra = divmod(len(commits), n)
    start = (k - 1) * size + min(k - 1, extra)
    return commits[start : start + size + (1 if k <= extra else 0)]


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n")[0])
    ap.add_argument("--base", help="base ref of the PR (default: origin/main, then main)")
    ap.add_argument("--head", default="HEAD", help="head ref of the PR (default: HEAD)")
    ap.add_argument(
        "--shard",
        type=parse_shard,
        default=(1, 1),
        metavar="K/N",
        help="build only the K-th of N contiguous slices of the commits (default 1/1)",
    )
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

    k, n = args.shard
    problems = similarity_problems(first, net) if k == 1 else []
    mine = shard_slice(commits, k, n)
    label = f" (shard {k}/{n}: {len(mine)} of {len(commits)})" if n > 1 else ""
    if not mine:
        print(f"shard {k}/{n}: no commits to build")
        if problems:
            print("\n".join(problems))
            return 1
        return 0
    scratch = pathlib.Path(tempfile.mkdtemp(prefix="check-commits-build-"))
    wt = scratch / "wt"
    try:
        try:
            git(["worktree", "add", "--detach", "--quiet", str(wt), mine[0]], repo)
            for commit in mine:
                print(f"building {commit[:12]} ...", flush=True)
                problems.extend(build_commit(commit, repo, wt))
        except GitError as err:
            print(f"check_commits_build: {err}", file=sys.stderr)
            return 2
    finally:
        git(["worktree", "remove", "--force", str(wt)], repo, check=False)
        git(["worktree", "prune"], repo, check=False)
        shutil.rmtree(scratch, ignore_errors=True)

    if problems:
        print()
        print("\n".join(problems))
        print(f"\n{len(problems)} problem(s) in {len(mine)} commit(s){label}")
        return 1
    moves = "every move is a detected rename" if k == 1 else "moves are checked by shard 1"
    print(f"ok: {len(mine)} commit(s) build in both modules{label}; {moves}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
