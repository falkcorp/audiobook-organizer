#!/usr/bin/env python3
# file: core/scripts/ci/core_api_gate.py
# version: 1.0.0
# guid: eeb68bd0-5560-4049-abeb-c352785ec7cc
# last-edited: 2026-11-08
"""SC1: core's public API is stable across 3 consecutive checkpoints (12 §12.3 SC1; make -C core apidiff-gate).

A readiness measurement, never a merge gate. The SDK is every core package
outside kernel/ and examples/ (03 §3.1; internal/ is filtered by apidiff
itself). Checkpoints are refs/core-checkpoints/v0.Y.Z, not tags (12 §12.4.1),
fetched explicitly. For the four newest refs this script writes apidiff's
module export data once per ref (a git worktree per ref, in a temp dir), then
compares the three adjacent pairs with ``apidiff -m -incompatible``, drops the
lines for kernel/ and examples/, and requires:

  * four checkpoint refs exist (three pairs);
  * every ref is at or above --first-counting (the checkpoint of release R2,
    the first that counts, 12 SC1; earlier checkpoints are reported but start
    no count; unset means nothing counts yet);
  * every pair has no incompatible SDK change;
  * the newest and oldest of the four are at least --min-days apart (6 weeks).

The filter is covered by test_core_api_gate.py, whose fixtures are real apidiff
output (a kernel/ break and an examples/ break are dropped, an SDK break is
reported), so a change in x/exp's output format fails loudly instead of silently
passing or failing every run.

The post-split form (gorelease -base against the previous tag, 12 §12.3 SC1)
is not here: no tag exists before the split.

Stdlib only; apidiff comes from core/tools/apidiff (``make -C core tools``).
"""

from __future__ import annotations

import argparse
import datetime
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Callable, Sequence

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402

CORE_DIR = pathlib.Path(__file__).resolve().parents[2]
# 12 SC1: three pairs need four refs.
PAIRS = 3
MIN_DAYS = 42

# Lines of `apidiff -m -incompatible` that belong to kernel/ or examples/ (12 SC1,
# "Output format the filter relies on"): `- ./<rel>.<Obj>: <msg>` for a changed
# object and `- package <full path>: removed` for a removed package.
_OUT_OF_SDK_RE = re.compile(
    r"^- (\./(kernel|examples)(/[^.: ]+)*\.|package github\.com/falkcorp/mediacore/(kernel|examples)(/|:))"
)

PairDiff = Callable[[str, str], list[str]]


def filter_incompatible(apidiff_output: str) -> list[str]:
    """The incompatible lines that are not about kernel/ or examples/."""
    return [
        line
        for line in apidiff_output.splitlines()
        if line.strip() and not _OUT_OF_SDK_RE.match(line)
    ]


def days_between(older: str, newer: str) -> int:
    return (datetime.date.fromisoformat(newer) - datetime.date.fromisoformat(older)).days


def evaluate(
    refs: Sequence[tuple[str, lib.Semver]],
    first_counting: lib.Semver | None,
    dates: dict[str, str],
    pair_diff: PairDiff,
    min_days: int = MIN_DAYS,
) -> lib.Result:
    """SC1's verdict over the checkpoint refs (newest first); pair_diff(old, new) returns the SDK breaks."""
    title = "Public API stable across 3 consecutive core checkpoints"
    details: list[str] = []
    window = list(refs[: PAIRS + 1])
    if len(window) < PAIRS + 1:
        return lib.Result(
            "SC1",
            title,
            lib.FAIL,
            f"{len(refs)} checkpoint(s) recorded, {PAIRS + 1} needed for {PAIRS} pairs",
            [f"recorded: {', '.join(str(v) for _, v in refs) or 'none'}"],
        )
    oldest_to_newest = list(reversed(window))
    stable = 0
    counting = 0
    unmet: list[str] = []
    for (old_ref, old_v), (new_ref, new_v) in zip(oldest_to_newest, oldest_to_newest[1:]):
        breaks = pair_diff(old_ref, new_ref)
        counts = first_counting is not None and old_v >= first_counting
        details.append(
            f"v{old_v} -> v{new_v}: "
            + (
                f"{len(breaks)} incompatible SDK change(s)"
                if breaks
                else "no incompatible SDK change"
            )
            + ("" if counts else " (before the counting start: reported only)")
        )
        details += [f"    {line}" for line in breaks]
        counting += counts
        if breaks:
            unmet.append(f"v{old_v} -> v{new_v} has incompatible changes")
        elif counts:
            stable += 1
    if first_counting is None:
        unmet.append(
            "no counting-start checkpoint set (SC1_FIRST_CHECKPOINT, the release R2 checkpoint): nothing counts yet"
        )
    elif counting < PAIRS:
        unmet.append(
            f"only {counting} of {PAIRS} pairs are at or after the counting start v{first_counting}"
        )
    span = days_between(dates[oldest_to_newest[0][0]], dates[oldest_to_newest[-1][0]])
    details.append(
        f"span: {span} day(s) from v{oldest_to_newest[0][1]} to v{oldest_to_newest[-1][1]} (needs {min_days})"
    )
    if span < min_days:
        unmet.append(f"span {span} day(s) < {min_days}")
    status = lib.PASS if not unmet else lib.FAIL
    measured = f"{stable}/{PAIRS} stable pairs at or after the counting start, span {span} day(s)"
    return lib.Result("SC1", title, status, measured, details + [f"not met: {u}" for u in unmet])


class ApidiffRunner:
    """Writes apidiff export data per ref (in git worktrees under a temp dir) and diffs pairs."""

    def __init__(self, repo: pathlib.Path, prefix: str, apidiff: pathlib.Path, tmp: pathlib.Path):
        # prefix is core's path inside the repository ("core/" here, "" in an export of core/ alone).
        self.repo, self.prefix, self.apidiff, self.tmp = repo, prefix, apidiff, tmp
        self.exports: dict[str, pathlib.Path] = {}
        self.env = lib.go_env()

    def export(self, ref: str) -> pathlib.Path:
        if ref in self.exports:
            return self.exports[ref]
        slug = re.sub(r"[^A-Za-z0-9.]+", "_", ref)
        tree = self.tmp / f"wt-{slug}"
        lib.git(self.repo, "worktree", "add", "--detach", str(tree), ref)
        try:
            out = self.tmp / f"{slug}.api"
            core = tree / self.prefix
            res = subprocess.run(
                [str(self.apidiff), "-m", "-w", str(out), lib.module_path(core)],
                cwd=core,
                env=self.env,
                capture_output=True,
                text=True,
                check=False,
            )
            if res.returncode != 0:
                raise RuntimeError(f"apidiff -w at {ref}: {(res.stderr or res.stdout).strip()}")
        finally:
            lib.git(self.repo, "worktree", "remove", "--force", str(tree), check=False)
        self.exports[ref] = out
        return out

    def pair(self, old_ref: str, new_ref: str) -> list[str]:
        old, new = self.export(old_ref), self.export(new_ref)
        res = subprocess.run(
            [str(self.apidiff), "-m", "-incompatible", str(old), str(new)],
            cwd=self.tmp,
            env=self.env,
            capture_output=True,
            text=True,
            check=False,
        )
        if res.returncode != 0:
            raise RuntimeError(
                f"apidiff {old_ref}..{new_ref}: {(res.stderr or res.stdout).strip()}"
            )
        return filter_incompatible(res.stdout)


def measure(
    core: pathlib.Path,
    apidiff: pathlib.Path,
    first_counting: lib.Semver | None,
    fetch: bool,
    remote: str,
    min_days: int = MIN_DAYS,
) -> lib.Result:
    title = "Public API stable across 3 consecutive core checkpoints"
    try:
        repo, prefix = lib.repo_root(core)
        if fetch and lib.has_remote(repo, remote):
            lib.fetch_checkpoints(repo, remote)
        refs = lib.list_checkpoints(repo)
    except (RuntimeError, ValueError, subprocess.CalledProcessError) as e:
        return lib.Result("SC1", title, lib.ERROR, f"checkpoints unreadable: {e}")
    if len(refs) < PAIRS + 1:
        return evaluate(refs, first_counting, {}, lambda a, b: [], min_days)
    if not apidiff.is_file():
        return lib.Result(
            "SC1", title, lib.ERROR, f"{apidiff} is missing (make -C core tools builds it)"
        )
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="core-api-gate-"))
    try:
        runner = ApidiffRunner(repo, prefix, apidiff, tmp)
        window = refs[: PAIRS + 1]
        dates = {name: lib.commit_date(repo, name) for name, _ in window}
        return evaluate(refs, first_counting, dates, runner.pair, min_days)
    except (RuntimeError, ValueError) as e:
        return lib.Result("SC1", title, lib.ERROR, str(e))
    finally:
        lib.git(repo, "worktree", "prune", check=False)
        shutil.rmtree(tmp, ignore_errors=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--core",
        type=pathlib.Path,
        default=CORE_DIR,
        help="core module root (default: this script's module)",
    )
    parser.add_argument(
        "--apidiff",
        type=pathlib.Path,
        default=CORE_DIR / "bin" / "apidiff",
        help="the pinned apidiff binary",
    )
    parser.add_argument(
        "--first-counting",
        default="",
        metavar="vX.Y.Z",
        help="the first checkpoint that counts (release R2's); empty: none counts yet",
    )
    parser.add_argument("--min-days", type=int, default=MIN_DAYS)
    parser.add_argument("--remote", default="origin")
    parser.add_argument("--no-fetch", action="store_true")
    parser.add_argument(
        "--out", type=pathlib.Path, help="write sc1.json here (make -C core readiness)"
    )
    parser.add_argument(
        "--report", action="store_true", help="report only: exit 0 on FAIL or ERROR"
    )
    args = parser.parse_args(argv)
    try:
        first = lib.parse_semver(args.first_counting) if args.first_counting else None
    except ValueError as e:
        parser.error(f"--first-counting: {e}")
    result = measure(
        args.core.resolve(),
        args.apidiff.resolve(),
        first,
        not args.no_fetch,
        args.remote,
        args.min_days,
    )
    return lib.finish(result, args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
