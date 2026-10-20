#!/usr/bin/env python3
# file: scripts/check_store_noops.py
# version: 1.0.1
# guid: d8be29f8-1681-4bfd-b6a0-9637cc3e03d6
# last-edited: 2026-10-20

"""For each candidate noop file, identify the struct field(s) of type
`database.Store` and check whether the file actually calls any methods on
those fields. Files with zero method calls are safe to migrate by deleting
the field entirely."""

from __future__ import annotations

import pathlib
import re
import sys

CANDIDATES = [
    "internal/server/ai_scan_pipeline.go",
    "internal/server/audiobook_update_service.go",
    "internal/server/author_series_service.go",
    "internal/server/batch_service.go",
    "internal/server/changelog_service.go",
    "internal/server/config_update_service.go",
    "internal/server/dashboard_service.go",
    "internal/server/dedup_engine.go",
    "internal/server/diagnostics_service.go",
    "internal/server/import_path_service.go",
    "internal/server/import_service.go",
    "internal/server/isbn_enrichment.go",
    "internal/server/merge_service.go",
    "internal/server/metadata_fetch_service.go",
    "internal/server/metadata_upgrade.go",
    "internal/server/organize_preview_service.go",
    "internal/server/organize_service.go",
    "internal/server/rename_service.go",
    "internal/server/revert_service.go",
    "internal/server/scan_service.go",
    "internal/server/system_service.go",
    "internal/server/work_service.go",
]

# Field declaration: "<name>    database.Store" with any whitespace,
# capturing the name. Anchored to line start (after optional tab/spaces)
# so it only matches struct fields, not random references.
FIELD_RE = re.compile(r"^\s+([a-z][a-zA-Z0-9_]*)\s+database\.Store\b", re.MULTILINE)

# Method call: <name>.<Capital>...
CALL_TEMPLATE = re.compile(r"(?:^|[^a-zA-Z_0-9])(?:[a-z]\.)?{name}\.[A-Z][a-zA-Z0-9]+\s*\(")


def scan(path: pathlib.Path) -> dict:
    text = path.read_text()
    fields = FIELD_RE.findall(text)
    fields = list(dict.fromkeys(fields))  # preserve order, dedup
    report = {"path": str(path), "fields": fields, "calls": {}}
    for name in fields:
        pat = re.compile(
            rf"(?:^|[^a-zA-Z_0-9]){re.escape(name)}\.[A-Z][a-zA-Z0-9]+\s*\(", re.MULTILINE
        )
        hits = pat.findall(text)
        report["calls"][name] = len(hits)
    return report


def main() -> int:
    repo = pathlib.Path(__file__).resolve().parent.parent
    noop = []
    has_calls = []
    for rel in CANDIDATES:
        path = repo / rel
        if not path.exists():
            print(f"MISSING: {rel}")
            continue
        r = scan(path)
        total_calls = sum(r["calls"].values())
        if not r["fields"]:
            print(f"{rel}: NO database.Store field found (already cleaned?)")
            continue
        fields_desc = ", ".join(f"{n}({r['calls'][n]})" for n in r["fields"])
        if total_calls == 0:
            noop.append((rel, r["fields"]))
            print(f"NOOP: {rel}  fields: {fields_desc}")
        else:
            has_calls.append((rel, r["calls"]))
            print(f"HAS CALLS: {rel}  {fields_desc}")

    print(f"\nSummary: {len(noop)} noop, {len(has_calls)} with calls")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
