#!/usr/bin/env python3
# file: scripts/apply_narrowing.py
# version: 1.0.1
# guid: 7cf45b0f-299a-4f02-aa66-3e500d676cd0
# last-edited: 2026-10-20

"""Replace `database.Store` with narrow composites in the 18 small struct-
based services identified by narrow_struct_services.py. Each file gets a
file-local named interface (e.g. `batchServiceStore`) because inline
anonymous interfaces with 3+ embedded sub-interfaces read poorly and clutter
both the struct declaration and the constructor signature.

Idempotent — re-run is a no-op once applied."""

from __future__ import annotations

import pathlib
import re

SPEC = {
    "internal/server/ai_scan_pipeline.go": {
        "alias": "aiScanPipelineStore",
        "ifaces": ["AuthorReader", "OperationStore"],
        "field_re": r"^(\s+)mainStore\s+database\.Store\b",
        "field_sub": r"\1mainStore aiScanPipelineStore",
        "param_re": r"mainStore database\.Store",
        "param_sub": "mainStore aiScanPipelineStore",
    },
    "internal/server/audiobook_update_service.go": {
        "alias": "audiobookUpdateStore",
        "ifaces": ["BookReader"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db audiobookUpdateStore",
        "param_re": r"db database\.Store",
        "param_sub": "db audiobookUpdateStore",
    },
    "internal/server/author_series_service.go": {
        "alias": "authorSeriesStore",
        "ifaces": ["AuthorReader", "SeriesReader"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db authorSeriesStore",
        "param_re": r"db database\.Store",
        "param_sub": "db authorSeriesStore",
    },
    "internal/server/batch_service.go": {
        "alias": "batchServiceStore",
        "ifaces": ["BookStore"],  # combined BookReader + BookWriter
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db batchServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db batchServiceStore",
    },
    "internal/server/changelog_service.go": {
        "alias": "changelogStore",
        "ifaces": ["MetadataStore", "OperationStore", "PathHistoryStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db changelogStore",
        "param_re": r"db database\.Store",
        "param_sub": "db changelogStore",
    },
    "internal/server/dashboard_service.go": {
        "alias": "dashboardStore",
        "ifaces": ["AuthorReader", "BookReader", "PlaylistStore", "SeriesReader", "StatsStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db dashboardStore",
        "param_re": r"db database\.Store",
        "param_sub": "db dashboardStore",
    },
    "internal/server/diagnostics_service.go": {
        "alias": "diagnosticsStore",
        "ifaces": [
            "AuthorReader",
            "BookReader",
            "OperationStore",
            "SeriesReader",
            "StatsStore",
            "SystemActivityStore",
        ],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db diagnosticsStore",
        "param_re": r"db database\.Store",
        "param_sub": "db diagnosticsStore",
    },
    "internal/server/import_path_service.go": {
        "alias": "importPathStore",
        "ifaces": ["ImportPathStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db database.ImportPathStore",
        "param_re": r"db database\.Store",
        "param_sub": "db database.ImportPathStore",
        "skip_alias": True,  # single interface, use bare
    },
    "internal/server/import_service.go": {
        "alias": "importServiceStore",
        "ifaces": ["AuthorReader", "AuthorWriter", "BookWriter", "SeriesReader", "SeriesWriter"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db importServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db importServiceStore",
    },
    "internal/server/isbn_enrichment.go": {
        "alias": "isbnEnrichmentStore",
        "ifaces": ["AuthorReader", "BookReader", "BookWriter"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db isbnEnrichmentStore",
        "param_re": r"db database\.Store",
        "param_sub": "db isbnEnrichmentStore",
    },
    "internal/server/merge_service.go": {
        "alias": "mergeServiceStore",
        "ifaces": ["BookReader", "BookWriter", "ExternalIDStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db mergeServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db mergeServiceStore",
    },
    "internal/server/metadata_upgrade.go": {
        "alias": "metadataUpgradeStore",
        "ifaces": ["BookReader", "TagStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db metadataUpgradeStore",
        "param_re": r"db database\.Store",
        "param_sub": "db metadataUpgradeStore",
    },
    "internal/server/organize_preview_service.go": {
        "alias": "organizePreviewStore",
        "ifaces": ["BookFileStore", "BookReader"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db organizePreviewStore",
        "param_re": r"db database\.Store",
        "param_sub": "db organizePreviewStore",
    },
    "internal/server/rename_service.go": {
        "alias": "renameServiceStore",
        "ifaces": ["BookFileStore", "BookReader", "BookWriter", "NarratorStore", "OperationStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db renameServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db renameServiceStore",
    },
    "internal/server/revert_service.go": {
        "alias": "revertServiceStore",
        "ifaces": ["BookReader", "BookWriter", "OperationStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db revertServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db revertServiceStore",
    },
    "internal/server/scan_service.go": {
        "alias": "scanServiceStore",
        "ifaces": ["BookReader", "BookWriter", "ImportPathStore", "MaintenanceStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db scanServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db scanServiceStore",
    },
    "internal/server/system_service.go": {
        "alias": "systemServiceStore",
        "ifaces": ["ImportPathStore", "OperationStore", "StatsStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db systemServiceStore",
        "param_re": r"db database\.Store",
        "param_sub": "db systemServiceStore",
    },
    "internal/server/work_service.go": {
        "alias": "workServiceStore",
        "ifaces": ["WorkStore"],
        "field_re": r"^(\s+)db\s+database\.Store\b",
        "field_sub": r"\1db database.WorkStore",
        "param_re": r"db database\.Store",
        "param_sub": "db database.WorkStore",
        "skip_alias": True,
    },
}


def make_alias_block(alias: str, ifaces: list[str]) -> str:
    lines = [f"// {alias} is the narrow slice of database.Store this service uses."]
    lines.append(f"type {alias} interface {{")
    for iface in ifaces:
        lines.append(f"\tdatabase.{iface}")
    lines.append("}")
    return "\n".join(lines) + "\n\n"


# Insert the alias after the last import block closing (`)`).
IMPORT_BLOCK_END = re.compile(r"^\)$", re.MULTILINE)


def insert_alias(text: str, alias: str, ifaces: list[str]) -> str:
    if f"type {alias} interface" in text:
        return text  # already inserted
    m = IMPORT_BLOCK_END.search(text)
    if not m:
        raise RuntimeError("no import block found")
    end = m.end()
    # Find next blank line and insert after it.
    rest = text[end:]
    block = "\n\n" + make_alias_block(alias, ifaces).rstrip() + "\n"
    return text[:end] + block + rest


def process(path: pathlib.Path, spec: dict) -> None:
    text = path.read_text()
    orig = text

    # Narrow field
    text = re.sub(spec["field_re"], spec["field_sub"], text, flags=re.MULTILINE)
    # Narrow constructor param (and any other occurrences)
    text = re.sub(spec["param_re"], spec["param_sub"], text)

    # Insert alias if needed
    if not spec.get("skip_alias"):
        text = insert_alias(text, spec["alias"], spec["ifaces"])

    if text != orig:
        path.write_text(text)
        print(f"updated: {path.name}")
    else:
        print(f"unchanged: {path.name}")


def main() -> None:
    repo = pathlib.Path(__file__).resolve().parent.parent
    for rel, spec in SPEC.items():
        path = repo / rel
        if not path.exists():
            print(f"MISSING: {rel}")
            continue
        try:
            process(path, spec)
        except Exception as exc:
            print(f"ERROR {rel}: {exc}")


if __name__ == "__main__":
    main()
