#!/usr/bin/env python3
# file: scripts/ci/fixture_test_packages.py
# version: 1.5.0
# guid: 92f31101-2399-4ce9-9b83-0af332666123
# last-edited: 2026-10-30
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
packages (they run side by side, as before) and one per slice. Concurrency is
capped by a job budget, FIXTURE_JOBS or else the CPU count: the slices run in a
pool of min(slices, budget // 2), heaviest first, and the whole-package run gets
`-p budget - pool`, so the test binaries running at once stay at or under the
budget, the old envelope of a single `go test` at -p NumCPU (parallel -race
suites have OOM-killed CI steps on these agents). Each child's output goes to a
log file under FIXTURE_LOG_DIR (default: a fresh temp dir) as it is produced and
is printed when the child finishes; a SIGTERM kills the children and prints
what they had written. A slice that selects no test, or a `-run` that matches
none, fails instead of reporting `ok [no tests to run]`.

Each slice logs how many tests it selected and a short hash of their names, so
two hosts' logs can be compared. The names come from the native `go test -list`
(the only list that can be produced here: a linux test binary cannot run on a
darwin agent). On a host other than linux a name that a static scan of the
linux test files finds but the native list lacks is reported as a ::warning::
and not as an error: a darwin agent legitimately lacks linux-only tests, and
failing it would turn every shard that holds one red on that agent. The linux
agents run it; the warning is how a run that did not stays visible.

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
import signal
import subprocess
import sys
import tempfile
import threading
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
    package list. A split package's parts weigh weight/parts each and go to
    different shards while there are shards to spare: a unit prefers a shard
    that holds no other part of its package, and only a package with more
    parts than there are shards doubles up. Without that preference two parts
    of one package can land in the same shard (one process per part, but the
    same agent and the same budget), which is the pile-up splitting is meant to
    avoid."""

    def unit_weight(u: Unit) -> float:
        return weight(u.pkg) / u.parts

    bins: list[list[Unit]] = [[] for _ in range(count)]
    loads = [0.0] * count
    all_units = units(pkgs)
    for u in sorted(all_units, key=lambda u: (-unit_weight(u), u.pkg, u.part)):
        free = [k for k in range(count) if all(o.pkg != u.pkg for o in bins[k])]
        b = min(free or range(count), key=lambda k: (loads[k], k))
        bins[b].append(u)
        loads[b] += unit_weight(u)
    if sorted(u for b in bins for u in b) != sorted(all_units):
        raise SystemExit("fixture_test_packages: shard assignment lost or duplicated a unit")
    return sorted(bins[index - 1])


def shard_load(us: list[Unit]) -> float:
    return sum(weight(u.pkg) / u.parts for u in us)


class Invocation(NamedTuple):
    """One `go test` process of a shard's plan."""

    label: str
    argv: list[str]
    is_slice: bool  # a -run slice of a split package, not the whole-packages run
    weight: float  # estimated seconds; slices start heaviest first
    tests: int = 0  # slices: how many top-level tests the -run selects
    names_hash: str = ""  # slices: short hash of those names


def names_hash(names: list[str]) -> str:
    return hashlib.sha256("\n".join(sorted(names)).encode()).hexdigest()[:12]


def job_budget() -> int:
    """How many test binaries may run at once: FIXTURE_JOBS, else the CPU count.
    Mirrors go_test_shards.default_jobs, which caps its concurrent `go test`
    processes the same way and for the same reason."""
    raw = os.environ.get("FIXTURE_JOBS")
    if raw:
        if not raw.isdigit() or int(raw) < 1:
            raise SystemExit(
                f"fixture_test_packages: FIXTURE_JOBS={raw!r}; want a positive integer"
            )
        return int(raw)
    return max(1, os.cpu_count() or 1)


def plan_concurrency(n_slices: int, budget: int) -> tuple[int, int]:
    """(slices running at once, -p for the whole-packages run). The two add up
    to the budget whenever there are slices, so the test binaries alive at once
    stay at or under it: a slice is one binary, and `go test -p N` runs at most
    N."""
    pool = min(n_slices, max(1, budget // 2))
    return pool, max(1, budget - pool)


def list_split(
    us: list[Unit],
    go_args: list[str],
    list_tests: Callable[[list[str], list[str]], dict[str, list[str]]] = (
        go_test_shards.list_tests_many
    ),
) -> dict[str, list[str]]:
    """`go test -list` names of every split package among us, in one call."""
    pkgs = sorted({u.pkg for u in us if u.parts > 1})
    return list_tests(pkgs, go_args) if pkgs else {}


def invocations(
    us: list[Unit], go_args: list[str], names: dict[str, list[str]], budget: int
) -> list[Invocation]:
    """The `go test` commands that run the units: one for every whole package
    (so they still run side by side under go's own -p, capped at what the
    slices leave of the budget) and one per split part, selecting that part's
    top-level tests with -run. `names` holds each split package's tests, from
    list_split.

    A part left with no test fails here rather than running as
    `[no tests to run]`, which `go test` reports as ok."""
    slices = [u for u in us if u.parts > 1]
    pool, p = plan_concurrency(len(slices), budget)
    out: list[Invocation] = []
    whole = sorted(u for u in us if u.parts == 1)
    if whole:
        argv = ["go", "test", *go_args, "-p", str(p), *(u.pkg for u in whole)]
        out.append(Invocation(f"{len(whole)} whole packages", argv, False, shard_load(whole)))
    for u in sorted(slices, key=lambda u: (-weight(u.pkg) / u.parts, u.pkg, u.part)):
        mine = [n for n in names[u.pkg] if part_of(n, u.parts) == u.part]
        if not mine:
            raise SystemExit(
                f"fixture_test_packages: {u} selects none of {u.pkg}'s {len(names[u.pkg])} "
                "tests; it would run nothing and still report ok"
            )
        pattern = "^(" + "|".join(mine) + ")$"
        argv = ["go", "test", *go_args, "-run", pattern, u.pkg]
        out.append(Invocation(str(u), argv, True, shard_load([u]), len(mine), names_hash(mine)))
    return out


_TEST_FUNC_RE = re.compile(r"^func (Test|Fuzz)(\w*)\(\s*\w+ \*testing\.[TF]\s*\)", re.MULTILINE)


def static_test_names(root: Path, pkgs: list[str]) -> dict[str, set[str]]:
    """Top-level Test and Fuzz functions in the test files `go list` selects
    under the discovery platform (GOOS=linux), by scanning their source. `go
    test -list` cannot give this on another host: it runs the test binary, and
    a linux binary does not run on darwin."""
    out: dict[str, set[str]] = {}
    for p in _go_list_json(root, pkgs, _DISCOVERY_ENV):
        names: set[str] = set()
        for f in [*p.get("TestGoFiles", []), *p.get("XTestGoFiles", [])]:
            try:
                text = (Path(p["Dir"]) / f).read_text(encoding="utf-8")
            except OSError as e:
                raise SystemExit(f"fixture_test_packages: cannot read {p['Dir']}/{f}: {e}")
            for kind, rest in _TEST_FUNC_RE.findall(text):
                # Go: the character after Test/Fuzz must not be a lowercase letter.
                if not rest[:1].islower():
                    names.add(kind + rest)
        out[p["ImportPath"]] = names
    return out


def missing_here(
    native: dict[str, list[str]], discovery: dict[str, set[str]]
) -> dict[str, list[str]]:
    """Tests the linux test files declare that this host's `go test -list` does
    not show, per package. Examples are not compared: go lists only those with
    an output comment, which a source scan cannot tell."""
    out = {}
    for pkg, names in sorted(native.items()):
        here = {n for n in names if n.startswith(("Test", "Fuzz"))}
        gone = sorted(discovery.get(pkg, set()) - here)
        if gone:
            out[pkg] = gone
    return out


def native_goos() -> str:
    res = subprocess.run(["go", "env", "GOOS"], capture_output=True, text=True)
    return res.stdout.strip()


class Result(NamedTuple):
    label: str
    rc: int
    secs: float
    output: str
    ok: bool
    is_slice: bool


def run_invocations(invs: list[Invocation], budget: int, log_dir: Path | None = None) -> int:
    """Run the plan and return 1 if any command failed, else 0.

    The whole-packages run goes in its own thread; the slices go through a pool
    of plan_concurrency's size, heaviest first. Each child writes to its own
    file under log_dir as it runs and the file is printed when the child exits,
    so output survives a killed script; SIGTERM (Woodpecker's cancel) kills the
    children and prints what they wrote."""
    log_dir = log_dir or Path(os.environ.get("FIXTURE_LOG_DIR") or tempfile.mkdtemp("-fixtures"))
    log_dir.mkdir(parents=True, exist_ok=True)
    print(f"fixture_test_packages: child logs in {log_dir}", flush=True)
    pool, _ = plan_concurrency(sum(i.is_slice for i in invs), budget)
    procs: dict[int, subprocess.Popen] = {}
    lock = threading.Lock()
    stopping = threading.Event()
    logs: dict[str, Path] = {}
    printed: set[str] = set()

    def log_path(idx: int, inv: Invocation) -> Path:
        return (
            log_dir / f"{idx:02d}-{re.sub(r'[^A-Za-z0-9]+', '_', inv.label).strip('_')[-60:]}.log"
        )

    def run(idx: int, inv: Invocation) -> Result:
        start = time.monotonic()
        path = logs[inv.label] = log_path(idx, inv)
        with open(path, "w", encoding="utf-8") as f:
            with lock:
                if stopping.is_set():
                    return Result(inv.label, 143, 0.0, "", False, inv.is_slice)
                proc = procs[idx] = subprocess.Popen(
                    inv.argv, stdout=f, stderr=subprocess.STDOUT, text=True
                )
            rc = proc.wait()
        out = path.read_text(encoding="utf-8", errors="replace")
        # A -run that matched nothing exits 0 with this marker. Only a slice
        # has a -run: the whole-packages run lists no tests, and a package in
        # it that has no test files says `[no test files]`, not this.
        empty = inv.is_slice and "[no tests to run]" in out
        return Result(
            inv.label, rc, time.monotonic() - start, out, rc == 0 and not empty, inv.is_slice
        )

    def report(res: Result) -> bool:
        printed.add(res.label)
        print(f"=== {res.label}: {'ok' if res.ok else 'FAIL'} in {res.secs:.0f}s", flush=True)
        print(res.output, end="" if res.output.endswith("\n") else "\n", flush=True)
        if res.rc == 0 and not res.ok:
            print(f"::error::{res.label} ran no tests; -list and -run disagree", flush=True)
        return res.ok

    def stop(*_: object) -> None:
        stopping.set()
        with lock:
            for proc in procs.values():
                if proc.poll() is None:
                    proc.terminate()

    def on_sigterm(signum: int, _frame: object) -> None:
        stop()
        raise SystemExit(128 + signum)

    old = None
    if threading.current_thread() is threading.main_thread():
        old = signal.signal(signal.SIGTERM, on_sigterm)
    failed = 0
    try:
        whole = [(i, inv) for i, inv in enumerate(invs) if not inv.is_slice]
        sliced = [(i, inv) for i, inv in enumerate(invs) if inv.is_slice]
        wex = concurrent.futures.ThreadPoolExecutor(max_workers=max(1, len(whole)))
        sex = concurrent.futures.ThreadPoolExecutor(max_workers=max(1, pool))
        with wex, sex:
            futs = [wex.submit(run, i, inv) for i, inv in whole]
            futs += [sex.submit(run, i, inv) for i, inv in sliced]
            try:
                for fut in concurrent.futures.as_completed(futs):
                    failed += not report(fut.result())
            except BaseException:
                stop()
                raise
    finally:
        if old is not None:
            signal.signal(signal.SIGTERM, old)
        # Whatever the children wrote before they were stopped.
        for label, path in logs.items():
            if label not in printed and path.exists():
                print(f"=== {label}: INTERRUPTED; output so far", flush=True)
                print(path.read_text(encoding="utf-8", errors="replace"), flush=True)
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
    budget = job_budget()
    names = list_split(us, go_args)
    if names and native_goos() != "linux":
        gone = missing_here(names, static_test_names(_REPO_ROOT, sorted(names)))
        for pkg, miss in gone.items():
            sys.stderr.write(
                f"::warning::{pkg}: {len(miss)} test(s) in the linux test files are not in this "
                f"host's `go test -list` and will not run here: {', '.join(miss[:20])}"
                f"{' ...' if len(miss) > 20 else ''}\n"
            )
    invs = invocations(us, go_args, names, budget)
    pool, p = plan_concurrency(sum(i.is_slice for i in invs), budget)
    print(
        f"fixture_test_packages: job budget {budget}: {pool} slice(s) at once, whole-package "
        f"run at -p {p}",
        flush=True,
    )
    for inv in invs:
        extra = f", {inv.tests} tests, names {inv.names_hash}" if inv.is_slice else ""
        print(
            f"fixture_test_packages: {inv.label}: est {inv.weight:.0f}s{extra}: "
            f"{' '.join(inv.argv[:-1])[:200]}",
            flush=True,
        )
    sys.exit(run_invocations(invs, budget))


if __name__ == "__main__":
    main()
