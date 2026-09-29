#!/usr/bin/env python3
# file: scripts/ci/go_test_shards.py
# version: 1.1.0
# guid: 3b8e6d21-7c4f-4a90-b1e5-9f2d0c7a6e48
# last-edited: 2026-09-29
"""Run Go test packages split into shards, then merge their coverage.

`go test` runs packages in parallel but each package's tests in one process,
one after another unless a test calls t.Parallel(). A CI workflow therefore
takes as long as its slowest package (abs, server, scanner and database run
230-620 s each). This script lists a package's top-level tests, deals them into
N shards balanced by recorded durations, and runs every shard as its own
`go test -run '^(...)$'` process. Separate processes keep the tests' shared
globals (config.AppConfig and friends) as isolated as they are today.

Coverage: each shard writes its own profile; the profiles are merged block by
block (counts summed, as `-covermode=atomic` means) into one file, so a block
covered by any shard counts once and the CI-COVERAGE sum stays exact.

Usage:
  go_test_shards.py --out cover.out [--jobs J] [--timings FILE] \\
      --pkg ./internal/server:4 --pkg ./internal/scanner:3 -- -short -race ...

Every argument after `--` goes to each `go test` call. Exits non-zero if any
shard fails or if a listed test was not assigned to exactly one shard.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

# `go test -list` prints test, fuzz and example names plus an "ok" summary.
# Benchmarks only run with -bench, so they are left out of the -run patterns.
_NAME_RE = re.compile(r"^(Test|Fuzz|Example)[A-Za-z0-9_]*$")


def list_tests(pkg: str, go_args: list[str]) -> list[str]:
    # -list compiles the package with the same flags the shards use, so the
    # shards reuse its compiled objects from the build cache (each still links
    # its own test binary).
    # Only flags that change the compiled binary; test flags such as -timeout
    # may take their value as a separate argument, which -list would misread.
    build_flags = [a for a in go_args if a in ("-race", "-cover", "-trimpath") or a.startswith(("-covermode=", "-tags=", "-gcflags="))]
    res = subprocess.run(["go", "test", *build_flags, "-list", ".", pkg], capture_output=True, text=True)
    if res.returncode != 0:
        # A compile error in the package's tests surfaces here first; show it,
        # as a plain `go test` would have.
        print(res.stdout + res.stderr, flush=True)
        raise SystemExit(f"::error::go test -list {pkg} failed (exit {res.returncode})")
    out = res.stdout
    return sorted({ln.strip() for ln in out.splitlines() if _NAME_RE.match(ln.strip())})


def balance(names: list[str], shards: int, timings: dict[str, float]) -> list[list[str]]:
    """Longest-processing-time-first: each test goes to the lightest shard."""
    known = [timings[n] for n in names if n in timings]
    default = sorted(known)[len(known) // 2] if known else 1.0
    buckets: list[tuple[float, list[str]]] = [(0.0, []) for _ in range(shards)]
    for n in sorted(names, key=lambda n: (-timings.get(n, default), n)):
        i = min(range(shards), key=lambda k: buckets[k][0])
        load, members = buckets[i]
        buckets[i] = (load + timings.get(n, default), members + [n])
    return [members for _, members in buckets if members]


def merge_profiles(paths: list[str], out: str) -> None:
    mode, blocks, order = None, {}, []
    for p in paths:
        if not os.path.exists(p):
            continue
        with open(p, encoding="utf-8") as fh:
            for i, line in enumerate(fh):
                line = line.rstrip("\n")
                if i == 0:
                    if not line.startswith("mode:"):
                        raise SystemExit(f"{p}: not a coverage profile")
                    if mode not in (None, line):
                        raise SystemExit(f"{p}: {line!r} differs from {mode!r}")
                    mode = line
                    continue
                if not line:
                    continue
                key, count = line.rsplit(" ", 1)
                if key not in blocks:
                    order.append(key)
                    blocks[key] = 0
                blocks[key] += int(count)
    with open(out, "w", encoding="utf-8") as fh:
        fh.write((mode or "mode: atomic") + "\n")
        for key in order:
            fh.write(f"{key} {blocks[key]}\n")


def main() -> int:
    ap = argparse.ArgumentParser(description="Run Go test packages split into shards, then merge their coverage.")
    ap.add_argument("--pkg", action="append", required=True, metavar="PKG:N",
                    help="package and its shard count (repeatable)")
    ap.add_argument("--out", required=True, help="merged coverage profile")
    ap.add_argument("--jobs", type=int, default=0, help="concurrent shards (default: all)")
    ap.add_argument("--timings", default=os.path.join(os.path.dirname(__file__), "go_test_timings.json"))
    ap.add_argument("go_args", nargs=argparse.REMAINDER)
    a = ap.parse_args()
    go_args = a.go_args[1:] if a.go_args[:1] == ["--"] else a.go_args
    try:
        with open(a.timings, encoding="utf-8") as fh:
            timings = json.load(fh)
    except FileNotFoundError:
        timings = {}

    plan: list[tuple[str, int, list[str]]] = []
    for spec in a.pkg:
        pkg, _, n = spec.rpartition(":")
        if not pkg or not n.isdigit() or int(n) < 1:
            ap.error(f"--pkg {spec!r}: want PKG:N with N >= 1")
        names = list_tests(pkg, go_args)
        key = pkg.removeprefix("./")
        shards = balance(names, int(n), timings.get(key, {}))
        assigned = sorted(t for s in shards for t in s)
        if assigned != names:
            print(f"::error::{pkg}: shards do not cover the listed tests exactly once", flush=True)
            return 1
        for i, s in enumerate(shards):
            plan.append((pkg, i, s))
        est = [sum(timings.get(key, {}).get(t, 0.0) for t in s) for s in shards]
        print(f"{pkg}: {len(names)} tests in {len(shards)} shards, est. {', '.join(f'{e:.0f}s' for e in est)}", flush=True)

    tmp = tempfile.mkdtemp(prefix="go-shards-")

    def run(item: tuple[str, int, list[str]]) -> tuple[str, int, int, float, str, str]:
        pkg, i, names = item
        prof = os.path.join(tmp, f"{re.sub(r'[^A-Za-z0-9]+', '_', pkg)}_{i}.out")
        pattern = "^(" + "|".join(names) + ")$"
        start = time.monotonic()
        res = subprocess.run(
            ["go", "test", *go_args, f"-coverprofile={prof}", "-run", pattern, pkg],
            capture_output=True, text=True,
        )
        return pkg, i, res.returncode, time.monotonic() - start, res.stdout + res.stderr, prof

    # Longest shards first so the slowest one starts immediately.
    order = sorted(plan, key=lambda it: -sum(timings.get(it[0].removeprefix("./"), {}).get(t, 0.0) for t in it[2]))
    jobs = a.jobs if a.jobs > 0 else len(order)
    failed, profiles = 0, []
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as ex:
            # Report each shard as it finishes, so a fast failure is not held
            # behind slower shards.
            for fut in concurrent.futures.as_completed([ex.submit(run, it) for it in order]):
                pkg, i, rc, secs, output, prof = fut.result()
                profiles.append(prof)
                # A shard whose names matched nothing means -list saw a
                # different test set than the shard compiled (a build flag
                # this script did not forward): those tests would silently not
                # run, so treat it as a failure.
                empty = "[no tests to run]" in output
                status = "ok" if rc == 0 and not empty else "FAIL"
                print(f"--- shard {pkg}#{i} {status} in {secs:.0f}s", flush=True)
                if status == "FAIL":
                    failed += 1
                    print(output, flush=True)
                    if empty:
                        print(f"::error::shard {pkg}#{i} ran no tests; -list and the shard disagree", flush=True)
                else:
                    for ln in output.splitlines():
                        if ln.startswith(("ok", "---", "FAIL")):
                            print("    " + ln, flush=True)
        merge_profiles(sorted(profiles), a.out)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
