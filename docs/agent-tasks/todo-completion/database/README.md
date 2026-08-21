<!-- file: docs/agent-tasks/todo-completion/database/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f4fda7e-3a86-4ae4-b59e-eed87ff2acdb -->
<!-- last-edited: 2026-08-21 -->

# Workstream — database (todo-completion)

20 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-020 | L238 | Reduce internal/database's -short test-run wall-clock cost (currently  | P2 | M | Sonnet-class | 1 |
| TASK-021 | L969 | database.Store (40) -- build the AST/go-types CI gate that makes it un | P2 | L | Opus-class | 2 |
| TASK-022 | L1227 | Finish killing database.Store — narrow the remaining references per th | P1 | L | Opus-class | 2 |
| TASK-023 | MERGE-CACHE-EVICT | Investigate then evict/dirty-flag merged-away book/file IDs from every | P1 | L | Opus-class | 2 |
| TASK-024 | VGBACKFILL-BOUNDS-FRAGILE | Replace fragile [0x30-0x3A]-only book:0..book:; bounds in the version- | P2 | S | Sonnet-class | 1 |
| TASK-025 | L1970 | Make WipeAllActivity cancellable (currently an uncancellable full scan | P2 | M | Sonnet-class | 1 |
| TASK-026 | SEC-CODEQL-BACKLOG | Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-all | P2 | M | Sonnet-class | 1 |
| TASK-027 | L3414 | Build a diagnostic reconciling the 3,954-book gap between the store's  | P2 | M | Sonnet-class | 1 |
| TASK-028 | L3526 | Guard author delete paths with an unfiltered author-reference counter  | P1 | L | Opus-class | 1 |
| TASK-029 | L3966 | Add GetBooksBySeriesIDAllVersions and switch DedupSeries's merge loop  | P1 | M | Opus-class | 2 |
| TASK-030 | L4501 | Add a compare-and-swap on Collection.Version to PebbleStore.UpdateColl | P2 | M | Sonnet-class | 1 |
| TASK-031 | L4678 | Lock the three bare globalStore accesses in InitializeStore/CloseStore | P2 | S | Haiku-class | 1 |
| TASK-032 | L4694 | Add the 4 missing compile-time assertions to iface_assert.go | P2 | S | Haiku-class | 1 |
| TASK-033 | L4721 | Repoint store.go:17's broken doc reference to the archived design spec | P2 | S | Haiku-class | 2 |
| TASK-034 | L4728 | Add Func override fields to MockStore's ~86 hardwired-zero-return meth | P2 | L | Haiku-class | 1 |
| TASK-035 | L5271 | Add DeleteNarrator to the store (CRUD building block only) | P1 | M | Opus-class | 1 |
| TASK-036 | L5290 | Fix DeleteAuthor's junction cleanup: it scans the dead book_author: ke | P1 | M | Opus-class | 2 |
| TASK-037 | L10523 | Omnibus/anthology book_type field — Part 1 of the omnibus-detection-an | P1 | L | Opus-class | 6 |
| TASK-038 | L10526 | Filter system-sourced tags out of the Browse-by-Tag cloud | P2 | M | Sonnet-class | 1 |
| TASK-039 | L10728 | Add transcribe_status to the book-summary list projection and a fronte | P2 | M | Sonnet-class | 3 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.github/workflows/ci.yml`: TASK-006, TASK-021 → serialize by wave (TASK-006=w1, TASK-021=w2)
- `internal/database/bookcore.go`: TASK-037, TASK-039 → serialize by wave (TASK-037=w6, TASK-039=w3)
- `internal/database/dbtest/invariants.go`: TASK-022, TASK-085 → serialize by wave (TASK-022=w2, TASK-085=w4)
- `internal/database/memdb_summaries.go`: TASK-026, TASK-039 → serialize by wave (TASK-026=w1, TASK-039=w3)
- `internal/database/pebble_store.go`: TASK-029, TASK-039, TASK-085 → serialize by wave (TASK-029=w2, TASK-039=w3, TASK-085=w4)
- `internal/database/pebble_store_authors.go`: TASK-035, TASK-036 → serialize by wave (TASK-035=w1, TASK-036=w2)
- `internal/database/store.go`: TASK-019, TASK-031, TASK-033, TASK-037, TASK-039 → serialize by wave (TASK-019=w4, TASK-031=w1, TASK-033=w2, TASK-037=w6, TASK-039=w3)
- `internal/dedup/series_dedup.go`: TASK-029, TASK-044, TASK-046 → serialize by wave (TASK-029=w2, TASK-044=w1, TASK-046=w3)
- `internal/merge/service.go`: TASK-023, TASK-040, TASK-043, TASK-048, TASK-050 → serialize by wave (TASK-023=w2, TASK-040=w1, TASK-043=w3, TASK-048=w4, TASK-050=w5)
- `internal/plugins/maintenance/deps.go`: TASK-022, TASK-069, TASK-073 → serialize by wave (TASK-022=w2, TASK-069=w1, TASK-073=w5)
- `internal/search/bleve_index.go`: TASK-023, TASK-129 → serialize by wave (TASK-023=w2, TASK-129=w1)
- `internal/search/index_builder.go`: TASK-023, TASK-129 → serialize by wave (TASK-023=w2, TASK-129=w1)
- `internal/server/handlers/audiobooks/handler.go`: TASK-004, TASK-037, TASK-099, TASK-102 → serialize by wave (TASK-004=w1, TASK-037=w6, TASK-099=w2, TASK-102=w3)
- `internal/server/indexed_store.go`: TASK-022, TASK-137 → serialize by wave (TASK-022=w2, TASK-137=w1)
- `internal/server/maintenance_fixups.go`: TASK-025, TASK-133 → serialize by wave (TASK-025=w1, TASK-133=w2)
- `internal/server/server.go`: TASK-022, TASK-026 → serialize by wave (TASK-022=w2, TASK-026=w1)
- `internal/server/server_lifecycle.go`: TASK-026, TASK-068, TASK-132, TASK-136, TASK-144 → serialize by wave (TASK-026=w1, TASK-068=w5, TASK-132=w2, TASK-136=w3, TASK-144=w4)
- `internal/server/server_maintenance_deps.go`: TASK-022, TASK-069, TASK-073 → serialize by wave (TASK-022=w2, TASK-069=w1, TASK-073=w5)
- `web/src/pages/BookDetail.tsx`: TASK-037, TASK-104, TASK-170 → serialize by wave (TASK-037=w6, TASK-104=w1, TASK-170=w8)
- `web/src/services/api.ts`: TASK-037, TASK-073 → serialize by wave (TASK-037=w6, TASK-073=w5)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-020, TASK-024, TASK-025, TASK-026, TASK-027, TASK-028, TASK-030, TASK-031, TASK-032, TASK-034, TASK-035, TASK-038 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-021, TASK-022, TASK-023, TASK-029, TASK-033, TASK-036 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-039 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-037 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
