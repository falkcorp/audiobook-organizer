<!-- file: docs/agent-tasks/logging-slog/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 13f180ce-4370-4151-b650-c9bf62f9cd88 -->
<!-- last-edited: 2026-07-01 -->

# Workstream — Structured-logging residual

Wire `logging.Info(ctx)` into the remaining long-tail async ops that still use raw `slog`. From SLOG-W13 residual. Split into small tasks because W13 was previously re-held for context overflow.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|---|---|---|---|---|---|---|
| TASK-01 | SLOG-W13a | Wire `logging.Info(ctx)` into `runIsbnEnrichment`'s `EnrichBookISBN` + the ISBN enrichment goroutine callsite | P2 | S | Haiku | 1 |
| TASK-02 | SLOG-W13b | Wire `logging.Info(ctx)` into iTunes sync ops — **BLOCKED, close as no-op** | P3 | XS | Haiku | 1 |
| TASK-03 | SLOG-W13c | Wire `logging.Info(ctx)` into scanner deep paths | P2 | M (re-scoped from S — see brief) | Haiku (flag for Sonnet escalation) | 1 |

## Ground rules

Go. Mechanical: replace raw `slog.Info/Warn/Error/Debug` with `logging.Info(ctx,...)` etc. ONLY inside op-context flows where `logging.WithOp` is upstream. Keep each task's file set small. Build+test the changed package.

**Verified 2026-07-01 during authoring:** the spec's original evidence blurb ("runBulkWriteBack, iTunes sync, scanner deep paths remain") does not fully match the current code:
- `runBulkWriteBack` (`internal/server/metadata_ops.go`) **already** uses `progress.Log(...)` everywhere — there is no raw `slog.Info/Warn/Error/Debug` call inside it to swap. The only `slog.` references left in that file are `slog.LevelInfo`/`slog.LevelWarn`/`slog.LevelError`/`slog.LevelDebug` constants used to pick a log *level*, not raw log calls — those are correct as-is and must not be touched.
- The real "ISBN enrichment" residual is inside `internal/metafetch/isbn.go`'s `EnrichBookISBN`, which lacks a `ctx` parameter even though its only in-op caller (`EnrichMissingISBNs`) already has one.
- iTunes sync ops are all unimplemented stubs with zero `logging.WithOp` usage anywhere in the itunes tree — there is no valid op-context flow to wire today. See TASK-02.
- Scanner's raw `slog.Info` calls are real and reachable from the `performScanInternal(ctx, opID, ...)` op, but `ctx` is dropped at the exported `ScanDirectoryParallel` call — fixing this means widening an exported function signature and the `Scanner` interface (and hand-editing the generated mock), not a pure one-line swap. See TASK-03 for the full scope.

## Collision / wave note

Each task targets a distinct set of files — one wave, but keep each worker's scope small. TASK-01 (`internal/metafetch/*`) and TASK-03 (`internal/scanner/*`) do not share any files and can run fully in parallel. TASK-02 requires no file changes (documented no-op close).

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
