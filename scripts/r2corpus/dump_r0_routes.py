#!/usr/bin/env python3
# file: scripts/r2corpus/dump_r0_routes.py
# version: 1.0.0
# guid: 5a7e2c91-3b64-4d08-9f1a-8c0d6e4b2a57
# last-edited: 2026-11-13
"""Dump the R0 commit's gin route table into internal/books/rbacreach/routes.json (F4-07).

R0 here is main at the commit ``r0config/r0_reach_commit.txt`` pins (F4-08,
2026-11-22; it was the v0.223.0 tag until then, see ``build_r0_reach.py``). The
binary has no route-listing endpoint, so the dump is taken from that commit's own
source: a detached worktree of the commit gets ``r0routes/dump_routes_test.go.txt``
as ``internal/server/r0_route_dump_test.go`` and ``go test -run TestR0RouteDump``
writes the router's routes with the ABS surface off and on. ``record_r0_reach.py``
probes every dumped route against the running binary, so a route the source dump
names and the binary does not serve shows up as a NoRoute answer in its
``uniform_not_found`` list.

    python3 scripts/r2corpus/dump_r0_routes.py            # needs go and network-free git
    python3 scripts/r2corpus/dump_r0_routes.py --src DIR  # a checkout of the commit already
"""

from __future__ import annotations

import argparse
import datetime
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

_HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = _HERE.parents[1]
OUT = REPO_ROOT / "internal" / "books" / "rbacreach" / "routes.json"
TEST_SRC = _HERE / "r0routes" / "dump_routes_test.go.txt"
GUID = "d41a8b7f-2c65-4e93-8f10-6b9e3a7c5d28"
VERSION = "1.0.0"


def _build_module():
    """build_r0_reach.py, which owns the pinned commit (imported by path)."""
    spec = importlib.util.spec_from_file_location("build_r0_reach", _HERE / "build_r0_reach.py")
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def read_commit() -> str:
    """The commit r0config/r0_reach_commit.txt pins."""
    b = _build_module()
    try:
        return b.read_commit()
    except b.BuildError as e:
        raise SystemExit(str(e)) from e


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
    ap.add_argument("--src", help="a checkout of the R0 commit (default: a temporary worktree)")
    ap.add_argument("--out", default=str(OUT))
    args = ap.parse_args(argv)
    commit = read_commit()
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r0routes-"))
    raw = tmp / "dump.json"
    worktree: pathlib.Path | None = None
    try:
        if args.src:
            src = pathlib.Path(args.src)
        else:
            worktree = tmp / "src"
            subprocess.run(
                ["git", "worktree", "add", "--detach", str(worktree), commit],
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
        "what": "the gin route table of the R0 commit's router (source dump, scripts/r2corpus/r0routes)",
        "r0_commit": commit,
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
