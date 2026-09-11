### Fixed

#### Every activity-log maintenance pass now runs on both activity backends, and `POST /admin/recompact-digests` is a background op

PR #3214 corrected `MigratingActivityStore.CompactByDay` to compact both
activity backends; `Summarize`, `Prune`, `RecompactDigests` and
`RepairActivityIndexes` on the same wrapper still ran on the active backend
only, while `Record` wrote to both. The inactive backend therefore kept every
debug row past 30 days and every change row past 90 days forever, never had
its legacy digest items re-derived, and — once reads flip to SQLite, whose
index repair is a documented no-op — would never have its Pebble index leak
repaired again. All four now fan out to both backends through one shared
helper (`runOnBothBackends`), always attempting both, summing the counters,
and surfacing the first error while wrapping the second.

Running the deleting passes on SQLite exposed a window the backfill could not
tolerate: it copies a batch into SQLite and then re-presents it, counting any
row the second pass inserts as a copied row that did not land — a parity
failure that blocks the read flip and forces a full rescan on the next boot. A
summarize/prune/compaction delete landing between the two passes read exactly
like that. `SQLActivityStore` now carries a backfill gate (`sync.RWMutex`):
the backfill holds the read side across copy-and-verify
(`copyAndVerifyBatch`), and `CompactByDay`, `Summarize`, `Prune` and
`WipeAllActivity` hold the write side for their run. This closes the same
window for compaction, which #3214 had already put on SQLite.

Liveness for the nightly `maintenance.cleanup-activity-log`, which now does
twice the summarize/prune/repair work: a second context hook
(`database.WithMaintenanceProgress`) carries per-group progress from both
`Summarize` implementations and a per-backend completion from the wrapper for
summarize and index repair; the server brackets each phase with a stamp
(`Prune` has no context on the interface and cannot report mid-run); and the
def's `ProgressTimeout` is raised from the 5m default to 20m.

`POST /api/v1/admin/recompact-digests` no longer re-derives digests inside the
request. It enqueues the new `maintenance.recompact-activity-digests` op
(`LivenessNone` with an explicit 1h budget, cancellable, sharing the nightly
cleanup's concurrency key) and answers 202 with the op id, 409 if a run is
already live — the same contract `POST /activity/compact` gained in #3214.
The `touched`/`skipped` counters are the op's persisted result.

Known interaction, unchanged in kind from #3214: each backend now writes its
own summary rows, so a later re-run of the activity backfill copies Pebble's
summaries into SQLite alongside SQLite's own — one extra `pruned_at`-marked
line per group per backfill, not lost data.
