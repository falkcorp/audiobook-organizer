<!-- file: docs/plans/2026-08-23-retire-last-v1-op-minter.md -->
<!-- version: 2.0.0 -->
<!-- guid: 9bad0e56-17c8-4b14-8010-7a04f6e17c6f -->
<!-- last-edited: 2026-08-23 -->

# Retire the last v1 operation minter (`maintenance_dispatcher.go`)

## Goal

Delete the final live `CreateOperation` call site in the repo
(`internal/server/maintenance_dispatcher.go:158`) so maintenance jobs mint a **v2 row only**,
and return the v2 operation id to the client. No v2→v1 shape mappers; the route becomes
v2-native.

## Measured starting state (at `fceaf51fb`)

| Fact | Value |
|---|---|
| Live `CreateOperation` call sites | **1** (`maintenance_dispatcher.go:158`) — the other 2 grep hits are comments |
| `LegacyOpID` refs (non-test, non-mock) | **45 across 19 files** |
| `maintenanceJobOpParams` refs | 6, **all in `internal/server/`** — not mirrored cross-package, so the compiler catches signature changes |
| Maintenance jobs reading `OperationIDFromCtx` | **8** |
| `DedupeQueuedRuns` non-test setters | **0** |

## The dependency the grep does not show

`LegacyOpID` is not only an id stamp. `maintenance_job_op.go` wires it into the context:

```go
ctx = maintenance.WithOperationID(ctx, p.LegacyOpID)
```

and **8 jobs** read it back out via `maintenance.OperationIDFromCtx(ctx)` to key
`CreateOperationResult` / `GetOperationResults`:
`scan_composer_tags`, `bulk_fetch_metadata`, `backfill_file_hashes`, `repair_missing_files`,
`refetch_missing_authors`, `revert_metadata_fetch`, `recompute_book_aggregates`,
`bulk_deluge_import`.

This coupling travels through a `context` value, so it is invisible to both the compiler and a
`LegacyOpID` grep. It is the reason this is a 6-file change and not a 2-file change.

**Key enabling fact:** the v1 *results* store is keyed by an opID **string** with no foreign key
to an `Operation` row. So results can keep working after the v1 row is gone, provided the
context is re-keyed to the v2 op id.

## Gates measured before writing this plan

**Gate 1 — frontend consumer of `operation_id`: CLEAR.**
`api.runMaintenanceJob()` is typed `Promise<{operation_id: string}>`, but both call sites
(`MaintenanceTab.tsx:765`, `:1190`) **discard the return value**; the optimistic-UI key is a
synthetic `maintenance:${jobId}`, not the server id. Returning the v2 id breaks nothing.

**Gate 2 — the activity block's summary-log read: CLEAR (measured 2026-08-23).**
Initially assumed blocking. It is not. `GetOperationSummaryLog(id string)`
(`pebble_store_operations.go:267`) is **string-keyed**, with no foreign key to an `Operation`
row — the same shape as the results store. Jobs write it from the ctx op id
(`backfill_file_hashes.go:109`, `recompute_book_aggregates.go:206`). So re-keying the context to
the v2 id makes the summary log follow automatically; **no `ReporterSetResult` migration is
needed**. Step 1 shrinks to changing the id source.

Separately verified: `ReporterOpID` returns "" for any reporter lacking `OpID()`, which would
silently blind activity. The production reporter DOES implement it —
`dbReporter.OpID()` at `internal/operations/registry/reporter_db.go:549`. Re-key is safe.

**Gate 3 — two production result routes are typed on the v1 op: BLOCKING.**
`maintenance_fixups.go:487` and `:561` do `GetOperationByID(opID)` then compare
`op.Type` against `"maintenance:scan-composer-tags"` / `"maintenance:repair-missing-files"`,
then `GetOperationResults(opID)`. With no v1 row the lookup 404s and both routes die.
Repoint the *row lookup* to v2 (def id `maintenance.<jobID>`); `GetOperationResults` keeps
working unchanged because it is string-keyed.

## Non-issue, closed by measurement — do not re-investigate

The v1 op type is `"maintenance:"+jobID` (colon) while the v2 def is `"maintenance."+jobID`
(dot), so `opRegistry.Def(op.Type)` in `resumeInterruptedOperations` never resolves and control
always falls to the legacy branch. **This is structural, not a typo:** `RegisterOp` *rejects*
ids containing `:` (asserted by `TestMaintenanceOpIDIsRegistrable`).

Both resume paths therefore fire for one logical maintenance run — v2 `resumeAfterStartup`
(via `container.Start`, `server_lifecycle.go:382`) then v1 `resumeInterruptedOperations`
(`:448`). The second enqueue is absorbed by `EnqueueOp`'s ConcurrencyKey dedupe, which compares
params via `sameParamsIgnoringLegacyID`. Both `resumeRestart` and `resumeRequeue` preserve
`row.Params`, so byte-equality holds.

It does not matter whether a divergence is reachable: **this change deletes the second resume
path outright**, so the answer changes no line below. Closed.

## Blast radius correction

`isResumableOpStatus` accepts only `running` / `queued` / `interrupted*` — **not `pending`**.
The ~1,737 stranded v1 rows recorded in memory are all `pending` and are therefore *not*
re-resumed today and *not* affected by this change. That lane stays separate.

## Files to change

1. `internal/server/maintenance_job_op.go` — re-key ctx + activity to the v2 op id
2. `internal/server/maintenance_dispatcher.go` — delete v1 minting, return v2 id
3. `internal/server/server_lifecycle.go` — delete the `maintenance:` resume branch
4. `internal/server/maintenance_fixups.go` — repoint 2 result routes to the v2 row
5. `internal/operations/registry/` — drop `sameParamsIgnoringLegacyID` **iff** maintenance was its last user
6. tests: `maintenance_job_op_test.go`, `maintenance_dryrun_default_test.go`,
   `maintenance_resume_fallback_metric_test.go`, `server_lifecycle_countgate_test.go`

## Ordered steps

1. **Re-key first (memory mandate).** In `maintenance_job_op.go`, source the op id from
   `opsregistry.ReporterOpID(reporter)` instead of `p.LegacyOpID`; feed that to
   `maintenance.WithOperationID`. Replace the `GetOperationSummaryLog` read with the v2 result.
   Drop the `p.LegacyOpID != ""` guard — the v2 id is always non-empty.
   *Verify:* activity rows still appear, keyed by the v2 id.
2. **Repoint the two fixup routes** to look the op up by v2 row + def id. Do this BEFORE step 3
   so the routes are never broken at any commit.
3. **Delete v1 minting** in `maintenance_dispatcher.go`: `CreateOperation`, `SaveParams`,
   `mergedIntoLegacyOpID`, the orphan `DeleteOperationWithLogs`. Return `v2OpID`.
4. **Delete the `maintenance:` branch** of `resumeInterruptedOperations`, plus the now-dead
   `advertisedDryRunDefault` and `RecordMaintenanceResumeParamsFallback` metric — these exist
   *only* because `database.Operation` has no params field, which the v2 row does have.
5. **Remove `LegacyOpID`** from `maintenanceJobOpParams`. Then check whether any other def still
   writes `legacy_op_id`; if not, delete `sameParamsIgnoringLegacyID` as dead.
6. Update tests; add a regression test asserting the maintenance route returns an id that
   resolves as a **v2** row.

## Test strategy

Per-PR gate (NOT `make ci` — red on main for unrelated staticcheck):

```
go build ./... && go vet ./internal/server/... ./internal/operations/...
go test ./internal/server/... ./internal/operations/... ./internal/maintenance/...
cd web && npx tsc --noEmit
```

Mutation-check the re-keyed activity guard: break the id source and confirm a test fails.
Per memory, assert each mutation anchor is unique before applying it.

## Rollback

Single squashed-scope branch, rebase-merged. Revert the merge commit; v1 minting resumes with
no schema change (nothing is migrated or dropped — v1 rows simply stop being *created*).
Already-existing v1 maintenance rows remain readable throughout.

## Explicitly out of scope

- The 1,737 stranded `pending` v1 rows (separate lane, unaffected — see above)
- `metabatch.ResolveCandidateFetch` / `reconcileV2RowAsOperation` v1 response shapes
- The pre-existing diagnostics `raw_responses`/`suggestions` gap

---

## Executed 2026-08-23 — what changed from the plan above

The plan is kept as written so the corrections below are legible against it.
**Three of its claims did not survive contact.** Two were wrong, and one gate it
never measured turned out to be the real risk.

### 1. `advertisedDryRunDefault` is NOT dead — step 4 was wrong to delete it

Step 4 lists it as "now-dead" alongside the deleted resume branch. It has a
**live caller in `runMaintenanceJob` itself** (`maintenance_dispatcher.go:148`):
it is the fail-safe that makes a bodyless POST honour the `dry_run` the catalogue
advertises. Deleting it would have silently restored Go's zero value — `dry_run:
false` — for the 18 jobs that advertise `true`, including `cleanup-series`, whose
first phase deletes every single-book series and whose names are not recoverable.
Only its resume-path caller died. **The function and its conformance tests are
untouched.**

### 2. `sameParamsIgnoringLegacyID` keeps live users — step 5's condition fails

Step 5 says to delete it "iff maintenance was its last user". It was not:
`server_lifecycle.go:229` and `:240` still construct
`schedulerExtraOpParams{LegacyOpID: opID}` for the `isbn-enrichment` and
`metadata-refresh` legacy-resume branches, which this change does not touch.
`propagateLegacyOpStatus` and the whole `legacy_op_status.go` bridge stay for the
same reason. **Kept.**

### 3. The gate the plan did not measure: what the v1 row was *for*

`maintenance_dispatcher.go:154` justified the v1 row with "so it appears in
active operations / activity bell". Nothing in the plan checked either surface.
Measured before deleting:

| Surface | Reads | Verdict |
|---|---|---|
| `GET /operations/active`, `/operations/recent` | — | **410 Gone** (`server_lifecycle.go:1569-1574`) |
| `GET /operations/timeline` (what the UI uses) | `ListOperationsV2Since` | **v2 only** |
| Activity bell | `database.ActivityEntry`, `OperationID string` | **no FK to any operations row** |

Both halves of the justification were false. The v1 row was invisible to every
surface it was said to feed — a stale comment that outlived its reason, exactly
the pattern CLAUDE.md's worked example describes. Its only real readers were the
resume sweep, the status mirror, and the two fixup routes.

### 4. Ordering: steps 1 and 3 had to ship together

The plan orders 1 → 2 → 3 and frames the hazard as the routes 404ing. That misses
what step 1 does **alone**: after the re-key the 8 jobs write results under the v2
id while the unchanged dispatcher still returns the v1 id, so a run in that window
resolves its row fine and returns **zero results** — silent, and not something the
v1 fallback catches. Landed as **2, then 1+3 together**, then 4 → 5.

### 5. `JobID` kept, its comment corrected

Its stated justification ("resume reads params written by an older build via
`operations.SaveParams` / `LoadParams`") became false when steps 3 and 4 deleted
both call sites. The field is now **read by nothing**: the job is captured in the
Run closure, and EnqueueOp's dedupe is scoped to a single def
(`registry.go`, `if op.DefID != defID { continue }`), so params cannot conflate two
jobs. Kept as the human-readable record in a params blob, with the comment
rewritten to say that rather than the old reason. Removing it is a candidate for a
separate change — it predates this lane.

### 6. Extra scope taken, with reasons

- **`ServerOpsStore` narrowed.** `CreateOperation` and `DeleteOperationWithLogs`
  lost their last non-test callers in `internal/server`. Leaving them advertised
  on the server's own store interface is the stale-surface problem this lane
  exists to end. Their removal also undoes the `serverOperationWriter` split,
  which existed only because `DeleteOperationWithLogs` was its 9th method and
  tripped `interfacebloat`'s cap of 8; at 7 it is one leaf again.
- **The `maintenance_resume_params_fallback_total` changelog fragment was deleted,
  not amended.** The metric is unreleased, so announcing a counter that will not
  exist is worse than saying nothing. No alert rule or dashboard referenced it.

### 7. Where the deleted tests' invariants went

Nothing was dropped; two moved keyspace.

| Deleted / rewritten | Invariant now lives in |
|---|---|
| `TestResumeLegacyOp_MaintenanceJobHonorsSavedDryRun` | `TestResume_PreservesParamsAcrossRestartAndRequeue` (`internal/operations/registry`) — pins params preservation across **both** resume policies |
| `TestRunMaintenanceJob_MergedRequestLeavesNoOrphanRow` | `TestRunMaintenanceJob_MintsV2RowOnly` — pins the invariant that makes the cleanup unnecessary |
| `maintenance_dedupe_test.go` "differing only in LegacyOpID" arm | `TestSameParamsIgnoringLegacyID` (`internal/operations/registry`), beside the enqueue sites that still stamp one |

Every new guard was mutation-checked: the v1 fallback, the v2 def-id check, the
no-v1-row invariant, the no-second-resume assertion, and both arms of params
preservation each fail under their own mutant.

### Still open, deliberately out of scope

- The **1,737 stranded `pending` v1 rows** — `isResumableOpStatus` accepts only
  `running`/`queued`/`interrupted*`, so they are neither swept today nor affected
  by this change. Separate lane.
- Removing `maintenanceJobOpParams.JobID` (see 5).
- `sameParamsIgnoringLegacyID` and the `legacy_op_status.go` bridge, which retire
  when the scheduler's two legacy-resume branches do.
