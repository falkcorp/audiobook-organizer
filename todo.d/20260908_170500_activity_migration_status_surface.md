- [x] **Give the Pebble→SQLite activity migration a visible status.** DONE. The
  migration now writes an `operations_v2` row (`activity.sql-migration`) and drives
  it from a progress callback on the backfill, so it appears in the operations UI
  with its tier, position (`tier 1/7`) and running counts, and closes out
  `completed` / `failed` / `interrupted_quiesced`.

  **The route this note recommended was wrong — do not retry it.** Running the
  backfill AS a registry op is unsafe, and the "just close the 2s gap" fix below
  does not work. That reasoning treated batch cost as a constant. Per
  `sqlBackfillProgressEvery`'s own comment a 500-row batch of iTunes
  `ApplyITLOperations` rows carries **~9.6 MB of `details` EACH** — gigabytes to
  compress and insert, twice (copy then parity) — and a single row's insert is not
  interruptible at all. No number of ctx checks bounds that under a hard 2s escape.
  Confirming it: **`pebble_activity_store.go` contains `recoverPebbleClosed` zero
  times**, so overrunning the escape is a process PANIC, not an error. That is why
  `sqlMigrationStarter.Stop` joins unbounded, and why that comment must not be
  "fixed".

  So the work stays on its own goroutine and only the REPORTING is registry-shaped
  (`InsertOperationV2` / `UpdateOpProgressV2` / `UpdateOperationV2Status` are plain
  store writes with no worker and no `goroutineWG` enrollment). **No `OperationDef`
  is registered on purpose**: `ActiveDefs()` has no allowlist and feeds
  `/api/v1/op-defs`, so registering one would add a Run button that could launch a
  second concurrent backfill. The cost is that `resumeAfterStartup` drops a stale
  row as "unknown def", which is the correct terminal state.

  **Two things this bought that the checkpoint blob cannot give.** A `failed`
  migration is now durably recorded with its reason — `loadActivityBackfillProgress`
  rewrites a `failed` tier verdict back to `in_progress` on the next boot (correctly,
  so it re-verifies in full), which means after a restart the blob can no longer
  answer "why didn't it flip?". And the row survives independently of it.

  **Denominator: dropped deliberately, not deferred.** `total` is 0. Counting one
  costs a full key walk per tier and the number moves under itself anyway as
  dual-writes land. `OperationsIndicator` already renders `total === 0` as an
  indeterminate bar, which is the truthful rendering — but it also printed the
  literal `'Starting...'` in that case, which for a 4-hour job with 4.7M rows copied
  reads as a stuck job. Fixed generally for every zero-total op: show the count once
  progress is non-zero (`formatProgressCounts`, `web/src/components/layout/
  operationsFormat.ts`).

- [x] **Never resume the `digest` tier of the activity migration.** DONE (follow-up
  to the checkpoint below, after review caught it). A resume bound is sound only
  where every writer either dual-writes to SQLite or appends forward in time.
  `CompactByDay` does neither: it keys its row `pactPrimaryKey("digest",
  startOfDay, ulid)` — BACKDATED — and routes through
  `MigratingActivityStore.CompactByDay` → `m.active()`, which is Pebble for the
  whole migration, so there is no SQLite counterpart. It is reachable
  mid-migration from scheduled maintenance (`server_maintenance_deps.go:285`) and
  from a user button (`handlers/activity.go:385`), and it REPLACES a day's digest
  with a fresh ULID. A row below the cursor was therefore skipped by the copy AND
  by the verify — the parity pass re-presents only the batch it just read, never
  re-reading Pebble — leaving the tier `clean` while missing a row that may be a
  correction to a stale one. `activityNonResumableTiers` now forces a full scan of
  `digest` (~1 row/day); `change` (~5.7M rows) still resumes. Residual, documented
  not coded: `Summarize` also bypasses the dual-write but keys at `now`, unsafe
  only if a caller recorded a FUTURE-dated entry the scan already passed
  (`normalizeActivityEntry` rejects pre-epoch, does not clamp forward), and the
  loss would be one summary row whose originals are already in SQLite.

- [ ] **Two stale reading-guides were corrected with it — watch for more.**
  `PerTierCopied`'s doc said scanned≫copied means "a RESUMED run re-streaming
  already-copied history"; checkpointing INVERTS that (a resume now skips it, so
  scanned≈copied), and the shape now means a full re-scan instead. The
  `streamTierEntries` resume comment asserted keys sort in write order, which
  `sql_activity_backfill.go`'s own header denies (timestamps are caller-supplied
  and non-monotonic). Both are fixed; the pattern — a justification outliving its
  reason — is worth a sweep of the other activity-migration comments.

- [x] **Checkpoint the migration so a restart doesn't start over.** DONE: adds
  `ActivitySQLBackfillProgressKey`, a per-tier `{state, cursor, scanned, copied,
  reinserted}` blob written every batch (NoSync) with verdict transitions synced,
  and threads a `startAfter` lower bound through `streamTierEntries`. Production
  restarted the migration from row zero three times on 2026-09-08, discarding
  ~81 minutes, then spent ~3h re-reading 6.5M rows it had already copied.
  **The non-obvious half:** a bare cursor would have been a data-integrity bug.
  `ParityOK` was in-memory only and reset to `true` at the top of every run, so it
  was fail-closed only *by accident* — a crash forced a full re-verify. With a
  cursor, a run that failed parity, died, and resumed would re-derive "clean" from
  its untainted tail and flip reads onto an unverified copy. The verdict is now
  durable per tier, `reinserted` carries across resumes, a `failed` tier is
  re-scanned in FULL before it can pass, and the sentinel gate is a census
  (`AllTiersClean`) rather than a process-local flag.
