### 🧩 Queued-op consolidator — collapse N queued runs of one def into a single merged op

Owner decision 2026-08-22: **do B now, then build this.**

- [ ] **B (do first):** give each maintenance def a per-job `ConcurrencyKey` so a job can never run
      concurrently with itself. One field in `registerMaintenanceJobOp`
      (`internal/server/maintenance_job_op.go`): `ConcurrencyKey: maintenanceOpID(jobID)`. Do NOT
      also set `DedupeQueuedRuns` — dropping a second request silently is the bug #2688 fixed, and
      `maintenanceJobOpParams` carries `DryRun`, so a "run for real" clicked during a dry-run would
      vanish and report success. Needs a test that two enqueues produce two SEQUENTIAL runs, and a
      mutation check that removing the key lets them overlap.

- [ ] **Then: the consolidator.** Once ~3–4 ops for the same def are QUEUED, open one new op whose
      params are the merge of theirs, and close the originals. **Queued only — never touch a
      RUNNING op**, which has already done work.

#### Why this is not just `OperationDef.Batchable`

The registry already has batching (`types.go:124-155`): `Batchable` buckets a call's *subject*
before any row exists, returns `("", nil)`, and flushes on a debounce (`BatchWindow`) capped by
`BatchMaxWait`. Close, but the wrong shape for this ask. Batching coalesces *before* the op is
real; the consolidator coalesces rows the user has already seen in Active Operations. Whether the
right build is "extend Batchable to a post-enqueue mode" or "a separate consolidator pass" is open
— but the difference in visibility is the reason it cannot just be `Batchable: true`.

#### The constraint that decides the design: op-ID identity

`EnqueueOp` returns an ID and **callers retain it**: `internal/plugins/maintenance/optimize.go:148`
captures `childID` and waits on it; `internal/scheduler/tasks.go` captures `v2ID` at :134, :173,
:194, :253, :316, :337, :364. If a consolidator closes those rows, every holder is watching a dead
op — a wait that never returns, a UI row that vanishes.

`Batchable` dodges this honestly by returning `""` up front: "no ID yet." A consolidator cannot —
it has already handed out IDs. So it needs one of:

1. **A `superseded_by` pointer on the closed rows**, with the ops API and the activity feed
   following it. Preferred: the waiter follows the redirect, and the UI can say "merged into op X"
   instead of dropping a row. This is the honest version of the feature.
2. Restricting consolidation to defs whose callers provably never retain the ID. Narrower, and the
   proof rots the first time someone adds a caller.

#### Merge semantics must be per-def. There is no safe generic default

`book_ids: [...]` unions obviously. `dry_run: bool` does not — merging `true` and `false` is a
policy choice, and choosing wrong turns a preview into a mutation. That is the same hazard
`maintenance_dispatcher.go` already documents for resume (seven jobs are both `CanResume()` and
advertise `dry_run: true`; `cleanup-empty-folders` removes directories from disk).

So: the def supplies a merge function, and **a def with no merge function is not consolidatable**.
Refuse rather than guess. A generic "last write wins" or "OR the booleans" default would be a
data-loss bug wearing a convenience feature's clothes.

#### Other things the build must settle

- Trigger: a count threshold (3–4), a time window, or both? `BatchWindow`/`BatchMaxWait` already
  model the time half — reuse the vocabulary rather than inventing a second one.
- Every close must be journaled with the replacement ID. An op that disappears without a record is
  the silently-discarded-request failure again, just later in the pipeline.
- Interaction with `ConcurrencyKey` from B: consolidation operates on the queue that Gate 3 builds
  up, so B is a prerequisite, not merely "first" — without a key there is no queue to consolidate,
  because everything dispatches immediately.

#### Update, same day: scope this down — most of it already exists

Owner refinement: consolidate only ops whose **parameters are identical**, and otherwise just
block so they run sequentially. That is a different, much smaller feature than the one sketched
above, and **`EnqueueOp` already implements it** as of #2688: it reuses an active op when
`bytes.Equal(rawParams, op.Params)`, and queues a second row otherwise, which Gate 3 then
serializes. Identical params also dissolves the merge-function problem — if the params are the
same, the merged op's params *are* the params; nothing needs merging.

The only reason this does not work today is `LegacyOpID`: a fresh ULID per request that makes
"same parameters" never true. That field exists solely to bridge back to v1, and
`docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md` **Phase 1 step 3 deletes
`maintenance_dispatcher.go`**, the only thing that writes it. That plan is IN PROGRESS (step 1
landed as #2551).

**Revised plan:**

1. `ConcurrencyKey` per maintenance def (item B above) — still needed; without it nothing queues.
2. Finish v1 retirement, Phase 1 steps 2–3. `LegacyOpID` disappears with the dispatcher.
3. Re-measure. Same-params dedupe and different-params serialization should both work with no new
   code.

Only build a consolidator if step 3 shows a real gap. The `superseded_by` redirect and the per-def
merge function above are **not** needed for the same-params-only version — do not build them
speculatively. Keep the notes: they apply if a general merge is ever wanted, and they record why
"just OR the booleans" is unsafe.
