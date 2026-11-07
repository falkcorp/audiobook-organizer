#!/usr/bin/env python3
# file: core/examples/hello/testdata/specblocks/regen.py
# version: 1.0.0
# guid: f5df7c64-7c04-4501-b33b-01f06e7155f8
# last-edited: 2026-11-07
"""Snapshot the hello example's Go blocks out of the platform-core SDK spec.

TestSpecGoBlocksCompile (06 §6.10, COR-16) compiles and boots the hello
example's Go blocks. It reads this directory, never the spec, so a spec edit
cannot break core CI and the test survives the repo split. This script is the
one writer of the snapshot:

    python3 regen.py <path to 06-app-sdk.md>          # rewrite the snapshot
    python3 regen.py --check <path to 06-app-sdk.md>  # exit 1 when it is stale

A block is part of the hello example when its first line that is not blank and
not a //go:build constraint is a comment naming a file under
core/examples/hello/ (06 §6.12 marks every block so). Each block becomes
<name>.go.txt, its text verbatim. blocks.json records, for each block, the
spec file, the heading above it, the file it names and the sha256 of its text.
A row that grows hello reruns this and commits the refreshed snapshot with its
code. --check is wired into no required check.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
MARK = re.compile(r"^// (core/examples/hello/\S+\.go)$")
INDEX = "blocks.json"


def extract(spec: Path) -> list[dict]:
    """Return the marked Go blocks of spec, in document order."""
    blocks: list[dict] = []
    heading = ""
    lines = spec.read_text(encoding="utf-8").splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        if line.startswith("#"):
            heading = line.lstrip("#").strip()
        if line.strip() == "```go":
            body: list[str] = []
            i += 1
            while i < len(lines) and lines[i].strip() != "```":
                body.append(lines[i])
                i += 1
            first = next(
                (b for b in body if b.strip() and not b.startswith("//go:build")),
                "",
            )
            m = MARK.match(first.strip())
            if m:
                text = "\n".join(body) + "\n"
                dest = m.group(1).removeprefix("core/")
                rel = dest.removeprefix("examples/hello/")
                blocks.append(
                    {
                        "snapshot": rel.replace("/", "-") + ".txt",
                        "dest": dest,
                        "spec": spec.name,
                        "heading": heading,
                        "sha256": hashlib.sha256(text.encode()).hexdigest(),
                        "text": text,
                    }
                )
        i += 1
    names = [b["snapshot"] for b in blocks]
    if len(names) != len(set(names)):
        sys.exit(f"regen.py: two blocks name the same file: {sorted(names)}")
    return blocks


def rendered(blocks: list[dict]) -> dict[str, str]:
    """Return every file of the snapshot, name to content."""
    index = {
        "generator": "core/examples/hello/testdata/specblocks/regen.py",
        "blocks": [{k: v for k, v in b.items() if k != "text"} for b in blocks],
    }
    files = {b["snapshot"]: b["text"] for b in blocks}
    files[INDEX] = json.dumps(index, indent=2, sort_keys=True) + "\n"
    return files


def current() -> dict[str, str]:
    """Return the snapshot files on disk."""
    out = {p.name: p.read_text(encoding="utf-8") for p in HERE.glob("*.go.txt")}
    idx = HERE / INDEX
    if idx.exists():
        out[INDEX] = idx.read_text(encoding="utf-8")
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("spec", type=Path, help="path to docs/specs/platform-core/06-app-sdk.md")
    ap.add_argument(
        "--check", action="store_true", help="exit 1 when the snapshot differs from the spec"
    )
    args = ap.parse_args()
    want = rendered(extract(args.spec))
    have = current()
    if args.check:
        stale = sorted(n for n in want.keys() | have.keys() if want.get(n) != have.get(n))
        if stale:
            print("regen.py: snapshot is stale: " + ", ".join(stale), file=sys.stderr)
            return 1
        return 0
    for name in have.keys() - want.keys():
        (HERE / name).unlink()
    for name, text in want.items():
        (HERE / name).write_text(text, encoding="utf-8")
    print(f"regen.py: wrote {len(want) - 1} blocks and {INDEX}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
