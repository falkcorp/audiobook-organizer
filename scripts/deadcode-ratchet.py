#!/usr/bin/env python3
# file: scripts/deadcode-ratchet.py
# version: 1.0.0
# guid: 5d0c7f2e-8a41-4b6d-9e13-c7a2f4b86d90
# last-edited: 2026-10-11
"""Ratchet on production functions unreachable from every `main`.

Runs golang.org/x/tools/cmd/deadcode (pinned) and compares the set of
unreachable function identifiers against scripts/deadcode-baseline.txt.

One-way, like scripts/check-errcheck-ratchet.sh (owner decision D47): a NEW
identifier fails; an identifier that disappeared prints a ::notice:: and exits
0. Humans never lower the baseline by hand; the scheduled bot (07-G4) runs
`--write-baseline` and opens the PR that does.

Identifiers, not line numbers, are stored (`<package dir>.<Name>`, methods as
`<package dir>.<Type>.<Name>`) so unrelated edits that shift a function do not
change the baseline.

Why GOOS/GOARCH are pinned to linux/amd64: build-tagged files
(`*_linux.go`, `*_darwin.go`) are only analysed under the GOOS that selects
them, so a run on a developer Mac and a run on the ubuntu CI runner would see
different sets. Pinning makes every runner report what CI reports. The tool
binary itself is built for the host (a cross-GOOS `go run` cannot execute).
The pin is v0.51.0, the version the baseline was measured with: a version
bump can change the set on its own and trip the ratchet as if code had changed.

Excluded from the set: pkg/ (public SDK, exported on purpose),
internal/writeback/ (the iTunes writer), test files, test helper packages
(testutil, *test, conformance, rapidgen), mock_store.go, and any name
containing `ForTest` (test seams).
"""
from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

DEADCODE_PKG = "golang.org/x/tools/cmd/deadcode@v0.51.0"
LINE_RE = re.compile(r"^(?P<file>[^:\s]+):\d+:\d+: unreachable func: (?P<name>\S+)\s*$")
EXCLUDED_DIR_PREFIXES = ("pkg/", "internal/writeback/")
EXCLUDED_PKG_NAMES = {"testutil", "conformance", "rapidgen"}


def repo_root() -> Path:
    out = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True
    )
    return Path(out.stdout.strip())


def excluded(file: str, name: str) -> bool:
    if file.endswith("_test.go") or file.endswith("mock_store.go"):
        return True
    if "ForTest" in name:
        return True
    if file.startswith(EXCLUDED_DIR_PREFIXES):
        return True
    for part in file.split("/")[:-1]:
        if part in EXCLUDED_PKG_NAMES or part.endswith("test"):
            return True
    return False


def parse(output: str) -> set[str]:
    """Turn deadcode's flat `file:line:col: unreachable func: Name` lines
    into a set of `<package dir>.<Name>` identifiers, filters applied."""
    found: set[str] = set()
    for line in output.splitlines():
        m = LINE_RE.match(line)
        if not m:
            continue
        file, name = m.group("file"), m.group("name")
        if excluded(file, name):
            continue
        pkg_dir = file.rsplit("/", 1)[0] if "/" in file else "."
        found.add(f"{pkg_dir}.{name}")
    return found


def read_baseline(path: Path) -> set[str]:
    return {
        ln.strip()
        for ln in path.read_text().splitlines()
        if ln.strip() and not ln.lstrip().startswith("#")
    }


def compare(current: set[str], baseline: set[str]) -> tuple[list[str], list[str]]:
    return sorted(current - baseline), sorted(baseline - current)


def run_deadcode(root: Path) -> str:
    binary = os.environ.get("DEADCODE_BIN")
    with tempfile.TemporaryDirectory() as tmp:
        if not binary:
            host_env = {k: v for k, v in os.environ.items() if k not in ("GOOS", "GOARCH")}
            host_env["GOBIN"] = tmp
            subprocess.run(["go", "install", DEADCODE_PKG], check=True, cwd=root, env=host_env)
            binary = os.path.join(tmp, "deadcode")
        env = dict(os.environ, GOOS="linux", GOARCH="amd64")
        res = subprocess.run(
            [binary, "./..."], cwd=root, env=env, capture_output=True, text=True
        )
        if res.returncode != 0:
            sys.stderr.write(res.stderr)
            sys.exit(2)
        return res.stdout


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--write-baseline", action="store_true", help="rewrite the baseline (bot only)")
    ap.add_argument("--baseline", default="scripts/deadcode-baseline.txt")
    ap.add_argument("--input", help="read deadcode output from this file instead of running it")
    args = ap.parse_args(argv)

    root = repo_root()
    baseline_path = root / args.baseline
    output = Path(args.input).read_text() if args.input else run_deadcode(root)
    current = parse(output)

    if args.write_baseline:
        baseline_path.write_text("".join(f"{i}\n" for i in sorted(current)))
        print(f"wrote {len(current)} identifiers to {args.baseline}")
        return 0

    baseline = read_baseline(baseline_path)
    new, gone = compare(current, baseline)
    print(f"deadcode: {len(current)} unreachable (baseline {len(baseline)})")
    if gone:
        print(
            f"::notice::deadcode: {len(gone)} identifier(s) no longer unreachable; "
            f"the bot (or `--write-baseline`) lowers the baseline to {len(current)}"
        )
    if new:
        print(f"FAIL: {len(new)} new unreachable function(s) not in {args.baseline}:", file=sys.stderr)
        for ident in new:
            print(f"  {ident}", file=sys.stderr)
        print(
            "Delete the dead code, or make it reachable from a main. "
            "Do not add to the baseline by hand.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
