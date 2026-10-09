#!/usr/bin/env python3
# file: scripts/ci/short_test_shards.py
# version: 1.0.0
# guid: 6c2f9a47-3e1b-4d88-9f05-b7a1d4e3c862
# last-edited: 2026-10-09
"""Run the whole short Go test suite as one of N shards, then merge coverage.

`make test-short` runs `go test ./... -short -race` as one process tree, so
the PR gate takes as long as its slowest package plus everything queued behind
it (19-23 minutes on a GitHub runner). This script splits the same suite into
N shards that run on separate runners (ci.yml's `go-test-short-shards`
matrix):

  * Units. Every package `go list ./...` reports is a unit. A package whose
    weight exceeds _SPLIT_CAP seconds is split into ceil(weight / _SPLIT_CAP)
    groups of top-level test names with go_test_shards.balance(), balanced by
    the per-test seconds in go_test_timings.json; each group is its own unit.
  * Assignment. Longest-processing-time-first over all units into N bins,
    deterministic (sorted by (-weight, name), ties to the lowest bin), the
    same rule fixture_test_packages.shard uses.
  * Run. A shard runs all its whole packages in one `go test` invocation and
    each split group as `go test -run '^(A|B|...)$' pkg`, all at once by
    default (see run_shard for why), each with its own -coverprofile;
    go_test_shards.merge_profiles then folds them into --out.
  * Completeness. Before anything runs, every shard checks that each package
    is assigned exactly once (whole, or as split groups and never both) and
    that the split groups of every split package cover its `go test -list`
    names exactly once. `--check` extends the name-level check to every test
    package. A compile error in a listed package fails the shard
    (go_test_shards.list_tests exits non-zero), and a split group whose
    pattern matched nothing fails it too.

Weights (short_test_weights.json, package -> seconds; JSON carries no header,
so its provenance is recorded here). The values for internal/server (414 s),
internal/metafetch (240 s) and internal/database (157 s) are the Mac `-short`
numbers from appendix A.7 of
docs/proposals/2026-10-holistic/07-design-decisions-and-modularity.md.
internal/plugins/maintenance is 343 s, its package elapsed time under
`go test -short -race -json` on the Mac on 2026-10-09 (the measurement that
produced its go_test_timings.json key; A.7's 578 s predates #3781, which made
its fragment-fixer tests parallel). internal/scanner (A.7 timed out without a
number) and internal/server/handlers/abs (not in A.7) use the sum of their
go_test_timings.json entries, 292 s and 239 s. A package with tests that is in
neither file weighs _DEFAULT_WEIGHT (as in fixture_test_packages.py); a package
without test files weighs _NO_TESTS_WEIGHT (it still runs, because `go test`
reports its statements as uncovered and the coverage total depends on that).
A stale weight costs balance, never coverage.

Usage:
  short_test_shards.py --shard K/N --out FILE [--jobs J] [--weights FILE]
      [--timings FILE] -- -short -race -covermode=atomic -timeout 25m
  short_test_shards.py --shard K/N --plan -- -short -race
  short_test_shards.py [--shard K/N] --check -- -short -race
  short_test_shards.py --merge OUT IN [IN ...]

Every argument after `--` goes to each `go test` call; the script adds only
-coverprofile and, for split groups, -run.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import math
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from collections import Counter
from dataclasses import dataclass
from typing import Callable

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import go_test_shards as gts  # noqa: E402

_HERE = os.path.dirname(os.path.abspath(__file__))
_SPLIT_CAP = 150.0
_DEFAULT_WEIGHT = 10.0
_NO_TESTS_WEIGHT = 1.0
_DEFAULT_SHARDS = 4
# Linux caps a single argv string at 131072 bytes (MAX_ARG_STRLEN); macOS has
# no such cap, so a local run cannot catch an oversized -run pattern. The
# largest today is ~76 KB (all of internal/plugins/maintenance in one group).
_MAX_PATTERN = 120_000

Lister = Callable[[str, list[str]], list[str]]


@dataclass(frozen=True)
class Package:
    path: str  # import path, as passed to go test
    key: str  # module-relative key used by the weights and timings files
    has_tests: bool


@dataclass(frozen=True)
class Unit:
    pkg: Package
    group: int  # 0: the whole package; 1..k: split group
    names: tuple[str, ...]  # empty for a whole package
    weight: float

    @property
    def label(self) -> str:
        return self.pkg.key if self.group == 0 else f"{self.pkg.key}#{self.group}"


def load_packages() -> list[Package]:
    """Every package `go test ./...` would run, in `go list` order."""
    fmt = "{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}"
    mod = subprocess.run(["go", "list", "-m"], capture_output=True, text=True)
    res = subprocess.run(["go", "list", "-f", fmt, "./..."], capture_output=True, text=True)
    for r in (mod, res):
        if r.returncode != 0:
            sys.stderr.write(r.stdout + r.stderr)
            raise SystemExit(f"::error::go list failed (exit {r.returncode})")
    module = mod.stdout.strip()
    pkgs = []
    for line in res.stdout.splitlines():
        path, tests, xtests = line.split()
        key = "." if path == module else path.removeprefix(module + "/")
        pkgs.append(Package(path, key, int(tests) + int(xtests) > 0))
    if not pkgs:
        raise SystemExit("::error::go list ./... returned no packages")
    return pkgs


def package_weight(
    pkg: Package, weights: dict[str, float], timings: dict[str, dict[str, float]]
) -> float:
    if not pkg.has_tests:
        return _NO_TESTS_WEIGHT
    if pkg.key in weights:
        return float(weights[pkg.key])
    if pkg.key in timings:
        return float(sum(timings[pkg.key].values()))
    return _DEFAULT_WEIGHT


def _estimate(names: list[str], timings: dict[str, float]) -> dict[str, float]:
    # The same per-name estimate balance() uses: unknown names get the median.
    known = sorted(timings[n] for n in names if n in timings)
    default = known[len(known) // 2] if known else 1.0
    return {n: timings.get(n, default) for n in names}


def _list_all(
    pkgs: list[Package], go_args: list[str], lister: Lister, jobs: int
) -> dict[str, list[str]]:
    if not pkgs:
        return {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, jobs)) as ex:
        futs = {p.path: ex.submit(lister, p.path, go_args) for p in pkgs}
        return {path: f.result() for path, f in futs.items()}


def build_units(
    pkgs: list[Package],
    weights: dict[str, float],
    timings: dict[str, dict[str, float]],
    go_args: list[str],
    lister: Lister,
    jobs: int = 4,
) -> tuple[list[Unit], dict[str, list[str]]]:
    """Return the units and the `go test -list` names of every split package."""
    split = {}
    for p in pkgs:
        w = package_weight(p, weights, timings)
        if p.has_tests and w > _SPLIT_CAP:
            split[p.path] = math.ceil(w / _SPLIT_CAP)
    listed = _list_all([p for p in pkgs if p.path in split], go_args, lister, jobs)

    units: list[Unit] = []
    for p in pkgs:
        w = package_weight(p, weights, timings)
        names = listed.get(p.path)
        if not names:
            # Not split, or split but it lists no tests (only benchmarks, say):
            # running it whole is always complete.
            units.append(Unit(p, 0, (), w))
            continue
        pkg_timings = timings.get(p.key, {})
        est = _estimate(names, pkg_timings)
        total = sum(est.values()) or 1.0
        for i, group in enumerate(gts.balance(names, split[p.path], pkg_timings), start=1):
            units.append(Unit(p, i, tuple(group), w * sum(est[n] for n in group) / total))
    return units, listed


def assign(units: list[Unit], count: int) -> list[list[Unit]]:
    """Longest-processing-time-first into `count` bins; deterministic."""
    bins: list[list[Unit]] = [[] for _ in range(count)]
    loads = [0.0] * count
    for u in sorted(units, key=lambda u: (-u.weight, u.label)):
        b = min(range(count), key=lambda k: (loads[k], k))
        bins[b].append(u)
        loads[b] += u.weight
    return bins


def verify(bins: list[list[Unit]], pkgs: list[Package], listed: dict[str, list[str]]) -> list[str]:
    """Problems with the plan: empty shards, packages or split names not assigned exactly once."""
    errors = [f"shard {i}/{len(bins)} has no units" for i, b in enumerate(bins, start=1) if not b]
    units = [u for b in bins for u in b]
    whole = Counter(u.pkg.path for u in units if u.group == 0)
    split_names: dict[str, list[str]] = {}
    for u in units:
        if u.group:
            split_names.setdefault(u.pkg.path, []).extend(u.names)
    for p in pkgs:
        n_whole = whole.pop(p.path, 0)
        names = split_names.pop(p.path, None)
        if n_whole > 1:
            errors.append(f"{p.key}: assigned whole {n_whole} times")
        if n_whole and names is not None:
            errors.append(f"{p.key}: assigned whole and as split groups")
        if not n_whole and names is None:
            errors.append(f"{p.key}: not assigned to any shard")
        if names is not None:
            want = set(listed.get(p.path, []))
            dup = sorted(n for n, c in Counter(names).items() if c > 1)
            missing = sorted(want - set(names))
            extra = sorted(set(names) - want)
            if dup:
                errors.append(
                    f"{p.key}: {len(dup)} tests in more than one group: {', '.join(dup[:5])}"
                )
            if missing:
                errors.append(
                    f"{p.key}: {len(missing)} tests in no group: {', '.join(missing[:5])}"
                )
            if extra:
                errors.append(
                    f"{p.key}: {len(extra)} grouped tests not in go test -list: {', '.join(extra[:5])}"
                )
    for u in units:
        if u.group and len(run_pattern(u.names)) > _MAX_PATTERN:
            errors.append(
                f"{u.label}: -run pattern is {len(run_pattern(u.names))} bytes, over "
                f"{_MAX_PATTERN}; raise its weight so the package splits into more groups"
            )
    for path in sorted(set(whole) | set(split_names)):
        errors.append(f"{path}: assigned but not in go list ./...")
    return errors


def full_check(
    bins: list[list[Unit]],
    pkgs: list[Package],
    listed: dict[str, list[str]],
    go_args: list[str],
    lister: Lister,
    jobs: int,
) -> tuple[int, int, int]:
    """Name-level union over every test package: (total, duplicates, missing)."""
    need = [p for p in pkgs if p.has_tests and p.path not in listed]
    every = dict(listed)
    every.update(_list_all(need, go_args, lister, jobs))
    assigned: Counter[tuple[str, str]] = Counter()
    for b in bins:
        for u in b:
            for n in u.names if u.group else every.get(u.pkg.path, []):
                assigned[(u.pkg.path, n)] += 1
    full = {(path, n) for path, names in every.items() for n in names}
    dup = sum(1 for k, c in assigned.items() if c > 1)
    missing = len(full - set(assigned))
    return len(full), dup, missing


def _load_json(path: str, required: bool) -> dict:
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except FileNotFoundError:
        if required:
            raise SystemExit(f"::error::{path} not found")
        return {}


def _print_plan(bins: list[list[Unit]], index: int) -> None:
    for i, b in enumerate(bins, start=1):
        mark = " <- this shard" if i == index else ""
        print(f"shard {i}/{len(bins)}: est. {sum(u.weight for u in b):.0f}s, {len(b)} units{mark}")
        for u in sorted(b, key=lambda u: (-u.weight, u.label)):
            what = f"{len(u.names)} tests" if u.group else "whole package"
            if u.weight > _DEFAULT_WEIGHT:
                print(f"    {u.weight:7.1f}s  {u.label}  ({what})")
        small = [u for u in b if u.weight <= _DEFAULT_WEIGHT]
        if small:
            print(
                f"    {sum(u.weight for u in small):7.1f}s  {len(small)} whole packages of <= {_DEFAULT_WEIGHT:.0f}s each"
            )


def run_pattern(names: tuple[str, ...]) -> str:
    return "^(" + "|".join(names) + ")$"


def run_shard(units: list[Unit], go_args: list[str], out: str, jobs: int = 0) -> int:
    """Run a shard's go test calls; jobs <= 0 runs every call at once.

    The default matters for the timeout invariant in ci.yml: each call has its
    own -timeout 25m, and a call queued behind another for a free slot could
    start late enough that the job's 35-minute cap kills it before Go's own
    timeout prints the goroutine dump. go_test_shards.py defaults the same way.
    """
    whole = [u.pkg.path for u in units if u.group == 0]
    calls: list[tuple[str, list[str], bool]] = []
    if whole:
        calls.append((f"{len(whole)} whole packages", whole, False))
    for u in sorted((u for u in units if u.group), key=lambda u: -u.weight):
        calls.append((u.label, ["-run", run_pattern(u.names), u.pkg.path], True))
    workers = jobs if jobs > 0 else len(calls)

    tmp = tempfile.mkdtemp(prefix="short-shard-")

    def run(i: int) -> tuple[str, int, float, str, bool, str]:
        label, args, is_split = calls[i]
        prof = os.path.join(tmp, f"{i}.out")
        start = time.monotonic()
        res = subprocess.run(
            ["go", "test", *go_args, f"-coverprofile={prof}", *args], capture_output=True, text=True
        )
        return (
            label,
            res.returncode,
            time.monotonic() - start,
            res.stdout + res.stderr,
            is_split,
            prof,
        )

    failed, profiles = 0, []
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, workers)) as ex:
            for fut in concurrent.futures.as_completed(
                [ex.submit(run, i) for i in range(len(calls))]
            ):
                label, rc, secs, output, is_split, prof = fut.result()
                profiles.append(prof)
                # A split group whose names matched nothing means -list saw a
                # different test set than the run compiled: those tests would
                # silently not run, so that is a failure.
                empty = is_split and "[no tests to run]" in output
                status = "ok" if rc == 0 and not empty else "FAIL"
                print(f"--- {label} {status} in {secs:.0f}s", flush=True)
                if status == "FAIL":
                    failed += 1
                    print(output, flush=True)
                    if empty:
                        print(
                            f"::error::{label} ran no tests; -list and the run disagree", flush=True
                        )
                else:
                    for ln in output.splitlines():
                        if ln.startswith(("ok", "---", "FAIL")):
                            print("    " + ln, flush=True)
        gts.merge_profiles(sorted(profiles), out)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    return 1 if failed else 0


def merge(out: str, inputs: list[str]) -> int:
    # Unlike a shard's own profiles, every input here must exist: a missing
    # shard artifact would silently lower the merged total.
    missing = [p for p in inputs if not os.path.isfile(p)]
    if missing:
        print(f"::error::coverage profiles missing: {', '.join(missing)}", flush=True)
        return 1
    gts.merge_profiles(inputs, out)
    return 0


def _parse_shard(spec: str) -> tuple[int, int]:
    m = re.fullmatch(r"(\d+)/(\d+)", spec or "")
    if not m or not 1 <= int(m.group(1)) <= int(m.group(2)):
        raise SystemExit(f"short_test_shards: bad --shard {spec!r}; want K/N with 1 <= K <= N")
    return int(m.group(1)), int(m.group(2))


def main(
    argv: list[str] | None = None,
    lister: Lister = gts.list_tests,
    loader: Callable[[], list[Package]] = load_packages,
) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--shard", help="K/N, 1-based")
    ap.add_argument("--out", help="merged coverage profile for this shard")
    ap.add_argument(
        "--jobs",
        type=int,
        default=0,
        help="concurrent go test calls in a shard (default 0: all at once); "
        "`go test -list` runs use the CPU count",
    )
    ap.add_argument("--weights", default=os.path.join(_HERE, "short_test_weights.json"))
    ap.add_argument("--timings", default=os.path.join(_HERE, "go_test_timings.json"))
    ap.add_argument(
        "--plan", action="store_true", help="print the units and per-shard estimates; run nothing"
    )
    ap.add_argument(
        "--check",
        action="store_true",
        help="assert every listed test is in exactly one shard; run nothing",
    )
    ap.add_argument(
        "--merge",
        nargs="+",
        metavar=("OUT", "IN"),
        help="merge coverage profiles IN... into OUT and exit",
    )
    ap.add_argument("go_args", nargs=argparse.REMAINDER)
    a = ap.parse_args(argv)
    go_args = a.go_args[1:] if a.go_args[:1] == ["--"] else a.go_args

    if a.merge:
        if len(a.merge) < 2:
            ap.error("--merge needs OUT and at least one IN")
        return merge(a.merge[0], a.merge[1:])

    if a.shard:
        index, count = _parse_shard(a.shard)
    elif a.check:
        index, count = 0, _DEFAULT_SHARDS
    else:
        ap.error("--shard K/N is required (except with --check or --merge)")
    if not (a.plan or a.check or a.out):
        ap.error("--out is required to run a shard")

    weights = _load_json(a.weights, required=True)
    timings = _load_json(a.timings, required=False)
    pkgs = loader()
    list_jobs = os.cpu_count() or 1
    units, listed = build_units(pkgs, weights, timings, go_args, lister, list_jobs)
    bins = assign(units, count)
    errors = verify(bins, pkgs, listed)
    for e in errors:
        print(f"::error::{e}", flush=True)
    if errors:
        return 1

    if a.plan:
        _print_plan(bins, index)
    if a.check:
        total, dup, missing = full_check(bins, pkgs, listed, go_args, lister, list_jobs)
        print(
            f"{'OK' if not dup and not missing else 'FAIL'}: {total} tests across {count} shards, {dup} duplicates, {missing} missing",
            flush=True,
        )
        if dup or missing:
            return 1
    if a.plan or a.check:
        return 0

    mine = bins[index - 1]
    print(
        f"shard {index}/{count}: {len(mine)} units, est. {sum(u.weight for u in mine):.0f}s",
        flush=True,
    )
    return run_shard(mine, go_args, a.out, a.jobs)


if __name__ == "__main__":
    sys.exit(main())
