#!/usr/bin/env python3
# file: scripts/r2corpus/dump_r0_routes.py
# version: 1.0.0
# guid: 5a7e2c91-3b64-4d08-9f1a-8c0d6e4b2a57
# last-edited: 2026-11-13
"""Dump the R0 tag's gin route table into internal/books/rbacreach/routes.json (F4-07).

R0 has no route-listing endpoint, and main's router is not R0's (R1 deleted
``POST /system/reset`` and ``/system/factory-reset`` and rewired ten server
files), so the dump is taken from the tag's own source: a detached worktree of the
tag gets ``r0routes/dump_routes_test.go.txt`` as ``internal/server/r0_route_dump_test.go``
and ``go test -run TestR0RouteDump`` writes the router's routes with the ABS
surface off and on. ``record_r0_reach.py`` probes every dumped route against the
running binary, so a route the source dump names and the binary does not serve
shows up as a NoRoute answer in its ``uniform_not_found`` list.

    python3 scripts/r2corpus/dump_r0_routes.py            # needs go and network-free git
    python3 scripts/r2corpus/dump_r0_routes.py --src DIR  # a checkout of the tag already
"""

from __future__ import annotations

import argparse
import datetime
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

_HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = _HERE.parents[1]
TAG_FILE = REPO_ROOT / "ci" / "r0_release.txt"
OUT = REPO_ROOT / "internal" / "books" / "rbacreach" / "routes.json"
TEST_SRC = _HERE / "r0routes" / "dump_routes_test.go.txt"
GUID = "d41a8b7f-2c65-4e93-8f10-6b9e3a7c5d28"
VERSION = "1.0.0"


def read_tag() -> str:
    lines = [
        ln.strip()
        for ln in TAG_FILE.read_text(encoding="utf-8").splitlines()
        if ln.strip() and not ln.startswith("#")
    ]
    if len(lines) != 1:
        raise SystemExit(f"{TAG_FILE}: want exactly one tag line")
    return lines[0]


def run_dump(src: pathlib.Path, dst: pathlib.Path) -> None:
    shutil.copy(TEST_SRC, src / "internal" / "server" / "r0_route_dump_test.go")
    env = {**os.environ, "R0_ROUTE_DUMP": str(dst)}
    try:
        subprocess.run(
            ["go", "test", "-count=1", "-run", "TestR0RouteDump", "./internal/server/"],
            cwd=src, env=env, check=True,
        )  # fmt: skip
    finally:
        (src / "internal" / "server" / "r0_route_dump_test.go").unlink(missing_ok=True)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--src", help="a checkout of the R0 tag (default: a temporary worktree)")
    ap.add_argument("--out", default=str(OUT))
    args = ap.parse_args(argv)
    tag = read_tag()
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r0routes-"))
    raw = tmp / "dump.json"
    worktree: pathlib.Path | None = None
    try:
        if args.src:
            src = pathlib.Path(args.src)
        else:
            worktree = tmp / "src"
            subprocess.run(
                ["git", "worktree", "add", "--detach", str(worktree), tag],
                cwd=REPO_ROOT, check=True,
            )  # fmt: skip
            src = worktree
        run_dump(src, raw)
        dump = json.loads(raw.read_text(encoding="utf-8"))
    finally:
        if worktree is not None:
            subprocess.run(
                ["git", "worktree", "remove", "--force", str(worktree)], cwd=REPO_ROOT, check=False
            )
        shutil.rmtree(tmp, ignore_errors=True)
    doc = {
        "_header": {
            "file": "internal/books/rbacreach/routes.json",
            "version": VERSION,
            "guid": GUID,
            "last-edited": datetime.date.today().isoformat(),
        },
        "what": "the gin route table of the R0 tag's router (source dump, scripts/r2corpus/r0routes)",
        "r0_tag": tag,
        "route_count": len(dump["routes"]),
        "abs_off_count": dump["abs_off"],
        "abs_on_count": dump["abs_on"],
        "routes": dump["routes"],
    }
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(doc, indent=1) + "\n", encoding="utf-8")
    print(
        f"wrote {doc['route_count']} routes ({dump['abs_off']} ABS off, {dump['abs_on']} ABS on) to {out}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
