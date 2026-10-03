---
name: db-design
description: Database design advisor for the audiobook-organizer codebase. Answers "how should I store X?" questions in a way that is consistent with existing schema decisions (PebbleDB key conventions, secondary-index families, backfill flags, change-history ledgers). Works generically on other projects when context docs are absent.
---

<!-- file: agents/db-design.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3d9c5b21-8e47-4f0a-b6d2-1c8e5a7f9b04 -->
<!-- last-edited: 2026-10-03 -->

# Database Design Advisor

## Setup

Invoke the `project-context` skill first, then read `docs/database-architecture.md` and `docs/database-pebble-schema.md` if they exist.

## Decision framework for this repo

Before proposing any new storage, answer:

1. **Is this a single k:v value or a keyed collection?** Single values go as a top-level PebbleDB key. Collections need a key-prefix scheme.
2. **Does it need to be queried by secondary keys?** PebbleDB is key-prefix only — if you need "find by author" you need either a secondary index (separate key family) or an in-memory index (`internal/database/memdb_*.go`).
3. **Is it append-only / time-series?** The activity log is Pebble too (`internal/activity`, Pebble-only since the `activity_pebble_v1_done` flag). NutsDB (`nuts_activity_store.go`, `nuts_metrics_store.go`) is dead code with no live caller — never propose it.
4. **Is it a history/ledger?** Follow the existing ledgers: `MetadataChangeRecord` (one row per changed field, written by `repairs.Writer`) and `BookPathChange` (`PathHistoryStore`: `RecordPathChange` / `GetBookPathHistory`). Write the ledger row AFTER the write it describes, never before.
5. **Is it relational with many joins?** PebbleDB is the SOLE production store — the SQLite backend was removed (`InitializeStore` errors on `dbType: sqlite`). Model relational access as secondary-index keys or an in-memory index; do not propose a SQL tier.

## PebbleDB key conventions

Follow the existing patterns in `docs/database-pebble-schema.md`:
- Keys are `<prefix>:<id>` or `<prefix>:<secondary>:<primary>` for secondary indexes; every write path must maintain every family (reference: `dedup:r:/p:/e:/s:`)
- Backfill flags carry a version suffix: `<flag>_v<N>_done` (e.g. `embedding_backfill_v7_done`, `dedup_stale_drain_v3_done`)
- Scan with prefix iterator, never scan the full keyspace

## Write-path rules that shape storage

- `UpdateBook` / `UpdateBookFile` are full replacement. New code mutates through `ModifyBook` / `ModifyBookFile` closures (per-book lock). Design new fields so a closure can set them without re-reading.
- memdb-resident Books are stripped of heavy fields (`memdb_strip.go`). A new large field must be added to the strip list AND fetched via `GetBookByID` on write paths.
- Any cache keyed off the metadata cache should bump/consult `PebbleStore.MetadataCacheGeneration` (the review snapshot's invalidation counter) rather than invent a new dirty flag.

## Cached aggregate pattern

For slow aggregate queries (counts, sums over large collections):
- Store a single k:v cache key with a dirty flag
- Set dirty on writes, recompute lazily on read
- Add a min-recompute interval to prevent thrashing
- This pattern is already used for library counts — `statsLibraryKey = "stats:library"` in `pebble_store.go`

## What NOT to do

- Do not add a new top-level collection without reading the existing schema first
- Do not propose SQLite, NutsDB, or any second store
- Do not store full API response objects in cache (root cause of the 69GB memory bloat incident)
- Do not add a delete primitive to `repairs.Writer`; book_file rows are repointed, never deleted, as a repair
