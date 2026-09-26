#!/usr/bin/env python3
# file: scripts/ci_remote.py
# version: 1.0.0
# guid: 4a04a3b5-d6a8-4802-b193-28b78d787636
# last-edited: 2026-09-26

"""Run `make ci` sharded across a pool of remote runner nodes (`make ci-remote`).

Why this exists: on 2026-09-25 several agents ran `make ci` on the same Mac at
once, load hit ~150, and packages that pass on their own blew the 25-minute
test timeout. Every agent's local gate read red for reasons unrelated to its
change. This script moves the heavy legs to the nodes listed in CI_NODES.

What it does, in order:

1. Resolve the commit to test: the worktree's HEAD (refuses a dirty tree unless
   --allow-dirty, which snapshots the tree into a temporary commit object that
   no branch points at), or --ref.
2. Read CI_NODES (environment first, then Makefile.local in this checkout, then
   Makefile.local in the primary checkout) and probe each node over ssh: OS,
   cores, load, free memory, tools on the CI PATH, and whether a LOCAL Ollama
   is actively serving.
3. Push the commit to each usable node's bare repo by explicit refspec and
   check it out into ~/ci/jobs/<sha>-<runid> (a git worktree of the bare repo),
   with a per-node shared GOCACHE and GOMODCACHE.
4. Build the legs that `make ci` runs: `go test -short -race` split by package
   into shards balanced by measured runtime, plus vet, staticcheck, sdkguard,
   bench-check, fmt-check, the errcheck ratchet, the web tests and mocks-check.
   Packages whose tests decode audio (ffmpeg/ffprobe/fpcalc) never go to a
   `nodecode` node, and a nodecode node runs with those binaries filtered out
   of PATH so a missed test skips instead of decoding.
5. Run the legs from a shared queue. Each node has `max=N` slot locks under
   ~/ci/locks (mkdir-based, heartbeat-refreshed, expired when stale), so
   parallel agents queue instead of piling on. Logs stream back with a
   `[leg@node]` prefix and are saved under .ci-remote/<sha>/.
6. Merge the shards' coverage profiles and run `make coverage-check-short` on
   the merged profile, exactly as `make ci` does after test-short.
7. Print a summary table; exit non-zero if any leg failed. Remove job dirs on
   success; keep the newest 3 failed ones per node.

If no node is usable it falls back to plain `make ci`.

Node list format (never committed; the repo is public):

    CI_NODES = user@node-a.example:max=2 user@node-b.example:prod,nodecode,max=2

Flags: `nodecode` (no audio decode/encode/fingerprint on this node), `prod`
(production host: lowest priority, at most half the cores, skipped above a load
threshold), `max=N` (at most N concurrent jobs on this node, across all agents).

Full docs: docs/process/ci-remote.md.
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as _dt
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from typing import Callable, Iterable

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

GO_TOOLCHAIN = "go1.27.1"  # keep in step with the Makefile's GOTOOLCHAIN pin
REMOTE_ROOT = "ci"  # relative to the node user's $HOME
BARE_REPO = f"{REMOTE_ROOT}/audiobook-organizer.git"
DECODE_TOOLS = ("ffmpeg", "ffprobe", "fpcalc")
# Directories put first on PATH on every node. The Homebrew/local dirs matter on
# macOS nodes, where a non-interactive ssh session does not have them and every
# decode test would silently skip.
REMOTE_PATH_PREFIX = (
    "$HOME/ci/opt/go/bin:$HOME/ci/opt/node/bin:$HOME/go/bin:"
    "/opt/homebrew/bin:/usr/local/bin"
)
TEST_SHORT_TIMEOUT = "25m"  # same as the Makefile's test-short
LEG_TIMEOUT_S = 45 * 60  # hard kill for one leg (ssh side)
STALE_LOCK_MINUTES = 10  # a lock whose heartbeat stopped this long ago is dead
LOCK_BUSY_RC = 75  # EX_TEMPFAIL: every slot on the node is held
NODECODE_REFUSE_RC = 70  # decode tools still visible on a nodecode node
KEEP_FAILED_JOBS = 3
PROD_MAX_LOAD_FRAC = float(os.environ.get("CI_REMOTE_PROD_MAX_LOAD_FRAC", "0.5"))
PROD_MIN_MEM_GB = float(os.environ.get("CI_REMOTE_PROD_MIN_MEM_GB", "8"))
BUSY_LOAD_FRAC = 1.5  # non-prod node with load1 above ncpu*this is de-weighted
OLLAMA_BUSY_CPU_PCT = 20.0
COVERAGE_BEGIN = "@@CI-REMOTE-COVERAGE-BEGIN@@"
COVERAGE_END = "@@CI-REMOTE-COVERAGE-END@@"

# A test file needs a real decoder when it looks one up or runs one. Matches
# exec.LookPath("ffmpeg"), exec.Command("ffprobe", ...),
# exec.CommandContext(ctx, "fpcalc", ...) and hard-coded absolute paths.
DECODE_RE = re.compile(
    r'LookPath\(\s*"(?:ffmpeg|ffprobe|fpcalc)"'
    r'|Command(?:Context)?\([^)\n]*"(?:ffmpeg|ffprobe|fpcalc)"'
    r'|"/(?:usr/(?:local/)?|opt/homebrew/)bin/(?:ffmpeg|ffprobe|fpcalc)"'
)

GO_RESULT_RE = re.compile(
    r"^(ok|FAIL|\?)\s+(\S+)\s+(?:(\d+(?:\.\d+)?)s|\(cached\)|\[no test files\]|\[build failed\]|\[setup failed\])"
)

GO_NOTEST_COVER_RE = re.compile(r"^\s+(\S+)\s+coverage: \d+(?:\.\d+)?% of statements\s*$")


# ---------------------------------------------------------------------------
# Node list
# ---------------------------------------------------------------------------


@dataclasses.dataclass(frozen=True)
class Node:
    """One entry of CI_NODES."""

    user: str
    host: str
    nodecode: bool = False
    prod: bool = False
    max_jobs: int = 1

    @property
    def target(self) -> str:
        return f"{self.user}@{self.host}" if self.user else self.host

    @property
    def name(self) -> str:
        return self.host


def parse_ci_nodes(spec: str) -> list[Node]:
    """Parse `user@host[:flag,flag]` entries separated by whitespace.

    Unknown flags are an error rather than silently ignored: a typo in
    `nodecode` must not route decode tests onto a node that is banned from
    running them.
    """
    nodes: list[Node] = []
    for raw in spec.split():
        target, _, flagstr = raw.partition(":")
        if not target:
            raise ValueError(f"CI_NODES entry {raw!r}: missing host")
        user, sep, host = target.rpartition("@")
        if not sep:
            user, host = "", target
        if not host:
            raise ValueError(f"CI_NODES entry {raw!r}: missing host")
        nodecode = prod = False
        max_jobs = 1
        for flag in filter(None, (f.strip() for f in flagstr.split(","))):
            if flag == "nodecode":
                nodecode = True
            elif flag == "prod":
                prod = True
            elif flag.startswith("max="):
                try:
                    max_jobs = int(flag[4:])
                except ValueError as exc:
                    raise ValueError(f"CI_NODES entry {raw!r}: bad {flag!r}") from exc
                if max_jobs < 1:
                    raise ValueError(f"CI_NODES entry {raw!r}: max must be >= 1")
            else:
                raise ValueError(f"CI_NODES entry {raw!r}: unknown flag {flag!r}")
        nodes.append(Node(user, host, nodecode, prod, max_jobs))
    return nodes


_MAKE_ASSIGN_RE = re.compile(r"^\s*(?:export\s+)?CI_NODES\s*(?:\?=|:=|::=|\+=|=)\s*(.*?)\s*$")


def ci_nodes_from_makefile(text: str) -> str | None:
    """Return the CI_NODES value assigned in a Makefile.local, or None.

    Handles `=`, `?=`, `:=`, `+=`, a leading `export`, trailing `# comments`
    and backslash continuations. Only literal values are supported.
    """
    lines = text.splitlines()
    value: str | None = None
    i = 0
    while i < len(lines):
        line = lines[i]
        while line.endswith("\\") and i + 1 < len(lines):
            i += 1
            line = line[:-1] + " " + lines[i]
        m = _MAKE_ASSIGN_RE.match(line)
        if m:
            v = m.group(1).split("#", 1)[0].strip()
            if "+=" in line.split("CI_NODES", 1)[1][:4] and value:
                value = f"{value} {v}"
            else:
                value = v
        i += 1
    return " ".join(value.split()) if value is not None else None


def find_ci_nodes(env: dict[str, str], makefiles: Iterable[Path]) -> tuple[str, str]:
    """Return (spec, source). The environment wins over any Makefile.local."""
    if env.get("CI_NODES", "").strip():
        return env["CI_NODES"].strip(), "environment"
    for mf in makefiles:
        try:
            text = mf.read_text()
        except OSError:
            continue
        v = ci_nodes_from_makefile(text)
        if v:
            return v, str(mf)
    return "", "none"


# ---------------------------------------------------------------------------
# Node probing and capacity
# ---------------------------------------------------------------------------


@dataclasses.dataclass
class Probe:
    ok: bool
    error: str = ""
    goos: str = ""
    ncpu: int = 1
    load1: float = 0.0
    mem_avail_gb: float = -1.0  # -1 = unknown
    tools: frozenset[str] = frozenset()
    ollama_local: bool = False
    ollama_ps: tuple[str, str] = ("", "")
    ollama_cpu: float = 0.0


def ollama_busy(probe: Probe) -> bool:
    """True when a LOCAL Ollama is serving right now.

    A loaded model alone means nothing: keep-alive keeps it resident for hours.
    Busy means the model's expiry moved between two samples a few seconds apart
    (a request finished in between) or the Ollama processes are burning CPU.
    A remote Ollama reached through a tunnel on the same port (the prod server
    forwards 11434 to another host) is not this node's load, so it is ignored.
    """
    if not probe.ollama_local:
        return False

    def models(raw: str) -> dict[str, str]:
        try:
            data = json.loads(raw or "{}")
        except json.JSONDecodeError:
            return {}
        return {m.get("name", "?"): m.get("expires_at", "") for m in data.get("models") or []}

    first, second = models(probe.ollama_ps[0]), models(probe.ollama_ps[1])
    if not second:
        return False
    if any(first.get(k) != v for k, v in second.items()):
        return True
    return probe.ollama_cpu >= OLLAMA_BUSY_CPU_PCT


@dataclasses.dataclass
class Capacity:
    slots: int  # workers this run starts on the node
    par: int  # go -p / GOMAXPROCS budget per slot
    gomaxprocs: int | None  # set only on prod nodes
    notes: list[str]
    skip: str = ""  # non-empty = do not use this node


def node_capacity(node: Node, probe: Probe) -> Capacity:
    notes: list[str] = []
    if not probe.ok:
        return Capacity(0, 1, None, notes, skip=f"unreachable: {probe.error}")
    missing = {"go", "make", "git"} - set(probe.tools)
    if missing:
        return Capacity(0, 1, None, notes, skip=f"missing tools: {', '.join(sorted(missing))}")
    slots = node.max_jobs
    ncpu = max(1, probe.ncpu)
    if node.prod:
        limit = ncpu * PROD_MAX_LOAD_FRAC
        if probe.load1 > limit:
            return Capacity(0, 1, None, notes, skip=f"prod load {probe.load1:.1f} > {limit:.1f}")
        if 0 <= probe.mem_avail_gb < PROD_MIN_MEM_GB:
            return Capacity(
                0, 1, None, notes,
                skip=f"prod MemAvailable {probe.mem_avail_gb:.1f}G < {PROD_MIN_MEM_GB:.0f}G",
            )
        par = max(1, (ncpu // 2) // slots)
        notes.append(f"prod: nice 19 + ionice idle, GOMAXPROCS/-p {par} per slot")
        return Capacity(slots, par, par, notes)
    if ollama_busy(probe):
        slots = max(1, slots // 2)
        notes.append("ollama busy: slots halved")
    elif probe.load1 > ncpu * BUSY_LOAD_FRAC:
        slots = max(1, slots // 2)
        notes.append(f"load {probe.load1:.1f} on {ncpu} cpus: slots halved")
    par = max(1, ncpu // max(1, node.max_jobs))
    return Capacity(slots, par, None, notes)


def can_decode(node: Node, probe: Probe) -> bool:
    return (not node.nodecode) and all(t in probe.tools for t in DECODE_TOOLS)


# ---------------------------------------------------------------------------
# Package discovery, decode routing, timings, sharding
# ---------------------------------------------------------------------------


@dataclasses.dataclass(frozen=True)
class Package:
    path: str  # import path
    dir: str
    test_bytes: int
    decode: bool


def test_file_needs_decode(text: str) -> bool:
    return bool(DECODE_RE.search(text))


def scan_package_dir(d: Path) -> tuple[int, bool]:
    """Return (bytes of *_test.go, needs_decode) for one package directory."""
    size = 0
    decode = False
    try:
        entries = list(d.iterdir())
    except OSError:
        return 0, False
    for f in entries:
        if not f.name.endswith("_test.go") or not f.is_file():
            continue
        try:
            text = f.read_text(errors="replace")
        except OSError:
            continue
        size += len(text)
        if not decode and test_file_needs_decode(text):
            decode = True
    return size, decode


def heuristic_seconds(test_bytes: int) -> float:
    """First-run estimate when no timing is recorded: ~1s per 4KB of tests."""
    return 2.0 + test_bytes / 4000.0


class Timings:
    """Measured durations, keyed by GOOS (a Mac's temp FS is ~15x slower for
    internal/server than Linux, so one number per package would misbalance).
    Stored in the git common dir so every worktree shares it and nothing is
    ever committed."""

    def __init__(self, path: Path | None):
        self.path = path
        self.data: dict = {"version": 1, "packages": {}, "legs": {}}
        if path and path.exists():
            try:
                loaded = json.loads(path.read_text())
                if loaded.get("version") == 1:
                    self.data = loaded
            except (OSError, json.JSONDecodeError):
                pass
        self._lock = threading.Lock()

    def pkg(self, goos_list: Iterable[str], pkg: str) -> float | None:
        vals = [self.data["packages"].get(g, {}).get(pkg) for g in goos_list]
        vals = [v for v in vals if v is not None]
        return sum(vals) / len(vals) if vals else None

    def leg(self, goos_list: Iterable[str], leg: str) -> float | None:
        vals = [self.data["legs"].get(g, {}).get(leg) for g in goos_list]
        vals = [v for v in vals if v is not None]
        return sum(vals) / len(vals) if vals else None

    @staticmethod
    def _ewma(old: float | None, new: float) -> float:
        return round(new if old is None else 0.5 * old + 0.5 * new, 2)

    def record_pkg(self, goos: str, pkg: str, secs: float) -> None:
        with self._lock:
            d = self.data["packages"].setdefault(goos, {})
            d[pkg] = self._ewma(d.get(pkg), secs)

    def record_leg(self, goos: str, leg: str, secs: float) -> None:
        with self._lock:
            d = self.data["legs"].setdefault(goos, {})
            d[leg] = self._ewma(d.get(leg), secs)

    def save(self) -> None:
        if not self.path:
            return
        with self._lock:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.path.with_suffix(".tmp")
            tmp.write_text(json.dumps(self.data, indent=1, sort_keys=True))
            tmp.replace(self.path)


def package_weight(p: Package, timings: Timings, goos_list: list[str]) -> float:
    measured = timings.pkg(goos_list, p.path)
    # A per-package constant covers compiling and linking the -race test binary,
    # which the `ok pkg 1.2s` line does not include.
    compile_cost = 3.0 if p.test_bytes else 1.0
    return compile_cost + (measured if measured is not None else heuristic_seconds(p.test_bytes))


def partition_lpt(items: list[tuple[str, float]], k: int) -> list[list[tuple[str, float]]]:
    """Longest-processing-time-first: each item goes to the lightest bin.

    Deterministic (ties broken by name, then bin index). Empty bins are dropped.
    """
    k = max(1, k)
    bins: list[list[tuple[str, float]]] = [[] for _ in range(k)]
    loads = [0.0] * k
    for name, w in sorted(items, key=lambda t: (-t[1], t[0])):
        i = min(range(k), key=lambda j: (loads[j], j))
        bins[i].append((name, w))
        loads[i] += w
    return [b for b in bins if b]


# ---------------------------------------------------------------------------
# Legs
# ---------------------------------------------------------------------------


@dataclasses.dataclass
class Leg:
    name: str
    command: str  # shell command run from the checkout root
    est: float
    needs_decode: bool = False
    local_only: bool = False
    remote_only: bool = False
    packages: tuple[str, ...] = ()
    queued_at: float = 0.0
    attempts: int = 0


# The non-test legs of `make ci`, as make targets. test-short's own `vet`
# prerequisite is its own leg here. coverage-check-short runs afterwards on the
# merged profile.
MAKE_LEGS = (
    ("vet", "make --no-print-directory vet", 120.0),
    ("staticcheck", "make --no-print-directory staticcheck", 300.0),
    ("errcheck-ratchet", "make --no-print-directory lint-errcheck-ratchet", 400.0),
    ("sdkguard", "make --no-print-directory sdkguard", 30.0),
    ("bench-check", "make --no-print-directory bench-check", 120.0),
    ("fmt-check", "make --no-print-directory fmt-check", 10.0),
    (
        "web-test",
        "npm ci --prefix web --no-audit --no-fund && make --no-print-directory web-test",
        240.0,
    ),
)


def go_test_command(packages: Iterable[str], cover_file: str, par: int) -> str:
    pkgs = " ".join(shlex.quote(p) for p in packages)
    return (
        f"go test -short -race -coverprofile={shlex.quote(cover_file)} -covermode=atomic "
        f"-timeout {TEST_SHORT_TIMEOUT} -p {par} {pkgs}"
    )


def build_shards(
    packages: list[Package],
    timings: Timings,
    decode_goos: list[str],
    all_goos: list[str],
    decode_bins: int,
    plain_bins: int,
) -> list[Leg]:
    """Split packages into go-test legs. Decode packages form their own shards
    (weighted with the decode-capable nodes' GOOS timings) so they can only be
    picked up by a node that may decode."""
    decode = [(p.path, package_weight(p, timings, decode_goos or all_goos)) for p in packages if p.decode]
    plain = [(p.path, package_weight(p, timings, all_goos)) for p in packages if not p.decode]
    legs: list[Leg] = []
    for tag, items, k, needs in (("decode", decode, decode_bins, True), ("go", plain, plain_bins, False)):
        for i, b in enumerate(partition_lpt(items, k)):
            legs.append(
                Leg(
                    name=f"test-{tag}-{i + 1:02d}",
                    command="",  # filled per worker (needs -p)
                    est=sum(w for _, w in b),
                    needs_decode=needs,
                    packages=tuple(sorted(n for n, _ in b)),
                )
            )
    return legs


def build_legs(shards: list[Leg], timings: Timings, all_goos: list[str]) -> list[Leg]:
    legs = list(shards)
    for name, cmd, default in MAKE_LEGS:
        legs.append(Leg(name=name, command=cmd, est=timings.leg(all_goos, name) or default))
    # mockery is pinned and installed only on developer machines; the diff it
    # checks is against the local tree anyway.
    legs.append(
        Leg(
            name="mocks-check",
            command="make --no-print-directory mocks-check",
            est=60.0,
            local_only=True,
        )
    )
    return legs


def parse_go_test_line(line: str) -> tuple[str, str, float | None] | None:
    """Parse a `go test` package summary line -> (status, package, seconds).

    Since Go 1.22 a package without test files prints
    `<tab>pkg<tab>coverage: 0.0% of statements` under -coverprofile instead of
    `?  pkg [no test files]`; that is reported with status "?".
    """
    m = GO_NOTEST_COVER_RE.match(line)
    if m:
        return "?", m.group(1), None
    m = GO_RESULT_RE.match(line.strip())
    if not m:
        return None
    status, pkg, secs = m.group(1), m.group(2), m.group(3)
    return status, pkg, (float(secs) if secs else None)


def merge_coverage_profiles(profiles: Iterable[str]) -> str:
    """Concatenate `go test -coverprofile` outputs into one profile.

    Every shard covers a disjoint package set (no -coverpkg), so blocks never
    repeat across shards; if one somehow did, counts are summed so the merge
    is still well-formed for `go tool cover`.
    """
    mode = ""
    order: list[str] = []
    counts: dict[str, int] = {}
    for text in profiles:
        for line in text.splitlines():
            line = line.strip()
            if not line:
                continue
            if line.startswith("mode:"):
                m = line.split(":", 1)[1].strip()
                if mode and m != mode:
                    raise ValueError(f"coverage mode mismatch: {mode} vs {m}")
                mode = m
                continue
            block, _, count = line.rpartition(" ")
            if not block:
                continue
            if block not in counts:
                order.append(block)
                counts[block] = 0
            if mode == "set":
                counts[block] = max(counts[block], int(count))
            else:
                counts[block] += int(count)
    if not mode:
        raise ValueError("no coverage mode line in any profile")
    return "mode: " + mode + "\n" + "".join(f"{b} {counts[b]}\n" for b in order)


# ---------------------------------------------------------------------------
# Scheduling
# ---------------------------------------------------------------------------


@dataclasses.dataclass
class Worker:
    label: str
    node: Node | None  # None = local
    decode_ok: bool
    par: int
    gomaxprocs: int | None = None
    alive: bool = True

    @property
    def is_local(self) -> bool:
        return self.node is None


def pick_leg(
    queue: list[Leg],
    worker: Worker,
    remote_workers: list[Worker],
    now: float,
    local_after: float,
) -> Leg | None:
    """Choose the next leg for `worker`, or None.

    Remote: never a local-only leg, never a decode leg on a nodecode node.
    A decode-capable node takes decode shards first (they cannot go anywhere
    else), then the heaviest remaining leg.
    Local: local-only legs; any leg no live remote worker can run; and, after
    `local_after` seconds in the queue, anything (the Mac as the last resort).
    """
    if worker.is_local:
        live = [w for w in remote_workers if w.alive]
        for leg in queue:
            if leg.local_only:
                return leg
        for leg in queue:
            if leg.remote_only:
                continue
            if not any(w.decode_ok or not leg.needs_decode for w in live):
                return leg
        if local_after >= 0:
            for leg in queue:
                if not leg.remote_only and now - leg.queued_at >= local_after:
                    return leg
        return None
    runnable = [l for l in queue if not l.local_only and (worker.decode_ok or not l.needs_decode)]
    if not runnable:
        return None
    if worker.decode_ok:
        decode = [l for l in runnable if l.needs_decode]
        if decode:
            return max(decode, key=lambda l: l.est)
    return max(runnable, key=lambda l: l.est)


# ---------------------------------------------------------------------------
# Remote shell snippets
# ---------------------------------------------------------------------------

PROBE_SCRIPT = r"""
export PATH="__PREFIX__:$PATH"
os=$(uname -s | tr A-Z a-z)
if [ "$os" = darwin ]; then ncpu=$(sysctl -n hw.ncpu); load=$(sysctl -n vm.loadavg | awk '{print $2}'); mem=-1
else ncpu=$(nproc); load=$(cut -d' ' -f1 /proc/loadavg); mem=$(awk '/MemAvailable/{print $2}' /proc/meminfo); fi
echo "goos=$os"; echo "ncpu=$ncpu"; echo "load1=$load"; echo "memkb=$mem"
tools=""
for t in go make git npm staticcheck golangci-lint mockery ffmpeg ffprobe fpcalc; do command -v "$t" >/dev/null 2>&1 && tools="$tools $t"; done
echo "tools=$tools"
test -d "$HOME/__BARE__" && echo "bare=1" || echo "bare=0"
if pgrep -x ollama >/dev/null 2>&1 || pgrep -f 'ollama serve' >/dev/null 2>&1; then
  echo "ollama_local=1"
  echo "ps1=$(curl -s -m3 127.0.0.1:11434/api/ps | tr -d '\n')"
  sleep 3
  echo "ps2=$(curl -s -m3 127.0.0.1:11434/api/ps | tr -d '\n')"
  echo "ollama_cpu=$(ps -Ao pcpu,comm | awk 'tolower($0) ~ /ollama/ {s+=$1} END {print s+0}')"
else
  echo "ollama_local=0"
fi
"""


def parse_probe(out: str) -> Probe:
    kv: dict[str, str] = {}
    for line in out.splitlines():
        k, sep, v = line.partition("=")
        if sep:
            kv[k.strip()] = v.strip()
    if "goos" not in kv:
        return Probe(ok=False, error="probe printed nothing")
    try:
        memkb = int(kv.get("memkb", "-1") or -1)
    except ValueError:
        memkb = -1
    return Probe(
        ok=True,
        goos=kv["goos"],
        ncpu=int(kv.get("ncpu", "1") or 1),
        load1=float(kv.get("load1", "0") or 0),
        mem_avail_gb=memkb / 1048576.0 if memkb >= 0 else -1.0,
        tools=frozenset(kv.get("tools", "").split()) | ({"bare"} if kv.get("bare") == "1" else set()),
        ollama_local=kv.get("ollama_local") == "1",
        ollama_ps=(kv.get("ps1", ""), kv.get("ps2", "")),
        ollama_cpu=float(kv.get("ollama_cpu", "0") or 0),
    )


def setup_script(job: str, sha: str, nodecode: bool) -> str:
    """Check the commit out into the job dir; on a nodecode node, build a PATH
    with ffmpeg/ffprobe/fpcalc filtered out and save it next to the job."""
    lines = [
        "set -eu",
        f'export PATH="{REMOTE_PATH_PREFIX}:$PATH"',
        f'mkdir -p "$HOME/{REMOTE_ROOT}/jobs" "$HOME/{REMOTE_ROOT}/locks" "$HOME/{REMOTE_ROOT}/cache"',
        f'cd "$HOME/{BARE_REPO}"',
        "git worktree prune",
        f'git worktree add --detach --force "{job}" {sha} >/dev/null',
        f'echo "checked out {sha[:12]} into {job}"',
    ]
    if nodecode:
        tools = " ".join(DECODE_TOOLS)
        lines += [
            f'fdir="{job}/.ci-remote-nodecode-bin"; rm -rf "$fdir"; mkdir -p "$fdir"',
            'out=""; n=0; oldifs=$IFS; IFS=:',
            "for d in $PATH; do",
            '  hit=0; for t in ' + tools + '; do [ -e "$d/$t" ] && hit=1; done',
            '  if [ $hit = 1 ]; then',
            '    n=$((n+1)); nd="$fdir/$n"; mkdir -p "$nd"',
            '    for f in "$d"/*; do b=${f##*/}; case "$b" in ffmpeg|ffprobe|fpcalc) ;; *) ln -s "$f" "$nd/$b" 2>/dev/null || true;; esac; done',
            '    out="$out:$nd"',
            '  else out="$out:$d"; fi',
            "done; IFS=$oldifs",
            'PATH="${out#:}"; export PATH',
            'for t in ' + tools + '; do if command -v "$t" >/dev/null 2>&1; then echo "nodecode PATH still has $t" >&2; exit 1; fi; done',
            f'printf "%s" "$PATH" > "{job}/.ci-remote-nodecode-path"',
            'echo "nodecode: filtered PATH has no ' + "/".join(DECODE_TOOLS) + '"',
        ]
    return "\n".join(lines) + "\n"


def leg_script(
    job: str,
    lock_dir: str,
    max_jobs: int,
    owner: str,
    command: str,
    nodecode: bool,
    prod: bool,
    gomaxprocs: int | None,
    cover_file: str | None,
) -> str:
    """The script one leg runs on a node: take a slot lock (or exit 75), keep
    it alive with a heartbeat, set the CI environment, run the command at low
    priority, and stream the coverage profile back between markers."""
    q = shlex.quote
    s = [
        "set -u",
        f'L="{lock_dir}"; mkdir -p "$L"; got=""',
        f"for i in $(seq 0 {max_jobs - 1}); do",
        '  d="$L/slot-$i"',
        f'  if [ -d "$d" ] && [ -n "$(find "$d" -maxdepth 0 -mmin +{STALE_LOCK_MINUTES} 2>/dev/null)" ]; then',
        '    mv "$d" "$d.stale.$$" 2>/dev/null && rm -rf "$d.stale.$$" && echo "ci-remote: expired stale lock $d" >&2',
        "  fi",
        '  if mkdir "$d" 2>/dev/null; then got="$d"; break; fi',
        "done",
        f'[ -n "$got" ] || exit {LOCK_BUSY_RC}',
        f'printf "%s\\n" {q(owner)} > "$got/owner"',
        # The heartbeat must not hold stdout/stderr: its `sleep` child would keep
        # the ssh session (and our pipe) open for up to a minute after the leg.
        '( while sleep 60; do touch "$got" 2>/dev/null || exit 0; done ) </dev/null >/dev/null 2>&1 & HB=$!',
        "trap 'kill $HB 2>/dev/null; rm -rf \"$got\"' EXIT",
        "trap 'exit 130' INT TERM HUP",
        f'export PATH="{REMOTE_PATH_PREFIX}:$PATH"',
    ]
    if nodecode:
        s += [
            f'PATH="$(cat "{job}/.ci-remote-nodecode-path")"; export PATH',
            "for t in " + " ".join(DECODE_TOOLS) + '; do if command -v "$t" >/dev/null 2>&1; then echo "ci-remote: refusing, $t is on PATH of a nodecode node" >&2; exit '
            + str(NODECODE_REFUSE_RC)
            + "; fi; done",
            "export CI_REMOTE_NODECODE=1",
        ]
    s += [
        f"export GOTOOLCHAIN={GO_TOOLCHAIN}",
        f'export GOCACHE="$HOME/{REMOTE_ROOT}/cache/go-build" GOMODCACHE="$HOME/{REMOTE_ROOT}/cache/gomod"',
        f'export npm_config_cache="$HOME/{REMOTE_ROOT}/cache/npm"',
    ]
    if gomaxprocs:
        s.append(f"export GOMAXPROCS={gomaxprocs}")
    s += [
        f'cd "{job}" || exit 1',
        'PRIO="nice -n 19"',
        'if command -v ionice >/dev/null 2>&1; then PRIO="ionice -c3 nice -n 19"; fi',
    ]
    if prod:
        s.append('echo "ci-remote: prod node, running under: $PRIO GOMAXPROCS=${GOMAXPROCS:-unset}"')
    s += [f"$PRIO bash -c {q(command)}", "rc=$?"]
    if cover_file:
        s += [
            f'if [ -f {q(cover_file)} ]; then echo "{COVERAGE_BEGIN}"; cat {q(cover_file)}; echo "{COVERAGE_END}"; rm -f {q(cover_file)}; fi'
        ]
    s.append("exit $rc")
    return "\n".join(s) + "\n"


def cleanup_script(job: str, run_ref: str, failed: bool) -> str:
    base = f'"$HOME/{REMOTE_ROOT}/jobs"'
    s = ["set -u", f'cd "$HOME/{BARE_REPO}" || exit 0']
    if failed:
        s += [
            f'touch "{job}/.ci-remote-failed" 2>/dev/null',
            # newest first; keep KEEP_FAILED_JOBS, remove the rest
            f"ls -1dt {base}/*/.ci-remote-failed 2>/dev/null | tail -n +{KEEP_FAILED_JOBS + 1} | while read -r m; do",
            '  d=$(dirname "$m"); git worktree remove --force "$d" 2>/dev/null || rm -rf "$d"',
            "done",
        ]
    else:
        s.append(f'git worktree remove --force "{job}" 2>/dev/null || rm -rf "{job}"')
    s += ["git worktree prune", f"git update-ref -d {run_ref} 2>/dev/null", "exit 0"]
    return "\n".join(s) + "\n"


# ---------------------------------------------------------------------------
# Execution
# ---------------------------------------------------------------------------

SSH_OPTS = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=30"]


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, text=True, capture_output=True, **kw)


def ssh_script(node: Node, script: str, timeout: float = 60) -> subprocess.CompletedProcess:
    return run(["ssh", *SSH_OPTS, node.target, "bash", "-s"], input=script, timeout=timeout)


@dataclasses.dataclass
class LegResult:
    leg: str
    where: str
    seconds: float
    rc: int
    packages: tuple[str, ...] = ()
    note: str = ""

    @property
    def ok(self) -> bool:
        return self.rc == 0


class Runner:
    def __init__(self, args, repo: Path, tree: Path, sha: str, out_dir: Path, timings: Timings):
        self.args = args
        self.repo = repo  # the checkout the user ran from
        self.tree = tree  # a local tree whose content is `sha`
        self.sha = sha
        self.out_dir = out_dir
        self.timings = timings
        self.run_id = time.strftime("%Y%m%d%H%M%S") + f"-{os.getpid()}"
        self.run_ref = f"refs/ci-jobs/{self.run_id}"
        self.job = f"$HOME/{REMOTE_ROOT}/jobs/{sha}-{self.run_id}"
        self.print_lock = threading.Lock()
        self.cond = threading.Condition()
        self.queue: list[Leg] = []
        self.in_flight = 0
        self.results: list[LegResult] = []
        self.pkg_results: dict[str, tuple[str, str]] = {}  # pkg -> (status, where)
        self.coverage: list[str] = []
        self.node_goos: dict[str, str] = {}
        self.node_failed: dict[str, bool] = {}
        self.procs: set[subprocess.Popen] = set()

    # -- output -----------------------------------------------------------
    def say(self, msg: str) -> None:
        with self.print_lock:
            print(msg, flush=True)

    # -- one leg ----------------------------------------------------------
    def _stream(self, proc: subprocess.Popen, prefix: str, log, leg: Leg, goos: str) -> list[str]:
        cover: list[str] = []
        in_cover = False
        assert proc.stdout is not None
        for line in proc.stdout:
            line = line.rstrip("\n")
            if line == COVERAGE_BEGIN:
                in_cover = True
                continue
            if line == COVERAGE_END:
                in_cover = False
                continue
            if in_cover:
                cover.append(line)
                continue
            log.write(line + "\n")
            parsed = parse_go_test_line(line)
            if parsed and leg.packages:
                status, pkg, secs = parsed
                with self.cond:
                    self.pkg_results[pkg] = (status, prefix)
                if secs is not None and goos:
                    self.timings.record_pkg(goos, pkg, secs)
            if not self.args.quiet or line.startswith(("FAIL", "---", "panic", "❌")) or "FAIL" in line[:8]:
                self.say(f"[{prefix}] {line}")
        return cover

    def run_leg(self, leg: Leg, w: Worker) -> LegResult:
        where = w.label
        prefix = f"{leg.name}@{where}"
        log_path = self.out_dir / f"{leg.name}.log"
        cover_file = f".ci-remote-{leg.name}.cover.out" if leg.packages else None
        command = leg.command or go_test_command(leg.packages, cover_file or "", w.par)
        start = time.monotonic()
        goos = self.node_goos.get(where, "")
        with open(log_path, "a") as log:
            log.write(f"# {prefix} started {_dt.datetime.now().isoformat(timespec='seconds')}\n# $ {command}\n")
            log.flush()
            if w.is_local:
                full = command
                if cover_file:
                    full = f"{command}; rc=$?; if [ -f {cover_file} ]; then echo {COVERAGE_BEGIN}; cat {cover_file}; echo {COVERAGE_END}; rm -f {cover_file}; fi; exit $rc"
                env = dict(os.environ, GOTOOLCHAIN=GO_TOOLCHAIN)
                proc = subprocess.Popen(
                    ["bash", "-c", full], cwd=self.tree, env=env, text=True,
                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                )
            else:
                assert w.node is not None
                owner = f"{os.environ.get('USER', '?')}@{os.uname().nodename} pid={os.getpid()} run={self.run_id} leg={leg.name}"
                script = leg_script(
                    self.job, f"$HOME/{REMOTE_ROOT}/locks/slots", w.node.max_jobs, owner, command,
                    w.node.nodecode, w.node.prod, w.gomaxprocs, cover_file,
                )
                proc = subprocess.Popen(
                    ["ssh", *SSH_OPTS, w.node.target, "bash", "-s"], text=True,
                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                )
                assert proc.stdin is not None
                proc.stdin.write(script)
                proc.stdin.close()
            with self.cond:
                self.procs.add(proc)
            timer = threading.Timer(LEG_TIMEOUT_S, proc.kill)
            timer.start()
            try:
                cover = self._stream(proc, prefix, log, leg, goos)
                rc = proc.wait()
            finally:
                timer.cancel()
                with self.cond:
                    self.procs.discard(proc)
            secs = time.monotonic() - start
            log.write(f"# {prefix} exit {rc} after {secs:.1f}s\n")
        if cover:
            with self.cond:
                self.coverage.append("\n".join(cover) + "\n")
        return LegResult(leg.name, where, secs, rc, leg.packages)

    # -- worker loop ------------------------------------------------------
    def worker_loop(self, w: Worker, remote_workers: list[Worker]) -> None:
        backoff = 5.0
        while True:
            with self.cond:
                while True:
                    if not self.queue and self.in_flight == 0:
                        self.cond.notify_all()
                        return
                    if not w.alive:
                        return
                    leg = pick_leg(self.queue, w, remote_workers, time.monotonic(), self.args.local_after)
                    if leg:
                        self.queue.remove(leg)
                        self.in_flight += 1
                        break
                    self.cond.wait(timeout=5)
            try:
                res = self.run_leg(leg, w)
            except Exception as exc:  # noqa: BLE001 - report and keep going
                res = LegResult(leg.name, w.label, 0.0, 1, leg.packages, note=f"runner error: {exc}")
            requeue_after = 0.0
            with self.cond:
                self.in_flight -= 1
                if not w.is_local and res.rc == LOCK_BUSY_RC:
                    self.say(f"[{leg.name}@{w.label}] all {w.node.max_jobs} slots held by other runs; requeued")
                    leg.queued_at = leg.queued_at or time.monotonic()
                    self.queue.append(leg)
                    requeue_after = backoff
                    backoff = min(backoff * 2, 60.0)
                elif not w.is_local and res.rc == 255 and leg.attempts < 2:
                    leg.attempts += 1
                    w.alive = False
                    self.say(f"[{leg.name}@{w.label}] ssh failed (rc 255); worker retired, leg requeued")
                    self.queue.append(leg)
                else:
                    backoff = 5.0
                    self.results.append(res)
                    if res.ok and not leg.packages:
                        goos = self.node_goos.get(w.label, "local")
                        self.timings.record_leg(goos, leg.name, res.seconds)
                    if not res.ok:
                        self.node_failed[w.label] = True
                    mark = "PASS" if res.ok else f"FAIL (rc {res.rc})"
                    self.say(f"==> {leg.name} on {w.label}: {mark} in {res.seconds:.0f}s")
                self.cond.notify_all()
            if requeue_after:
                time.sleep(requeue_after)

    def kill_all(self) -> None:
        with self.cond:
            for p in list(self.procs):
                try:
                    p.kill()
                except OSError:
                    pass


# ---------------------------------------------------------------------------
# Local git helpers
# ---------------------------------------------------------------------------


def git(repo: Path, *args: str, env: dict | None = None, check: bool = True) -> str:
    p = subprocess.run(["git", "-C", str(repo), *args], text=True, capture_output=True, env=env)
    if check and p.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {p.stderr.strip()}")
    return p.stdout.strip()


def snapshot_dirty_tree(repo: Path) -> str:
    """Commit the working tree (tracked + untracked, respecting .gitignore) to
    a commit object parented on HEAD, without moving any ref or touching the
    real index."""
    with tempfile.TemporaryDirectory() as td:
        env = dict(os.environ, GIT_INDEX_FILE=str(Path(td) / "index"))
        git(repo, "read-tree", "HEAD", env=env)
        git(repo, "add", "-A", env=env)
        tree = git(repo, "write-tree", env=env)
    return git(repo, "commit-tree", tree, "-p", "HEAD", "-m", "ci-remote: dirty snapshot")


def list_packages(tree: Path) -> list[Package]:
    p = subprocess.run(
        ["go", "list", "-f", "{{.ImportPath}}\t{{.Dir}}", "./..."],
        cwd=tree, text=True, capture_output=True, env=dict(os.environ, GOTOOLCHAIN=GO_TOOLCHAIN),
    )
    if p.returncode != 0:
        raise RuntimeError(f"go list ./... failed:\n{p.stderr}")
    pkgs = []
    for line in p.stdout.splitlines():
        path, _, d = line.partition("\t")
        if not path or "/node_modules/" in d:
            continue
        size, decode = scan_package_dir(Path(d))
        pkgs.append(Package(path, d, size, decode))
    return pkgs


def primary_checkout(repo: Path) -> Path | None:
    common = git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir", check=False)
    return Path(common).parent if common else None


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def fmt_table(rows: list[tuple[str, ...]], header: tuple[str, ...]) -> str:
    widths = [max(len(str(r[i])) for r in [header, *rows]) for i in range(len(header))]
    line = lambda r: "  ".join(str(c).ljust(widths[i]) for i, c in enumerate(r))  # noqa: E731
    return "\n".join([line(header), line(tuple("-" * w for w in widths)), *(line(r) for r in rows)])


def local_make_ci(tree: Path, reason: str) -> int:
    print(f"ci-remote: {reason}; falling back to local `make ci`", flush=True)
    return subprocess.call(["make", "ci"], cwd=tree)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--allow-dirty", action="store_true", help="snapshot uncommitted changes into a temp commit")
    ap.add_argument("--ref", help="test this commit-ish instead of HEAD (checked out to a temp tree for local legs)")
    ap.add_argument("--nodes", help="override CI_NODES")
    ap.add_argument("--dry-run", action="store_true", help="probe and plan, run nothing")
    ap.add_argument("--quiet", action="store_true", help="stream only failure lines (full logs still saved)")
    ap.add_argument(
        "--local-after", type=float, default=-1,
        help="let this machine take any leg queued this many seconds (default: never; "
        "it still takes mocks-check and anything no node can run)",
    )
    ap.add_argument("--no-fallback", action="store_true", help="fail instead of running local make ci")
    ap.add_argument("--keep-jobs", action="store_true", help="leave remote job dirs in place")
    args = ap.parse_args(argv)

    repo = Path(git(Path.cwd(), "rev-parse", "--show-toplevel"))
    primary = primary_checkout(repo)

    # --- commit ---------------------------------------------------------
    temp_tree: Path | None = None
    if args.ref:
        sha = git(repo, "rev-parse", "--verify", f"{args.ref}^{{commit}}")
    else:
        dirty = git(repo, "status", "--porcelain")
        if dirty and not args.allow_dirty:
            print("ci-remote: working tree is dirty; commit first or pass --allow-dirty", file=sys.stderr)
            return 2
        sha = snapshot_dirty_tree(repo) if dirty else git(repo, "rev-parse", "HEAD")
    head = git(repo, "rev-parse", "HEAD")
    tree = repo
    if args.ref and sha != head:
        temp_tree = Path(tempfile.mkdtemp(prefix="ci-remote-tree-"))
        git(repo, "worktree", "add", "--detach", "--force", str(temp_tree), sha)
        tree = temp_tree
    out_dir = repo / ".ci-remote" / sha
    out_dir.mkdir(parents=True, exist_ok=True)
    for old in out_dir.glob("*.log"):
        old.unlink()

    try:
        return _main_inner(args, repo, primary, tree, sha, out_dir)
    finally:
        if temp_tree:
            git(repo, "worktree", "remove", "--force", str(temp_tree), check=False)
            shutil.rmtree(temp_tree, ignore_errors=True)


def _main_inner(args, repo: Path, primary: Path | None, tree: Path, sha: str, out_dir: Path) -> int:
    makefiles = [repo / "Makefile.local"] + ([primary / "Makefile.local"] if primary else [])
    env = dict(os.environ)
    if args.nodes is not None:
        env["CI_NODES"] = args.nodes
    spec, source = find_ci_nodes(env, makefiles)
    try:
        nodes = parse_ci_nodes(spec)
    except ValueError as exc:
        print(f"ci-remote: {exc}", file=sys.stderr)
        return 2
    print(f"ci-remote: commit {sha[:12]}; {len(nodes)} node(s) from {source}", flush=True)
    if not nodes:
        return 1 if args.no_fallback else local_make_ci(tree, "CI_NODES is empty")

    # --- probe ------------------------------------------------------------
    probes: dict[Node, Probe] = {}

    def probe(n: Node) -> None:
        script = PROBE_SCRIPT.replace("__PREFIX__", REMOTE_PATH_PREFIX).replace("__BARE__", BARE_REPO)
        try:
            p = ssh_script(n, script, timeout=30)
            probes[n] = parse_probe(p.stdout) if p.returncode == 0 else Probe(False, error=(p.stderr.strip() or f"rc {p.returncode}")[:200])
        except subprocess.TimeoutExpired:
            probes[n] = Probe(False, error="ssh timed out")

    threads = [threading.Thread(target=probe, args=(n,)) for n in nodes]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    caps: dict[Node, Capacity] = {}
    rows = []
    for n in nodes:
        pr = probes[n]
        cap = node_capacity(n, pr)
        if not cap.skip and "bare" not in pr.tools:
            cap = Capacity(0, 1, None, cap.notes, skip=f"no ~/{BARE_REPO} (see docs/process/ci-remote.md)")
        caps[n] = cap
        flags = ",".join(f for f, on in (("nodecode", n.nodecode), ("prod", n.prod)) if on) or "-"
        rows.append((
            n.name, flags, pr.goos or "?", str(pr.ncpu if pr.ok else "?"),
            f"{pr.load1:.1f}" if pr.ok else "?", "yes" if pr.ok and can_decode(n, pr) else "no",
            str(cap.slots), cap.skip or "; ".join(cap.notes) or "ok",
        ))
    print(fmt_table(rows, ("node", "flags", "goos", "cpus", "load1", "decode", "slots", "status")), flush=True)
    usable = [n for n in nodes if caps[n].slots > 0]
    if not usable:
        return 1 if args.no_fallback else local_make_ci(tree, "no node is usable")

    # --- plan -------------------------------------------------------------
    packages = list_packages(tree)
    decode_nodes = [n for n in usable if can_decode(n, probes[n])]
    all_goos = sorted({probes[n].goos for n in usable})
    decode_goos = sorted({probes[n].goos for n in decode_nodes}) or ["darwin" if sys.platform == "darwin" else "linux"]
    total_slots = sum(caps[n].slots for n in usable)
    decode_slots = sum(caps[n].slots for n in decode_nodes) or 1
    timings_path = (primary / ".git" / "ci-remote" / "timings.json") if primary else None
    runner_timings = Timings(timings_path) if timings_path else Timings(None)
    shards = build_shards(packages, runner_timings, decode_goos, all_goos, decode_slots, total_slots)
    legs = build_legs(shards, runner_timings, all_goos)

    dec = sorted(p.path for p in packages if p.decode)
    print(f"ci-remote: {len(packages)} packages, {len(dec)} need a decoder (never sent to a nodecode node):", flush=True)
    for p in dec:
        print(f"    {p}", flush=True)
    if not decode_nodes:
        print("ci-remote: no decode-capable node; decode shards run on this machine", flush=True)
    print(fmt_table(
        [(l.name, f"{l.est:.0f}s", str(len(l.packages)) if l.packages else "-",
          "decode-only" if l.needs_decode else ("local" if l.local_only else "any")) for l in legs],
        ("leg", "est", "pkgs", "placement"),
    ), flush=True)
    (out_dir / "plan.json").write_text(json.dumps({
        "sha": sha, "decode_packages": dec,
        "legs": [{"name": l.name, "est": l.est, "packages": list(l.packages), "needs_decode": l.needs_decode} for l in legs],
    }, indent=1))
    if args.dry_run:
        return 0

    runner = Runner(args, repo, tree, sha, out_dir, runner_timings)

    # --- push + checkout ----------------------------------------------------
    ready: list[Node] = []

    def prepare(n: Node) -> None:
        env = dict(os.environ, GIT_SSH_COMMAND="ssh " + " ".join(SSH_OPTS))
        t0 = time.monotonic()
        p = subprocess.run(
            ["git", "-C", str(repo), "push", "--no-verify", "--quiet", f"{n.target}:{BARE_REPO}",
             f"+{sha}:{runner.run_ref}"],
            text=True, capture_output=True, env=env, timeout=1800,
        )
        if p.returncode != 0:
            runner.say(f"ci-remote: push to {n.name} failed: {p.stderr.strip()[:300]}")
            return
        s = ssh_script(n, setup_script(runner.job, sha, n.nodecode), timeout=600)
        if s.returncode != 0:
            runner.say(f"ci-remote: checkout on {n.name} failed: {(s.stderr or s.stdout).strip()[:300]}")
            return
        runner.say(f"ci-remote: {n.name} ready in {time.monotonic() - t0:.0f}s ({s.stdout.strip().splitlines()[-1]})")
        with runner.cond:
            ready.append(n)
            runner.node_goos[n.name] = probes[n].goos

    threads = [threading.Thread(target=prepare, args=(n,)) for n in usable]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    if not ready:
        return 1 if args.no_fallback else local_make_ci(tree, "no node could be prepared")
    runner.node_goos["local"] = "darwin" if sys.platform == "darwin" else "linux"

    remote_workers = [
        Worker(f"{n.name}" + (f"#{i + 1}" if caps[n].slots > 1 else ""), n,
               can_decode(n, probes[n]), caps[n].par, caps[n].gomaxprocs)
        for n in ready for i in range(caps[n].slots)
    ]
    for w in remote_workers:
        runner.node_goos[w.label] = probes[w.node].goos
    local = Worker("local", None, True, max(1, (os.cpu_count() or 2) // 2))
    now = time.monotonic()
    for l in legs:
        l.queued_at = now
    runner.queue = sorted(legs, key=lambda l: -l.est)

    wall0 = time.monotonic()
    threads = [threading.Thread(target=runner.worker_loop, args=(w, remote_workers), daemon=True)
               for w in [*remote_workers, local]]
    try:
        for t in threads:
            t.start()
        for t in threads:
            while t.is_alive():
                t.join(timeout=1)
    except KeyboardInterrupt:
        print("\nci-remote: interrupted; killing legs", file=sys.stderr)
        runner.kill_all()
        return 130

    # --- coverage -----------------------------------------------------------
    test_legs = [r for r in runner.results if r.packages]
    listed = {p.path for p in packages}
    seen = set(runner.pkg_results)
    missing = sorted(listed - seen)
    all_tests_ok = all(r.ok for r in test_legs) and len(test_legs) == len(shards)
    cov_note = ""
    if missing and all_tests_ok:
        cov_note = f"{len(missing)} package(s) produced no result line: {', '.join(missing[:5])}"
    if all_tests_ok and runner.coverage:
        merged = merge_coverage_profiles(runner.coverage)
        (out_dir / "coverage.out").write_text(merged)
        (tree / "coverage.out").write_text(merged)
        cleg = Leg("coverage-check-short", "make --no-print-directory coverage-check-short", 5.0)
        runner.results.append(runner.run_leg(cleg, local))
    else:
        runner.results.append(LegResult("coverage-check-short", "-", 0.0, 1, note="skipped: a test shard failed"))
    if cov_note:
        runner.results.append(LegResult("package-census", "-", 0.0, 1, note=cov_note))
    wall = time.monotonic() - wall0
    runner.timings.save()

    # --- cleanup ------------------------------------------------------------
    for n in ready:
        failed = any(not r.ok and r.where.split("#")[0] == n.name for r in runner.results)
        if args.keep_jobs:
            continue
        try:
            ssh_script(n, cleanup_script(runner.job, runner.run_ref, failed), timeout=300)
        except subprocess.TimeoutExpired:
            print(f"ci-remote: cleanup on {n.name} timed out", file=sys.stderr)

    # --- summary ------------------------------------------------------------
    order = {l.name: i for i, l in enumerate(legs)}
    rows = [
        (r.leg, r.where, f"{r.seconds:.0f}s", "PASS" if r.ok else (r.note or f"FAIL rc={r.rc}"))
        for r in sorted(runner.results, key=lambda r: order.get(r.leg, 999))
    ]
    table = fmt_table(rows, ("leg", "node", "duration", "result"))
    failed = [r for r in runner.results if not r.ok]
    placement = {}
    for pkg, (status, where) in sorted(runner.pkg_results.items()):
        placement[pkg] = {"status": status, "where": where}
    (out_dir / "summary.json").write_text(json.dumps({
        "sha": sha, "wall_seconds": round(wall, 1),
        "results": [dataclasses.asdict(r) for r in runner.results],
        "packages": placement,
    }, indent=1))
    print("\n" + table)
    print(f"\nci-remote: {len(runner.results) - len(failed)}/{len(runner.results)} legs passed "
          f"in {wall:.0f}s wall; logs in {out_dir}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
