<!-- file: docs/decisions/2026-10-30-finish-without-resuming.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7d2e9f4-3c61-4a85-9e0b-5f18a3c47d62 -->
<!-- last-edited: 2026-10-30 -->

# Finish without resuming (05-PR5-FINISH-ON-DECLINE)

## Decision log

### What the task assumed, and what the code has

The TODO entry says the decision sits "next to Retry and Discard for a v3 run in
`awaiting_decision`". There are no v3 Retry or Discard routes. `/api/v3/ops/runs/:id/` has
only `cancel` and `approve`. The Retry and Discard buttons on the Activity page are v1
routes: `POST /api/v1/operations/v2/:id/retry` (`RetryOperationV2`) and
`DELETE /api/v1/operations/v2/:id/record` (`DiscardOperationV2`). Retry of an interrupted
row goes through `Registry.RetryInterrupted`, which resets the v2 row; the v3 record is
projected from it. `state.EvDecideRetry` is defined and tested but no runtime code emits it.

### Chosen

- **Route**: `POST /api/v1/operations/v2/:id/finish`, beside retry, same guard
  (`settings.manage`), same handler checks (`defPermissionsHeld`, `repairsStartAllowed`).
  Response 202 `{id, def_id, status:"queued", mode:"finish_only"}`.
- **Registry**: `Registry.FinishWithoutResuming(ctx, opID, actor)` and
  `Registry.CanFinishWithoutResuming(defID)` in `internal/operations/registry/finish.go`.
  It takes `retryMu` (the lock Retry and the stand-down resume take), refuses a row that has a
  live handle, requires a v3 record in `awaiting_decision` and a native Batch/Fixer def, then
  applies the native `state.EvDecideFinish` transition (`awaiting_decision -> queued`).
- **Record flag**: `RunRecord.FinishOnly` (`finish_only`, omitempty). Set only by
  `decide_finish`. Kept by `wait_cleared` and by dispatch. Cleared by every other way into
  queued: `decide_retry`, resume, and a v2-side write into queued (`projectRunFromV2`), so only
  the decision ever produces a finish-only attempt. The run record is core/hot-split, so a new
  core field round-trips with no migration.
- **Runner**: `batchConfig.finishOnly`. `plan()` loads the stored ledger and marks whatever
  `job.Resume` says (a FromZero job would ignore them on a resume, but the rows they record
  were applied all the same), reads a Snapshot only for its size, and never touches the source.
  `runBatch` then starts no workers, producer or ledger loop, refreshes liveness, and runs
  `Finish` through the same `summary()` and `finish()` as a normal end. A shutdown that stops
  the finish-only attempt itself skips `Finish`, as for any attempt the registry will resume,
  and the run returns to `awaiting_decision` with the flag still set.
- **Metric label**: the alias-entry label is a closed set, so the decision is counted under
  the existing `retry` entry rather than widening the label.
- **UI gating**: the server computes `can_finish` (interrupted_ask and a finishable def) on the
  timeline and single-op responses; the Activity page shows the button from that and nothing
  else, so the button and the 409 cannot disagree.
- **No confirm dialog**: Retry and Discard on that page have none (Discard is the more
  destructive of the two), so the new button follows the page's pattern and says what it does
  in its tooltip and toast. A dialog can be added for all three together.
- **No ledger**: a run that never completed a chunk has no ledger; the decision still runs
  `Finish`, over an empty Summary. The TODO's note that the restart-strike force-drop needs
  nothing is unchanged: it fires only when no chunk ever completed.
- **Handler interface**: `OperationsRegistry` would have reached 9 methods, over the
  interface-width limit of 8. The decision methods moved into an embedded
  `OperationsDecisions` interface (Retry, Finish, CanFinish); the method set is unchanged.
- **Dead-code ratchet**: `runBatch` and `runV3` are in the one-time 05-PR5 baseline. The
  finish-only logic is inlined into `plan()` and `runBatch` instead of new helpers that only
  they call, so the baseline does not grow.

### Rejected

- **A new v3 route (`/api/v3/ops/runs/:id/finish`) now.** The Activity page and its Retry and
  Discard sit on v1, and nothing registers v3 defs in production until 05-PR7. A v3 route would
  have no caller and would be dead code. The v3 migration guide maps retry to a v3 path; the
  finish route should be added there when retry is.
- **Reusing `decide_retry` plus a flag on the request.** It would make one event mean two
  things and break `InferEvent`, which names `awaiting_decision -> queued` `decide_retry` for
  the v2 facade moves.
- **Enabling the decision for v2 defs.** A v2 def has no ledger and no Finish; "finish" would
  be a full Run, which is Retry under another name. Refused server-side (409) and not offered.
- **A flag carried only in memory or in the dispatch request.** The flag must survive a
  restart between the decision and the dispatch, so it lives on the persisted record.
- **Checking `activeRunOfDef` before the decision.** The finish-only attempt takes no lease
  and writes nothing the def's other runs read, and Retry's own check exists because a full
  resume competes for the same work. The dispatcher's concurrency key still applies when the
  attempt is dispatched.

### Known limits

- **Rows applied in a chunk that never completed** carry a row marker and no ledger mark, so
  `Finish` does not see them. Recovering them needs the items to run again, which is Retry.
- **A boot reconcile** that rebuilds a record from a v2 row drops `FinishOnly`, so a decided
  but not yet dispatched run would then resume in full. The window is the time between the
  decision and the dispatch; the outcome is a Retry, not a lost run.
- **`loadSnapshot`** logs a "starting over" warning when the snapshot is missing. In a
  finish-only attempt nothing starts over; the message is misleading there. It is left as it
  is because it is shared with the resume path and the brief did not cover it.
- **`can_finish` on a row that turns `interrupted_ask` through a live event** (rather than a
  timeline load) stays false until the next timeline load. That transition happens at boot,
  before the page connects, so the page loads it fresh.
