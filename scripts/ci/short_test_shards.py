#!/usr/bin/env python3
# file: scripts/ci/short_test_shards.py
# version: 1.4.2
# guid: 6c2f9a47-3e1b-4d88-9f05-b7a1d4e3c862
# last-edited: 2026-11-09
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
    names exactly once (one `go test -list` call for all split packages,
    go_test_shards.list_tests_many: see there for why not one per package). `--check` extends the name-level check to every test
    package. A compile error in a listed package fails the shard
    (go_test_shards.list_tests exits non-zero), and a split group whose
    pattern matched nothing fails it too.

Modules (R1-03, 09 §9.4 R1's CI classification). `go list ./...` from the
root never reaches the nested core/ module, so core's packages are listed with
`go -C core list ./...` and keyed `core/<path>`: a weights entry for core can
never collide with a root package. The root packages fill the N numbered
shards; every core package goes to the one extra core shard (`--shard core/N`),
which runs `go -C core test` and writes no coverage profile: core's floor
(core/.ci/coverage-floor.txt, from R1-23) is enforced by core-ci.yml's
`make -C core ci` over the profile core's own test-short writes, and the root
floor must keep comparing the same four-profile total. A weights entry that names no package of either module
fails the plan ("stale weight"): the entry would otherwise sit unread, and the
balance it was written for would silently stop applying.

Weights (short_test_weights.json, package -> seconds; JSON carries no header,
so its provenance is recorded here). The values for internal/server (414 s),
internal/metafetch (240 s) and internal/books/store (157 s) are the Mac `-short`
numbers from appendix A.7 of
docs/proposals/2026-10-holistic/07-design-decisions-and-modularity.md.
internal/plugins/maintenance is 186 s, its package elapsed time under
`go test -short -race` on the Mac on 2026-11-06, on a host at load average 50
(it was 702 s there before its tests moved to in-memory stores, and its
go_test_timings.json key predates that move). internal/scanner (A.7 timed out without a
number) and internal/server/handlers/abs (not in A.7) use the sum of their
go_test_timings.json entries, 292 s and 239 s. internal/books is 55 s, its
package elapsed time under `go test -short -race` on the Mac on 2026-11-08 at
load average 16 (R1-23's first-boot, claims and boot tests each boot the
composed app on a real Pebble store; it is in neither A.7 nor
go_test_timings.json, so it weighed the 10 s default). A package with tests that is in
neither file weighs _DEFAULT_WEIGHT (as in fixture_test_packages.py); a package
without test files weighs _NO_TESTS_WEIGHT (it still runs, because `go test`
reports its statements as uncovered and the coverage total depends on that).
A stale weight costs balance, never coverage.

Usage:
  short_test_shards.py --shard K/N --out FILE [--jobs J] [--weights FILE]
      [--timings FILE] -- -short -race -covermode=atomic -timeout 25m
  short_test_shards.py --shard core/N --out FILE -- -short -race -covermode=atomic -timeout 25m
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
# The nested Go modules besides the root, as (module directory, key prefix).
# Matches the root Makefile's GO_MODULE_DIRS; the root is always listed first.
CORE_DIR = "core"

# Import paths -> their `go test -list` names, in one go invocation.
Lister = Callable[[list[str], list[str]], dict[str, list[str]]]


@dataclass(frozen=True)
class Package:
    path: str  # import path, as passed to go test
    key: str  # module-relative key used by the weights and timings files
    has_tests: bool
    module: str = "."  # "." (root) or CORE_DIR; core keys carry a "core/" prefix


@dataclass(frozen=True)
class Unit:
    pkg: Package
    group: int  # 0: the whole package; 1..k: split group
    names: tuple[str, ...]  # empty for a whole package
    weight: float

    @property
    def label(self) -> str:
        return self.pkg.key if self.group == 0 else f"{self.pkg.key}#{self.group}"


# How go test's output is captured. Decoded as text for the report, with
# undecodable bytes replaced: a test that prints raw bytes (a gzip body starts
# 0x1f 0x8b) made strict UTF-8 decoding raise UnicodeDecodeError on 2026-11-08
# (#4455 shard 4/4). That crashed the whole shard and threw away every real
# test result in it.
GO_TEST_IO: dict[str, object] = {"capture_output": True, "text": True, "errors": "replace"}


def _load_module(directory: str) -> list[Package]:
    """Every package `go test ./...` would run in one module, in `go list` order."""
    fmt = "{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}"
    base = ["go"] if directory == "." else ["go", "-C", directory]
    mod = subprocess.run([*base, "list", "-m"], capture_output=True, text=True)
    res = subprocess.run([*base, "list", "-f", fmt, "./..."], capture_output=True, text=True)
    for r in (mod, res):
        if r.returncode != 0:
            sys.stderr.write(r.stdout + r.stderr)
            raise SystemExit(f"::error::go list failed in {directory} (exit {r.returncode})")
    module = mod.stdout.strip()
    prefix = "" if directory == "." else directory + "/"
    pkgs = []
    for line in res.stdout.splitlines():
        path, tests, xtests = line.split()
        rel = "" if path == module else path.removeprefix(module + "/")
        key = (prefix + rel) if rel else (directory if directory != "." else ".")
        pkgs.append(Package(path, key, int(tests) + int(xtests) > 0, directory))
    if not pkgs:
        raise SystemExit(f"::error::go list ./... returned no packages in {directory}")
    return pkgs


def load_packages() -> list[Package]:
    """Every package of every module (root, then core when it exists)."""
    pkgs = _load_module(".")
    if os.path.isfile(os.path.join(CORE_DIR, "go.mod")):
        pkgs += _load_module(CORE_DIR)
    return pkgs


def stale_weights(weights: dict[str, float], pkgs: list[Package]) -> list[str]:
    """Weight keys that name no package of any module, sorted."""
    known = {p.key for p in pkgs}
    return sorted(k for k in weights if k not in known)


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


# No test is free. go_test_timings.json records `go test -json` elapsed
# seconds, which round to 0.00 for most tests, so a split group of a thousand
# such tests estimated at nothing. Measured on the first warm run of main's
# gate (37990246364, 2026-10-09): internal/plugins/maintenance's three groups
# were estimated at 114 s each; the two with ~250 tests ran 122-125 s, the one
# with 1,098 "free" tests ran 355 s. Solving both for a per-test cost gives
# 0.27 s on a 4-vCPU runner under -race (process start, fixture setup, Pebble
# open/close), so every test is estimated at no less than this.
_MIN_TEST_SECONDS = 0.25


def _estimate(names: list[str], timings: dict[str, float]) -> dict[str, float]:
    # The same per-name rule balance() uses for unknown names (the median of
    # the known ones), plus the floor above; balance() is then handed this
    # estimate as its timings so both agree.
    known = sorted(timings[n] for n in names if n in timings)
    default = known[len(known) // 2] if known else 1.0
    return {n: max(timings.get(n, default), _MIN_TEST_SECONDS) for n in names}


def _list_all(pkgs: list[Package], go_args: list[str], lister: Lister) -> dict[str, list[str]]:
    if not pkgs:
        return {}
    return lister([p.path for p in pkgs], go_args)


def build_units(
    pkgs: list[Package],
    weights: dict[str, float],
    timings: dict[str, dict[str, float]],
    go_args: list[str],
    lister: Lister,
) -> tuple[list[Unit], dict[str, list[str]]]:
    """Return the units and the `go test -list` names of every split package."""
    split = {}
    for p in pkgs:
        w = package_weight(p, weights, timings)
        if p.has_tests and w > _SPLIT_CAP:
            split[p.path] = math.ceil(w / _SPLIT_CAP)
    listed = _list_all([p for p in pkgs if p.path in split], go_args, lister)

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
        for i, group in enumerate(gts.balance(names, split[p.path], est), start=1):
            units.append(Unit(p, i, tuple(group), w * sum(est[n] for n in group) / total))
    return units, listed


def assign(units: list[Unit], count: int) -> list[list[Unit]]:
    """Longest-processing-time-first into `count` bins; deterministic.

    Root units fill the `count` numbered bins. Core units are not balanced: they
    all go to one extra bin after them (the core shard), present only when the
    plan has core units. A root-only plan is exactly `count` bins.
    """
    bins: list[list[Unit]] = [[] for _ in range(count)]
    loads = [0.0] * count
    root = [u for u in units if u.pkg.module == "."]
    for u in sorted(root, key=lambda u: (-u.weight, u.label)):
        b = min(range(count), key=lambda k: (loads[k], k))
        bins[b].append(u)
        loads[b] += u.weight
    core = [u for u in units if u.pkg.module != "."]
    if core:
        bins.append(sorted(core, key=lambda u: (-u.weight, u.label)))
    return bins


def verify(bins: list[list[Unit]], pkgs: list[Package], listed: dict[str, list[str]]) -> list[str]:
    """Problems with the plan: empty shards, packages or split names not assigned exactly once."""
    has_core = any(p.module != "." for p in pkgs)
    root_count = len(bins) - 1 if has_core else len(bins)
    errors = []
    for i, b in enumerate(bins, start=1):
        if not b:
            errors.append(
                "core shard has no units"
                if i > root_count
                else f"shard {i}/{root_count} has no units"
            )
        for u in b:
            if i > root_count and u.pkg.module == ".":
                errors.append(f"{u.pkg.key}: root package in the core shard")
            elif i <= root_count and u.pkg.module != ".":
                errors.append(f"{u.pkg.key}: core package in root shard {i}/{root_count}")
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
) -> tuple[int, int, int]:
    """Name-level union over every test package: (total, duplicates, missing)."""
    need = [p for p in pkgs if p.has_tests and p.path not in listed]
    every = dict(listed)
    every.update(_list_all(need, go_args, lister))
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
    root_count = len(bins) - 1 if any(u.pkg.module != "." for b in bins for u in b) else len(bins)
    for i, b in enumerate(bins, start=1):
        mark = " <- this shard" if i == index else ""
        name = f"shard {i}/{root_count}" if i <= root_count else f"core shard (of {root_count})"
        print(f"{name}: est. {sum(u.weight for u in b):.0f}s, {len(b)} units{mark}")
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


@dataclass(frozen=True)
class Call:
    label: str
    module: str  # "." or CORE_DIR: where `go test` runs
    go_args: tuple[str, ...]  # flags for this call (no coverage flags for core)
    args: tuple[str, ...]  # -run pattern and import paths
    is_split: bool


# Flags that turn on coverage. A core call drops them: core has no coverage
# floor yet, so a profile would only cost time, and `go test -coverprofile`
# cannot span two modules anyway. `-covermode` and `-coverpkg` take a value, as
# a second argument or after `=`.
_COVER_VALUE_FLAGS = ("-covermode", "-coverpkg", "-coverprofile")


def _without_coverage(go_args: list[str]) -> list[str]:
    out: list[str] = []
    skip = False
    for a in go_args:
        if skip:
            skip = False
            continue
        name = a.split("=", 1)[0]
        if name in _COVER_VALUE_FLAGS or name == "-cover":
            skip = "=" not in a and name in _COVER_VALUE_FLAGS
            continue
        out.append(a)
    return out


def plan_calls(units: list[Unit], go_args: list[str]) -> list[Call]:
    """The `go test` calls of one shard: per module, one for all whole packages
    and one per split group."""
    calls: list[Call] = []
    for module in dict.fromkeys(u.pkg.module for u in units):
        mine = [u for u in units if u.pkg.module == module]
        args = tuple(go_args) if module == "." else tuple(_without_coverage(go_args))
        whole = [u.pkg.path for u in mine if u.group == 0]
        if whole:
            label = f"{len(whole)} whole packages" + ("" if module == "." else f" of {module}")
            calls.append(Call(label, module, args, tuple(whole), False))
        for u in sorted((u for u in mine if u.group), key=lambda u: -u.weight):
            calls.append(
                Call(u.label, module, args, ("-run", run_pattern(u.names), u.pkg.path), True)
            )
    return calls


def run_shard(units: list[Unit], go_args: list[str], out: str, jobs: int = 0) -> int:
    """Run a shard's go test calls; jobs <= 0 runs every call at once.

    The default matters for the timeout invariant in ci.yml: each call has its
    own -timeout 25m, and a call queued behind another for a free slot could
    start late enough that the job's 35-minute cap kills it before Go's own
    timeout prints the goroutine dump. go_test_shards.py defaults the same way.

    Root calls write a coverage profile each and merge into `out`; core calls
    run `go -C core test` and write none, so a core-only shard writes no `out`.
    """
    calls = plan_calls(units, go_args)
    workers = jobs if jobs > 0 else len(calls)

    tmp = tempfile.mkdtemp(prefix="short-shard-")

    def run(i: int) -> tuple[str, int, float, str, bool, str | None]:
        call = calls[i]
        base = ["go"] if call.module == "." else ["go", "-C", call.module]
        prof = os.path.join(tmp, f"{i}.out") if call.module == "." else None
        cover = [f"-coverprofile={prof}"] if prof else []
        start = time.monotonic()
        res = subprocess.run([*base, "test", *call.go_args, *cover, *call.args], **GO_TEST_IO)
        return (
            call.label,
            res.returncode,
            time.monotonic() - start,
            res.stdout + res.stderr,
            call.is_split,
            prof,
        )

    failed, profiles = 0, []
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, workers)) as ex:
            for fut in concurrent.futures.as_completed(
                [ex.submit(run, i) for i in range(len(calls))]
            ):
                label, rc, secs, output, is_split, prof = fut.result()
                if prof:
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
        if profiles:
            gts.merge_profiles(sorted(profiles), out)
        else:
            print("no coverage profile written (core has no coverage floor yet)", flush=True)
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
    """`K/N` is root shard K of N; `core/N` is the core shard after the N root shards."""
    m = re.fullmatch(r"(\d+|core)/(\d+)", spec or "")
    if m and m.group(1) == "core" and int(m.group(2)) >= 1:
        return int(m.group(2)) + 1, int(m.group(2))
    if not m or m.group(1) == "core" or not 1 <= int(m.group(1)) <= int(m.group(2)):
        raise SystemExit(
            f"short_test_shards: bad --shard {spec!r}; want K/N with 1 <= K <= N, or core/N"
        )
    return int(m.group(1)), int(m.group(2))


def module_lister(lister: Lister, pkgs: list[Package]) -> Lister:
    """Run `lister` for each module from that module's own directory.

    `go test -list` of a core package from the root fails ("missing go.sum
    entry": the root's go.sum holds only what the root imports), so core's
    packages are listed with core as the working directory. A lister that is
    not gts.list_tests_many (a test stub) takes no working directory and is
    called once, as before.
    """
    core_paths = {p.path for p in pkgs if p.module != "."}
    if not core_paths or lister is not gts.list_tests_many:
        return lister

    def run(paths: list[str], go_args: list[str]) -> dict[str, list[str]]:
        out: dict[str, list[str]] = {}
        root = [p for p in paths if p not in core_paths]
        core = [p for p in paths if p in core_paths]
        if root:
            out.update(lister(root, go_args))
        if core:
            here = os.getcwd()
            os.chdir(CORE_DIR)
            try:
                out.update(lister(core, go_args))
            finally:
                os.chdir(here)
        return out

    return run


def main(
    argv: list[str] | None = None,
    lister: Lister = gts.list_tests_many,
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
        help="concurrent go test calls in a shard (default 0: all at once)",
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
    lister = module_lister(lister, pkgs)
    # A weight for a package no module lists is never read; say so, because the
    # balance it was written for silently stops applying (a package was moved
    # or renamed without its entry).
    errors = [
        f"{k}: weight entry in {os.path.basename(a.weights)} but not in go list ./... of any module"
        for k in stale_weights(weights, pkgs)
    ]
    units, listed = build_units(pkgs, weights, timings, go_args, lister)
    bins = assign(units, count)
    errors += verify(bins, pkgs, listed)
    for e in errors:
        print(f"::error::{e}", flush=True)
    if errors:
        return 1

    if a.plan:
        _print_plan(bins, index)
    if a.check:
        total, dup, missing = full_check(bins, pkgs, listed, go_args, lister)
        print(
            f"{'OK' if not dup and not missing else 'FAIL'}: {total} tests across {count} shards"
            f"{' and the core shard' if len(bins) > count else ''}, {dup} duplicates, {missing} missing",
            flush=True,
        )
        if dup or missing:
            return 1
    if a.plan or a.check:
        return 0

    if index > len(bins):
        raise SystemExit(f"::error::--shard {a.shard}: the plan has no core shard (no core module)")
    mine = bins[index - 1]
    name = f"shard {index}/{count}" if index <= count else f"core shard (of {count})"
    print(f"{name}: {len(mine)} units, est. {sum(u.weight for u in mine):.0f}s", flush=True)
    return run_shard(mine, go_args, a.out, a.jobs)


if __name__ == "__main__":
    sys.exit(main())
