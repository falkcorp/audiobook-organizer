<!-- file: docs/agent-tasks/logging-slog/ORCHESTRATION.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7921cc8-41e7-45d6-a009-3f3afd5088af -->
<!-- last-edited: 2026-07-01 -->

# Orchestration — logging-slog

## Waves

There is exactly **one wave** for this workstream, and it contains only **one runnable task** (TASK-01). TASK-02 is BLOCKED (no-op close, see below); TASK-03 is real work but was re-scoped to M-effort during authoring because it requires an exported-interface change.

### Wave 1 (parallel-safe)

| Task | Files touched | Notes |
|---|---|---|
| TASK-01 (SLOG-W13a, writeback-isbn) | `internal/metafetch/isbn.go`, `internal/metafetch/service_fetch.go`, `internal/server/isbn_enrichment_test.go`, `internal/metafetch/service_mock_test.go` | Small, mechanical, no file overlap with TASK-03. |
| TASK-03 (SLOG-W13c, scanner-deep-paths) | `internal/scanner/scanner.go`, `internal/scanner/service.go`, `internal/scanner/scanner.go` (interface `Scanner`), `internal/scanner/mocks/mock_scanner.go`, `internal/scanner/chapter_consolidation.go`, `internal/scanner/shattered_coalesce.go`, plus scanner test files that call `ScanDirectoryParallel` | Wider blast radius (exported interface + generated mock). Does NOT touch any file TASK-01 touches, so it can run in the same wave without collision. |

TASK-01 and TASK-03 touch entirely disjoint file sets (`internal/metafetch/*` vs `internal/scanner/*`), so they run in parallel with zero rebase risk between them, per the workstream's collision note ("each task targets a distinct set of files — one wave").

### TASK-02 (SLOG-W13b, itunes-sync) — do not schedule as real work

Verification during authoring (2026-07-01) found that **every** `internal/plugins/itunes/*.go` operation entry point (`runSync`, `runPositionSync`, `runImport`, `runPathReconcile`, `runPathRepair`) is an unimplemented stub that only contains a `// TODO: Implement ...` comment and `return nil`. There is **zero** use of `logging.WithOp`, `logging.Info`, or `logging.Warn` anywhere under `internal/itunes/` or `internal/itunes/service/`. The raw `slog.*` calls that do exist in `internal/itunes/service/{playlist_sync,position_sync,validate,importer,writeback_batcher,track_provisioner,location_normalize}.go` are not reachable from any op-context flow today — they run standalone or from nowhere at all (`MigrateSmartPlaylists`, `PushDirty`, `Positions.Sync` have zero non-test callers in the repo).

Per the workstream ground rule ("ONLY inside op-context flows where `logging.WithOp` is upstream ... code outside ops can stay as raw slog"), there is currently no valid mechanical work for TASK-02. The task brief documents this and instructs the worker to close it as a no-op rather than force ctx-threading through dead/stub code (which would silently expand scope into implementing iTunes sync — explicitly out of bounds for this workstream). Re-open TASK-02 only after a future task actually implements one of the iTunes plugin op stubs and wires `logging.WithOp` upstream of it.

## Why not more waves

There's no second wave because TASK-02 produces no code change (see above) and TASK-01/TASK-03 have no dependency ordering — both can be dispatched immediately and merged independently, in either order.
