#!/usr/bin/env python3
# file: scripts/add_short_skip.py
# version: 1.0.1
# guid: c99b33b9-4a04-4018-835a-6605dd438591
# last-edited: 2026-10-20

"""Add `if testing.Short() { t.Skip(...) }` to the top of every TestProp_*
function in the listed slow property-test files. Idempotent — re-runs leave
already-annotated tests alone.

Slow = creates a PebbleStore per rapid.Check iteration, or does filesystem I/O.
Fast prop tests (pure compute — permissions, query parser, rapidgen) are not
touched: they take seconds and carry no value from -short gating.
"""

from __future__ import annotations

import pathlib
import re
import sys

SLOW_FILES = [
    "internal/database/pebble_store_prop_test.go",
    "internal/server/audiobook_service_prop_test.go",
    "internal/server/dedup_engine_prop_test.go",
    "internal/server/playlist_evaluator_prop_test.go",
    "internal/server/undo_engine_prop_test.go",
    "internal/server/version_lifecycle_prop_test.go",
]

FUNC_PATTERN = re.compile(
    r"^func (TestProp_[A-Za-z_0-9]+)\(([a-zA-Z_][a-zA-Z0-9_]*) \*testing\.T\) \{\n",
    re.MULTILINE,
)


def skip_block(receiver: str) -> str:
    return (
        f"\tif testing.Short() {{\n"
        f'\t\t{receiver}.Skip("slow property test; run without -short")\n'
        f"\t}}\n"
    )


def already_annotated(body: str, start: int) -> bool:
    """Return True if the first non-blank line after `start` already calls
    testing.Short(). Avoids double-inserting on re-run."""
    # Look at the next 120 chars — enough for the skip block.
    window = body[start : start + 200]
    return "testing.Short()" in window


def process(path: pathlib.Path) -> tuple[int, int]:
    text = path.read_text()
    funcs = list(FUNC_PATTERN.finditer(text))
    if not funcs:
        return 0, 0
    inserted = 0
    # Walk in reverse so earlier offsets stay valid.
    new_text = text
    for match in reversed(funcs):
        opening_end = match.end()
        receiver = match.group(2)
        if already_annotated(new_text, opening_end):
            continue
        new_text = new_text[:opening_end] + skip_block(receiver) + new_text[opening_end:]
        inserted += 1
    if new_text != text:
        path.write_text(new_text)
    return len(funcs), inserted


def main() -> int:
    repo = pathlib.Path(__file__).resolve().parent.parent
    total_funcs = 0
    total_inserted = 0
    for rel in SLOW_FILES:
        path = repo / rel
        if not path.exists():
            print(f"missing: {rel}", file=sys.stderr)
            return 1
        funcs, inserted = process(path)
        total_funcs += funcs
        total_inserted += inserted
        print(f"{rel}: {inserted}/{funcs} annotated")
    print(f"total: {total_inserted}/{total_funcs} annotated")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
