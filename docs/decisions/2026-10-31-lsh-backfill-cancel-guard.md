<!-- file: docs/decisions/2026-10-31-lsh-backfill-cancel-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b8e2a14-6c37-4d90-9f1a-3e7c0d24b869 -->
<!-- last-edited: 2026-10-31 -->

# acoustid.lsh-backfill: cancel handling and the flaky cancel test

## Root cause

`TestLSHBackfill_CancelMidRun` cancelled the context inside the fifth `UpdateBookFile` and asserted fewer than all rows were updated. Two things made that unreliable:

1. Test timing. `updates.Add(1) == 5` and `cancel()` were separate steps. If the goroutine was descheduled between them (likely under `-race` on a loaded runner), the other workers and the dispatcher ran through the whole library before ctx was done: 100/100.
2. Production. `registry.RunItems` checks ctx only in its dispatcher, in a `select` over `ctx.Done()` and `sem <- struct{}{}`. When both are ready Go picks at random, so after a cancel the dispatcher keeps handing out rows with 50% odds per free slot. Rows already handed to workers also run `fn` regardless. `runLSHBackfill`'s item function never looked at ctx, so each of those still hydrated and re-saved its row. The leak is bounded (pool size plus a geometric tail), not a whole-library write, but a cancelled op was still writing.

## Decision log

- Chose: per-item `ctx.Err()` guard at the top of the item function in `runLSHBackfill` (skip, return nil), plus a `ctx.Err()` check after `RunItems` returns. Rejected: returning `ctx.Err()` from the item function, because `runItemsPar` wraps item errors in `errors.Join`, so the op would return a join error and not `context.Canceled`. The post-run check keeps a skipped item from being reported as a complete run (sequential mode on one core can end without re-checking ctx).
- Chose: fix in the op, not in `registry.RunItems`. Rejected for this change: biasing the dispatcher to check ctx before the select and checking ctx in `runOne` before `fn`. That is the more general fix and would cover every op that uses `RunItems`, but it changes shared behaviour for all callers and is outside the brief. Recorded as follow-up in `todo.d/`.
- Chose: test asserts `updates <= cancelAt + Concurrency - 1`, with the count-and-cancel step under one mutex, and the pool size read from `lshBackfillConcurrency()` (the same function the op passes to RunItems). Row count scales with the pool so the bound is never vacuous. Rejected: loosening the old assertion or a sleep.
- Chose: a second test with an already-cancelled ctx asserting zero hydrates and zero updates. It is the deterministic check of the guard itself, since the mid-run bound alone passes without the guard most of the time (the random dispatch tail is short).
