#!/usr/bin/env python3
# file: scripts/ci/fixture_test_packages.py
# version: 1.1.0
# guid: 92f31101-2399-4ce9-9b83-0af332666123
# last-edited: 2026-10-05
"""List the Go packages whose tests skip under -short, for `make test-fixtures`.

Every PR-gating Go test run in this repo passes -short (`make test-short` in
ci.yml, all three Woodpecker test workflows). A test that calls
`testing.Short()` and skips, directly or through a shared fixture such as
internal/versionprimary/vptest (whose `New` skips under -short), therefore
never gates a merge. This script finds every such package so a separate job
can run exactly those packages WITHOUT -short.

A package is selected when either:
  * one of its _test.go files calls testing.Short(), or
  * its tests import a fixture helper: a package whose non-test .go files
    call testing.Short() (such as vptest), or a package that reaches one
    through its non-test imports, at any depth (a wrapper around vptest is a
    helper too). Helpers are found generically, so the next one is picked up
    without editing this script.

Discovery goes through `go list ./...`, which honors go.mod's `ignore ./web`,
so Go files under web/node_modules are never picked up.

Usage:
  fixture_test_packages.py                 # every selected package, one per line
  fixture_test_packages.py --shard 2/3     # shard 2 of 3 (1-based)

Shards are balanced by the per-package durations in _WEIGHTS (measured on the
nightly full run); unknown packages weigh _DEFAULT_WEIGHT. Exits non-zero if
nothing is selected, if a known helper in _REQUIRED_HELPERS is not detected as
one, or if a shard comes out empty, so a broken discovery can never turn into a
`go test` with no packages (which would test only the module root and pass) or
into a list that silently dropped every vptest-based package.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Callable, NamedTuple

MODULE = "github.com/falkcorp/audiobook-organizer"

# Seconds per package under `go test -race` without -short, from the Nightly
# Full CI run 37300110319 (2026-10-05, GitHub ubuntu-latest). Only used to
# balance shards; a stale value costs balance, never coverage.
_WEIGHTS = {
    "internal/plugins/maintenance": 400,
    "internal/database": 269,
    "internal/scanner": 192,
    "internal/server": 160,
    "internal/operations/registry": 69,
    "internal/playlist": 67,
    "internal/organizer": 48,
    "internal/audiobooks": 42,
    "internal/server/handlers": 36,
    "internal/dedup": 24,
    "internal/maintenance/jobs": 13,
    "internal/merge": 13,
    "internal/versions": 11,
    "internal/itunes/service": 9,
    "internal/plugins/dedup": 8,
    "internal/itunes": 5,
    "internal/batch": 3,
    "internal/realtime": 3,
    "internal/reconcile": 2,
    "internal/versionprimary": 2,
    "internal/plugins": 1,
    "internal/server/handlers/duplicates": 1,
}
_DEFAULT_WEIGHT = 10

# Helpers that must be detected. vptest.New is the fixture the merge, undo and
# redirect state-machine tests are built on; if the detector stops seeing it
# (a rename of testing.Short, a refactor that moves the skip), the selected
# list would still be non-empty -- the direct testing.Short() callers keep it
# populated -- while every vptest-based package silently fell out of the gate.
_REQUIRED_HELPERS = ("internal/versionprimary/vptest",)

_REPO_ROOT = Path(__file__).resolve().parents[2]

_SHORT_RE = re.compile(r"\btesting\.Short\(\)")


def _calls_short(directory: str, files: list[str]) -> bool:
    for name in files:
        try:
            if _SHORT_RE.search((Path(directory) / name).read_text(encoding="utf-8")):
                return True
        except OSError as e:
            raise SystemExit(f"fixture_test_packages: cannot read {directory}/{name}: {e}")
    return False


def _load_packages(root: Path) -> list[dict]:
    res = subprocess.run(["go", "list", "-json", "./..."], capture_output=True, text=True, cwd=root)
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


def find_helpers(pkgs: list[dict], calls_short: Callable[[str, list[str]], bool] = _calls_short) -> set[str]:
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


def select(pkgs: list[dict], helpers: set[str], calls_short: Callable[[str, list[str]], bool] = _calls_short) -> list[str]:
    selected = []
    for p in pkgs:
        test_files = p.get("TestGoFiles", []) + p.get("XTestGoFiles", [])
        if not test_files:
            continue
        imports = set(p.get("TestImports", [])) | set(p.get("XTestImports", []))
        if calls_short(p["Dir"], test_files) or imports & helpers:
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


def shard(pkgs: list[str], index: int, count: int) -> list[str]:
    """Longest-first greedy split; deterministic for a given package list."""
    def weight(p: str) -> int:
        return _WEIGHTS.get(p.removeprefix(MODULE + "/"), _DEFAULT_WEIGHT)

    bins: list[list[str]] = [[] for _ in range(count)]
    loads = [0] * count
    for p in sorted(pkgs, key=lambda p: (-weight(p), p)):
        b = min(range(count), key=lambda k: (loads[k], k))
        bins[b].append(p)
        loads[b] += weight(p)
    assigned = sorted(p for b in bins for p in b)
    if assigned != sorted(pkgs):
        raise SystemExit("fixture_test_packages: shard assignment lost or duplicated a package")
    return sorted(bins[index - 1])


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--shard", help="i/n, 1-based: print only shard i of n")
    args = ap.parse_args()

    pkgs = discover().selected
    if args.shard:
        m = re.fullmatch(r"(\d+)/(\d+)", args.shard)
        if not m or not 1 <= int(m.group(1)) <= int(m.group(2)):
            raise SystemExit(f"fixture_test_packages: bad --shard {args.shard!r}; want i/n with 1 <= i <= n")
        pkgs = shard(pkgs, int(m.group(1)), int(m.group(2)))
        if not pkgs:
            raise SystemExit(f"fixture_test_packages: shard {args.shard} is empty; use fewer shards")
    print("\n".join(pkgs))


if __name__ == "__main__":
    main()
