#!/usr/bin/env python3
# file: scripts/ci/fixture_test_packages.py
# version: 1.4.0
# guid: 92f31101-2399-4ce9-9b83-0af332666123
# last-edited: 2026-10-29
"""Select, partition and run the Go packages whose tests skip under -short, for
`make test-fixtures`.

Every PR-gating Go test run in this repo passes -short (`make test-short` in
ci.yml, all three Woodpecker test workflows). A test that calls
`testing.Short()` and skips, directly or through a shared fixture such as
internal/versionprimary/vptest (whose `New` skips under -short), therefore
never gates a merge. This script finds every such package so a separate job
can run exactly those packages WITHOUT -short.

A package is selected when any of these holds:
  * one of its _test.go files calls testing.Short(), or
  * its tests import a fixture helper: a package whose non-test .go files
    call testing.Short() (such as vptest), or a package that reaches one
    through its non-test imports, at any depth (a wrapper around vptest is a
    helper too). Helpers are found generically, so the next one is picked up
    without editing this script, or
  * it is itself a fixture helper and has tests: a package's TestImports
    never list the package itself, so the import rule alone would miss them.

Discovery goes through `go list ./...`, which honors go.mod's `ignore ./web`,
so Go files under web/node_modules are never picked up.

The partition is host-independent. Each of the three Woodpecker shard
workflows runs this script on whichever agent picks it up (linux/amd64 or
darwin/arm64), and OS-specific test files and build tags (reflink_control_*,
race_enabled_*) make the native package set differ between hosts. A greedy bin
assignment over differing lists can shift so a package lands in NO shard
(silent skip) or in two. So discovery always runs under GOOS=linux
GOARCH=amd64 CGO_ENABLED=1 -- the set ci.yml's ubuntu runner sees -- and only
the packages assigned to THIS shard are then checked against the native
`go list`; one that cannot be tested here fails loudly instead of being
skipped. A short hash of the partition input goes to stderr so shard logs can
be compared.

Usage:
  fixture_test_packages.py                 # every unit, one per line
  fixture_test_packages.py --shard 2/3     # shard 2 of 3 (1-based)
  fixture_test_packages.py --shard 2/3 --run -- -race -count=1 -timeout 25m
                                           # run shard 2 with `go test <args>`

Partition. The work is split into units: a whole package, or one hash slice of a
package too heavy for a shard. A unit prints as `pkg` or `pkg#2/3` (slice 2 of
3). Package weights are in _WEIGHTS (measured on the Woodpecker agents, see
there); a package heavier than _SPLIT_TARGET seconds is split into
ceil(weight / _SPLIT_TARGET) slices, each weighing weight / slices, and every
unit is then dealt longest-first into the lightest shard. internal/server alone
ran ~1300 s of a 1500 s timeout in one process (pipeline 1823 timed out at
1501.9 s); as two slices in different shards it is two ~400 s processes.

A slice is chosen by test NAME (part_of: a hash), not by position in the
package's `go test -list` output, so the three shard workflows agree even
though they list tests on different hosts (OS-specific _test.go files). Slices
select their tests with `go test -run '^(A|B|...)$'`. The package's tests are
independent processes, as in go_test_shards.py (which splits the short suite
the same way): a test that only passes after another test ran in the same
process would fail here, and TestMain-level state is rebuilt per process.

--run executes the selected units: one `go test` for all the shard's whole
packages (they run side by side, as before) and one per slice, all at once,
each printed when it finishes. A slice that selects no test, or a `-run` that
matches none, fails instead of reporting `ok [no tests to run]`.

Exits non-zero if nothing is selected, if a known helper in _REQUIRED_HELPERS is
not detected as one, or if a shard comes out empty, so a broken discovery can
never turn into a `go test` with no packages (which would test only the module
root and pass) or into a list that silently dropped every vptest-based package.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import math
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable, NamedTuple

# go_test_shards.py lives next to this file. It is on sys.path when this file
# runs as a script; the unit tests load this file by path, where it is not.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import go_test_shards  # noqa: E402

MODULE = "github.com/falkcorp/audiobook-organizer"

# Seconds a package takes inside `make test-fixtures` on the Woodpecker heavy
# agents: the median of the `ok <pkg> <secs>` lines of five green pipelines
# (1807, 1810, 1821, 1822, 1824; 2026-10-27..29; llm1 and the Mac agent), i.e.
# package wall time while its shard-mates run beside it, which is the number
# the per-process -timeout is spent against. They replace figures from the
# 2026-10-05 nightly on GitHub's ubuntu-latest (internal/server 160 s,
# internal/plugins/maintenance 215 s): measured now, with the packages grown and
# the shard-mates contending, internal/server took 1030-1390 s and maintenance
# 870-1475 s, and a package weighted at an eighth of its cost leaves its shard
# looking light until the 25m timeout fires (pipelines 1820 and 1823).
#
# internal/server is the one entry that is NOT a median: 830 is its 1334 s
# median times 0.62, the ratio of its own test time before and after the
# argon2/throttle changes in the same change (internal/server/main_test.go),
# the highest of the two before/after ratios measured on 211-test slices on one
# Mac (88 s -> 55 s = 0.62 on a slice with none of the worst offenders; 291 s ->
# 113 s = 0.39 on the slice with the login-throttle tests, whose seven worst
# tests went from 22-40 s each to under 1 s). Recalibrate it from the first run
# of this change. A stale weight costs balance, and a stale LOW
# weight also costs the split: split_count() is derived from it.
_WEIGHTS = {
    "internal/server": 830,
    "internal/plugins/maintenance": 1293,
    "internal/audiobooks": 738,
    "internal/scanner": 705,
    "internal/server/handlers": 543,
    "internal/database": 520,
    "internal/undo": 418,
    "internal/merge": 382,
    "internal/versions": 295,
    "internal/organizer": 179,
    "internal/playlist": 155,
    "internal/operations/registry": 135,
    "internal/maintenance/jobs": 125,
    "internal/dedup": 89,
    "internal/plugins/dedup": 86,
    "internal/catalog": 51,
    "internal/batch": 50,
    "internal/itunes/service": 42,
    "internal/reconcile": 34,
    "internal/versionprimary": 23,
    "internal/server/handlers/duplicates": 10,
    "internal/opscatalog": 7,
    "tools/cmd/oplint": 5,
    "internal/realtime": 3,
    "internal/diag/flight": 2,
}
_DEFAULT_WEIGHT = 10

# A package heavier than this many seconds (in _WEIGHTS' unit) runs as
# ceil(weight / _SPLIT_TARGET) processes, each a hash slice of its top-level
# tests, scheduled independently so they can land in different shards. 600 is
# 40% of FIXTURE_TIMEOUT's 25m: a process can run 2.5x slower than the median
# above (the same package ranged 1.4x-1.9x across those five pipelines) and
# still finish, where an unsplit internal/server at 1030-1390 s had no such room
# and timed out at 1501.9 s in pipeline 1823. Lower means more processes, each
# paying its own link of a -race test binary (~250 MB for internal/server).
_SPLIT_TARGET = 600

# Helpers that must be detected. vptest.New is the fixture the merge, undo and
# redirect state-machine tests are built on; if the detector stops seeing it
# (a rename of testing.Short, a refactor that moves the skip), the selected
# list would still be non-empty -- the direct testing.Short() callers keep it
# populated -- while every vptest-based package silently fell out of the gate.
_REQUIRED_HELPERS = ("internal/versionprimary/vptest",)

_REPO_ROOT = Path(__file__).resolve().parents[2]

_SHORT_RE = re.compile(r"\btesting\.Short\(\)")

# The platform the partition is computed for, whatever host runs the script:
# what ci.yml's ubuntu-latest runner has (cgo on, which is its default there).
_DISCOVERY_ENV = {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1"}


def _calls_short(directory: str, files: list[str]) -> bool:
    for name in files:
        try:
            if _SHORT_RE.search((Path(directory) / name).read_text(encoding="utf-8")):
                return True
        except OSError as e:
            raise SystemExit(f"fixture_test_packages: cannot read {directory}/{name}: {e}")
    return False


def _go_list_json(
    root: Path, patterns: list[str], env_overrides: dict[str, str], extra: tuple[str, ...] = ()
) -> list[dict]:
    res = subprocess.run(
        ["go", "list", *extra, "-json", *patterns],
        capture_output=True,
        text=True,
        cwd=root,
        env={**os.environ, **env_overrides},
    )
    if res.returncode != 0:
        sys.stderr.write(res.stderr)
        raise SystemExit(f"fixture_test_packages: go list failed (exit {res.returncode})")
    pkgs = []
    dec = json.JSONDecoder()
    text, i = res.stdout, 0
    while True:
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        obj, i = dec.raw_decode(text, i)
        pkgs.append(obj)
    return pkgs


def _load_packages(root: Path) -> list[dict]:
    return _go_list_json(root, ["./..."], _DISCOVERY_ENV)


def partition_hash(pkgs: list[str]) -> str:
    """Short digest of the full partition input, to compare shard logs."""
    return hashlib.sha256("\n".join(sorted(pkgs)).encode()).hexdigest()[:12]


def verify_testable_here(assigned: list[str], root: Path = _REPO_ROOT) -> None:
    """Fail loudly if a package assigned to this shard cannot be tested on this
    host: absent from the native `go list`, broken there, or with every test
    file excluded by this host's GOOS/GOARCH/tags. Skipping it instead would
    drop it from every shard's coverage without a trace."""
    native = _go_list_json(root, assigned, {}, extra=("-e",))
    by_path = {p["ImportPath"]: p for p in native}
    problems = []
    for path in assigned:
        p = by_path.get(path)
        if p is None:
            problems.append(f"{path}: not in this host's go list")
        elif p.get("Error") or p.get("DepsErrors"):
            err = p.get("Error") or p["DepsErrors"][0]
            problems.append(f"{path}: {err.get('Err', err) if isinstance(err, dict) else err}")
        elif not (p.get("TestGoFiles") or p.get("XTestGoFiles")):
            problems.append(f"{path}: no test files build on this host")
    if problems:
        raise SystemExit(
            "fixture_test_packages: package(s) assigned to this shard cannot be tested on "
            "this host (refusing to skip them silently):\n  " + "\n  ".join(problems)
        )


def find_helpers(
    pkgs: list[dict], calls_short: Callable[[str, list[str]], bool] = _calls_short
) -> set[str]:
    """Fixture helpers: non-test code that calls testing.Short(), closed over
    non-test Imports. A package importing a helper is itself a helper, at any
    depth, so a wrapper around vptest (or a wrapper around that) is found."""
    helpers = {p["ImportPath"] for p in pkgs if calls_short(p["Dir"], p.get("GoFiles", []))}
    importers: dict[str, set[str]] = {}
    for p in pkgs:
        for imp in p.get("Imports", []):
            importers.setdefault(imp, set()).add(p["ImportPath"])
    queue = list(helpers)
    while queue:
        for parent in importers.get(queue.pop(), ()):
            if parent not in helpers:
                helpers.add(parent)
                queue.append(parent)
    return helpers


def select(
    pkgs: list[dict],
    helpers: set[str],
    calls_short: Callable[[str, list[str]], bool] = _calls_short,
) -> list[str]:
    selected = []
    for p in pkgs:
        test_files = p.get("TestGoFiles", []) + p.get("XTestGoFiles", [])
        if not test_files:
            continue
        imports = set(p.get("TestImports", [])) | set(p.get("XTestImports", []))
        # A helper's own tests are never reached by the import rule (a package's
        # TestImports do not list the package itself), yet they run against the
        # code that calls testing.Short(), so they belong in the list too.
        if p["ImportPath"] in helpers or calls_short(p["Dir"], test_files) or imports & helpers:
            selected.append(p["ImportPath"])
    return sorted(selected)


class Discovery(NamedTuple):
    selected: list[str]
    helpers: set[str]


def discover(root: Path = _REPO_ROOT) -> Discovery:
    """Select the packages and fail closed on a discovery that is visibly broken."""
    pkgs = _load_packages(root)
    helpers = find_helpers(pkgs)
    missing = [h for h in _REQUIRED_HELPERS if f"{MODULE}/{h}" not in helpers]
    if missing:
        raise SystemExit(
            f"fixture_test_packages: {', '.join(missing)} not detected as a fixture helper; "
            "discovery is broken (it would silently drop every package built on it)"
        )
    selected = select(pkgs, helpers)
    if not selected:
        raise SystemExit("fixture_test_packages: no package selected; discovery is broken")
    return Discovery(selected, helpers)


class Unit(NamedTuple):
    """One schedulable piece of work: a whole package, or one hash part of a
    package too heavy for a single shard (see split_count)."""

    pkg: str  # full import path
    part: int  # 0-based; 0 for a whole package
    parts: int  # 1 = the whole package

    def __str__(self) -> str:
        return self.pkg if self.parts == 1 else f"{self.pkg}#{self.part + 1}/{self.parts}"


def weight(pkg: str) -> int:
    return _WEIGHTS.get(pkg.removeprefix(MODULE + "/"), _DEFAULT_WEIGHT)


def split_count(pkg: str) -> int:
    """How many parts a package runs as: one process per _SPLIT_TARGET seconds
    of its weight, rounded up. A package at or under the target is not split."""
    return max(1, math.ceil(weight(pkg) / _SPLIT_TARGET))


def part_of(test_name: str, parts: int) -> int:
    """The part a top-level test belongs to. A pure function of the NAME (not
    of the package's test list, its order or its size), so the three shard
    workflows agree on it even when their hosts compile different test files
    (OS-specific _test.go files, build tags): a test that exists on only one
    host lands in the same part there as it would anywhere, and no test can end
    up in two parts or none."""
    return int.from_bytes(hashlib.sha256(test_name.encode()).digest()[:8], "big") % parts


def units(pkgs: list[str]) -> list[Unit]:
    out = []
    for p in pkgs:
        k = split_count(p)
        out.extend(Unit(p, i, k) for i in range(k))
    return out


def shard(pkgs: list[str], index: int, count: int) -> list[Unit]:
    """Longest-first greedy split of the units; deterministic for a given
    package list. A split package's parts weigh weight/parts each and may land
    in different shards; that is the point."""

    def unit_weight(u: Unit) -> float:
        return weight(u.pkg) / u.parts

    bins: list[list[Unit]] = [[] for _ in range(count)]
    loads = [0.0] * count
    all_units = units(pkgs)
    for u in sorted(all_units, key=lambda u: (-unit_weight(u), u.pkg, u.part)):
        b = min(range(count), key=lambda k: (loads[k], k))
        bins[b].append(u)
        loads[b] += unit_weight(u)
    if sorted(u for b in bins for u in b) != sorted(all_units):
        raise SystemExit("fixture_test_packages: shard assignment lost or duplicated a unit")
    return sorted(bins[index - 1])


def shard_load(us: list[Unit]) -> float:
    return sum(weight(u.pkg) / u.parts for u in us)


def invocations(
    us: list[Unit],
    go_args: list[str],
    list_tests: Callable[[list[str], list[str]], dict[str, list[str]]] = (
        go_test_shards.list_tests_many
    ),
) -> list[tuple[str, list[str]]]:
    """The `go test` commands that run the units: one for every whole package
    (so they still run side by side under go's own -p), and one per split part,
    selecting that part's top-level tests with -run.

    Split packages' test names come from `go test -list`, in one call for all
    of them. A part left with no test fails here rather than running as
    `[no tests to run]`, which `go test` reports as ok."""
    cmds: list[tuple[str, list[str]]] = []
    whole = sorted(u.pkg for u in us if u.parts == 1)
    if whole:
        cmds.append((f"{len(whole)} whole packages", ["go", "test", *go_args, *whole]))
    split = [u for u in us if u.parts > 1]
    names = list_tests(sorted({u.pkg for u in split}), go_args) if split else {}
    for u in split:
        mine = [n for n in names[u.pkg] if part_of(n, u.parts) == u.part]
        if not mine:
            raise SystemExit(
                f"fixture_test_packages: {u} selects none of {u.pkg}'s {len(names[u.pkg])} "
                "tests; it would run nothing and still report ok"
            )
        pattern = "^(" + "|".join(mine) + ")$"
        cmds.append((str(u), ["go", "test", *go_args, "-run", pattern, u.pkg]))
    return cmds


def run_invocations(cmds: list[tuple[str, list[str]]]) -> int:
    """Run the commands side by side, as one `go test` over every package did,
    print each one's output as it finishes, and return 1 if any failed."""

    def run(item: tuple[str, list[str]]) -> tuple[str, int, float, str]:
        label, argv = item
        start = time.monotonic()
        res = subprocess.run(argv, capture_output=True, text=True)
        return label, res.returncode, time.monotonic() - start, res.stdout + res.stderr

    failed = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(cmds)) as ex:
        for fut in concurrent.futures.as_completed([ex.submit(run, c) for c in cmds]):
            label, rc, secs, out = fut.result()
            # A -run that matched nothing exits 0 with this marker.
            empty = "[no tests to run]" in out
            ok = rc == 0 and not empty
            print(f"=== {label}: {'ok' if ok else 'FAIL'} in {secs:.0f}s", flush=True)
            print(out, end="" if out.endswith("\n") else "\n", flush=True)
            if empty:
                print(f"::error::{label} ran no tests; -list and -run disagree", flush=True)
            failed += not ok
    return 1 if failed else 0


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--shard", help="i/n, 1-based: only shard i of n")
    ap.add_argument(
        "--run",
        action="store_true",
        help="run the selected units with `go test <args after -->` instead of listing them",
    )
    ap.add_argument("go_args", nargs=argparse.REMAINDER)
    args = ap.parse_args()
    go_args = args.go_args[1:] if args.go_args[:1] == ["--"] else args.go_args
    if go_args and not args.run:
        ap.error("go test arguments only make sense with --run")

    pkgs = discover().selected
    sys.stderr.write(
        f"fixture_test_packages: partition input {len(pkgs)} packages, "
        f"hash {partition_hash(pkgs)} (GOOS=linux GOARCH=amd64 CGO_ENABLED=1)\n"
    )
    us = units(pkgs)
    if args.shard:
        m = re.fullmatch(r"(\d+)/(\d+)", args.shard)
        if not m or not 1 <= int(m.group(1)) <= int(m.group(2)):
            raise SystemExit(
                f"fixture_test_packages: bad --shard {args.shard!r}; want i/n with 1 <= i <= n"
            )
        us = shard(pkgs, int(m.group(1)), int(m.group(2)))
        if not us:
            raise SystemExit(
                f"fixture_test_packages: shard {args.shard} is empty; use fewer shards"
            )
    verify_testable_here(sorted({u.pkg for u in us}))
    sys.stderr.write(f"fixture_test_packages: estimated load {shard_load(us):.0f}s\n")
    if not args.run:
        print("\n".join(str(u) for u in us))
        return
    cmds = invocations(us, go_args)
    for label, argv in cmds:
        print(f"fixture_test_packages: {label}: {' '.join(argv[:-1])[:200]}", flush=True)
    sys.exit(run_invocations(cmds))


if __name__ == "__main__":
    main()
