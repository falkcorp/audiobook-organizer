<!-- file: docs/agent-tasks/todo-completion/database/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5aa2717-9e71-457b-9807-b8647b3466a0 -->
<!-- last-edited: 2026-08-21 -->

# Workstream — database (todo-completion)

20 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-022 | L238 | Reduce internal/database's -short test-run wall-clock cost (currently  | P2 | M | Sonnet-class | 1 |
| TASK-023 | L969 | database.Store (40) -- build the AST/go-types CI gate that makes it un | P2 | L | Opus-class | 2 |
| TASK-024 | L1227 | Finish killing database.Store — narrow the remaining references per th | P1 | L | Opus-class | 1 |
| TASK-025 | MERGE-CACHE-EVICT | Investigate then evict/dirty-flag merged-away book/file IDs from every | P1 | L | Opus-class | 1 |
| TASK-026 | VGBACKFILL-BOUNDS-FRAGILE | Replace fragile [0x30-0x3A]-only book:0..book:; bounds in the version- | P2 | S | Sonnet-class | 1 |
| TASK-027 | L1970 | Make WipeAllActivity cancellable (currently an uncancellable full scan | P2 | M | Sonnet-class | 1 |
| TASK-028 | SEC-CODEQL-BACKLOG | Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-all | P2 | M | Sonnet-class | 2 |
| TASK-029 | L3414 | Build a diagnostic reconciling the 3,954-book gap between the store's  | P2 | M | Sonnet-class | 1 |
| TASK-030 | L3526 | Guard author delete paths with an unfiltered author-reference counter  | P1 | L | Opus-class | 1 |
| TASK-031 | L3966 | Add GetBooksBySeriesIDAllVersions and switch DedupSeries's merge loop  | P1 | M | Opus-class | 2 |
| TASK-032 | L4501 | Add a compare-and-swap on Collection.Version to PebbleStore.UpdateColl | P2 | M | Sonnet-class | 1 |
| TASK-033 | L4678 | Lock the three bare globalStore accesses in InitializeStore/CloseStore | P2 | S | Haiku-class | 1 |
| TASK-034 | L4694 | Add the 4 missing compile-time assertions to iface_assert.go | P2 | S | Haiku-class | 1 |
| TASK-035 | L4721 | Repoint store.go:17's broken doc reference to the archived design spec | P2 | S | Haiku-class | 2 |
| TASK-036 | L4728 | Add Func override fields to MockStore's ~86 hardwired-zero-return meth | P2 | L | Haiku-class | 1 |
| TASK-037 | L5271 | Add DeleteNarrator to the store (CRUD building block only) | P1 | M | Opus-class | 1 |
| TASK-038 | L5290 | Fix DeleteAuthor's junction cleanup: it scans the dead book_author: ke | P1 | M | Opus-class | 2 |
| TASK-039 | L10523 | Omnibus/anthology book_type field — Part 1 of the omnibus-detection-an | P1 | L | Opus-class | 3 |
| TASK-040 | L10526 | Filter system-sourced tags out of the Browse-by-Tag cloud | P2 | M | Sonnet-class | 1 |
| TASK-041 | L10728 | Add transcribe_status to the book-summary list projection and a fronte | P2 | M | Sonnet-class | 6 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  make ci ; make ci && npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.github/workflows/ci.yml`: TASK-008, TASK-023 → serialize by wave (TASK-008=w1, TASK-023=w2)
- `internal/database/bookcore.go`: TASK-039, TASK-041 → serialize by wave (TASK-039=w3, TASK-041=w6)
- `internal/database/memdb_summaries.go`: TASK-028, TASK-041 → serialize by wave (TASK-028=w2, TASK-041=w6)
- `internal/database/pebble_store.go`: TASK-031, TASK-041 → serialize by wave (TASK-031=w2, TASK-041=w6)
- `internal/database/pebble_store_authors.go`: TASK-037, TASK-038 → serialize by wave (TASK-037=w1, TASK-038=w2)
- `internal/database/store.go`: TASK-021, TASK-033, TASK-035, TASK-039, TASK-041 → serialize by wave (TASK-021=w5, TASK-033=w1, TASK-035=w2, TASK-039=w3, TASK-041=w6)
- `internal/dedup/series_dedup.go`: TASK-031, TASK-046, TASK-048 → serialize by wave (TASK-031=w2, TASK-046=w1, TASK-048=w3)
- `internal/merge/service.go`: TASK-025, TASK-042, TASK-045, TASK-050, TASK-052 → serialize by wave (TASK-025=w1, TASK-042=w2, TASK-045=w3, TASK-050=w4, TASK-052=w5)
- `internal/plugins/maintenance/deps.go`: TASK-024, TASK-074, TASK-078 → serialize by wave (TASK-024=w1, TASK-074=w2, TASK-078=w6)
- `internal/search/bleve_index.go`: TASK-025, TASK-134 → serialize by wave (TASK-025=w1, TASK-134=w2)
- `internal/search/index_builder.go`: TASK-025, TASK-134 → serialize by wave (TASK-025=w1, TASK-134=w2)
- `internal/server/handlers/audiobooks/handler.go`: TASK-006, TASK-039, TASK-104, TASK-107 → serialize by wave (TASK-006=w4, TASK-039=w3, TASK-104=w1, TASK-107=w2)
- `internal/server/indexed_store.go`: TASK-024, TASK-142 → serialize by wave (TASK-024=w1, TASK-142=w2)
- `internal/server/maintenance_fixups.go`: TASK-027, TASK-138 → serialize by wave (TASK-027=w1, TASK-138=w2)
- `internal/server/server.go`: TASK-024, TASK-028 → serialize by wave (TASK-024=w1, TASK-028=w2)
- `internal/server/server_lifecycle.go`: TASK-028, TASK-073, TASK-137, TASK-141, TASK-149 → serialize by wave (TASK-028=w2, TASK-073=w1, TASK-137=w3, TASK-141=w4, TASK-149=w5)
- `internal/server/server_maintenance_deps.go`: TASK-024, TASK-074, TASK-078 → serialize by wave (TASK-024=w1, TASK-074=w2, TASK-078=w6)
- `web/src/pages/BookDetail.tsx`: TASK-039, TASK-109, TASK-175 → serialize by wave (TASK-039=w3, TASK-109=w1, TASK-175=w5)
- `web/src/services/api.ts`: TASK-039, TASK-078 → serialize by wave (TASK-039=w3, TASK-078=w6)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-022, TASK-024, TASK-025, TASK-026, TASK-027, TASK-029, TASK-030, TASK-032, TASK-033, TASK-034, TASK-036, TASK-037, TASK-040 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-023, TASK-028, TASK-031, TASK-035, TASK-038 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-039 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-041 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
