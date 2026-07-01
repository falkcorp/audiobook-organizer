<!-- file: docs/agent-tasks/provenance-hash-chain/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 75820a85-e91b-4dd7-82c5-01b8d5d20783 -->
<!-- last-edited: 2026-07-01 -->

# Workstream — File provenance / hash chain

File-provenance features: a download-hash column and an integrity alert. From HASH-CHAIN-1, HASH-CHAIN-3.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | HASH-CHAIN-1 | Add DownloadHash field to book_files, populate from Deluge, manual-set API | P2 | M | Sonnet | 1 |
| TASK-02 | HASH-CHAIN-3 | Integrity alert: file_hash != original_file_hash with no AO write on record | P2 | M | Sonnet | 1 |

## Ground rules

Go. PebbleDB is the sole store (no SQLite). Build+test: `go build ./... && go test ./internal/database/... -count=1`.

## Collision / wave note

T01 and T02 touch different areas (schema/field vs integrity check) — both run in wave 1, in parallel. T01 modifies `internal/database/` (BookFile struct + PebbleStore) and adds an API endpoint under `internal/server/`. T02 adds a new maintenance check under `internal/plugins/maintenance/` and only reads existing fields (`FileHash`, `OriginalFileHash`, `PostMetadataHash`) — no field additions, no shared file edits with T01. Merge order does not matter; whichever merges second should rebase before opening its PR.

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
