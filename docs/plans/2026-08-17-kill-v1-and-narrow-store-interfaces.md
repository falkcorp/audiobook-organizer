<!-- file: docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3f2c9a41-8e77-4d0b-9a15-6c2b84ef1d37 -->
<!-- last-edited: 2026-08-22 -->

# Killing the v1 maintenance stack, then narrowing the store interfaces

**Status: IN PROGRESS.** Phase 1 step 1 landed (#2551). A separate interface-width
sweep ran ahead of phase 2 and changed its premise, and a parallel lane is retiring
the v1 operations row itself — read BOTH updates below before following phases 2–3
as written.

## Update 2026-08-18 — what the width sweep changed about this plan

Nine PRs (#2542, #2545, #2546, #2547, #2549, #2550, #2553, #2554, #2556) took the
`interfacebloat` count from **28 to 5**, measured. Three of this plan's assumptions
did not survive contact:

1. **"Shrinking the sub-interfaces in place would break every consumer."** True for
   `database.BookReader`, but the reachable win was elsewhere: `organizer.Store`
   went from 9 entries / 179 transitive methods to 6 / 22 with **no consumer
   changes**, because the consumers were already only calling 16 of them.
2. **The lever is parameter types, not declarations.** `organizer.Store` embedded a
   30-method `OperationStore` for exactly one reason: seven helper functions in
   `internal/operations/state.go` each took the whole store to call one method.
   #2552 fixed those seven signatures, and the narrowing followed. This mechanism
   is invisible to the linter and is the one worth looking for first elsewhere.
3. **Some of what the gate reported was dead.** `bookHandlerStore` (12 embeds, 182
   transitive methods) and three siblings had zero references outside their own
   file. Phase 2 would have spent effort splitting a declaration nothing used;
   #2554 deleted the file instead.

**Phase 2 item 1 (`JobStore` → per-job interfaces) is unaffected** and remains the
next real step. Phase 3 should be re-measured before starting: the five survivors
are documented in
[`docs/audits/2026-08-18-interface-width-shapes.md`](../audits/2026-08-18-interface-width-shapes.md)
§6, and none of them is a grouping problem.

## Update 2026-08-22 — the v1-operations-row lane, and why it is not mechanical

A parallel lane has been retiring the **v1 `operations` row** itself (distinct from
this doc's maintenance-stack lane, though the two meet at `maintenance_dispatcher.go`).
Merged: #2718, #2719, #2720 (aiscan→v2), #2721 (stranded-row backfill, applied —
1,737 rows), #2722 (v2 result store, an unplanned prerequisite). Open: #2723 (itunes
import/sync), #2724 (itunes path ops).

**Census — count by structure, not by name.** 25 `.CreateOperation(` grep hits is
**24 real call sites**; `legacy_op_status.go:151` is prose inside a doc comment. Of
the 24: **21 twinned**, **3 genuinely unmigrated** (`handlers/diagnostics.go:319`,
`handlers/organize.go:165`, `handlers/organize.go:224`) that need real migrations,
not a sweep. **4 of 24 done.**

### Program steps 5 and 6 are ONE change

The activity writer keys off whatever id it is handed and needs no v1 row to exist.
So "re-key activity to v2 ids" *is* "stop passing `LegacyOpID`", which is only
possible once the row stops being created. They cannot be sequenced.

### Three distinct migration shapes, not one

The plan assumed "swap the id". Measured, each subsystem is one of:

1. **Id swap + status read** (itunes import/sync). Handler returns an id; a status
   endpoint reads the row. Clean 1:1 field mapping v1→v2.
2. **Id swap + a contradicted resume policy** (itunes path ops) — see below.
3. **Latest-of-type scan** (reconcile; `handleGetLatestMetadataFetch`). The reader
   finds its subject by scanning `ListOperations`/`GetRecentOperations(5000)` for a
   v1 **type string**. v2 has no def-filtered lister — only
   `ListOperationsV2Since(since, limit)` — so each needs client-side filtering *and*
   a window choice, plus an explicit call on existing rows (next section).

### The transition-data question every subsystem must answer

Cutting a reader over to v2 hides everything already recorded in v1. Decide per
subsystem and state it in the PR: (a) read v2, fall back to v1; (b) v2-only, accept
the loss; (c) backfill.

itunes chose (b) **on evidence**: a structural enumeration of every non-test
`ResultData` reader (field access — a `Result\b` grep cannot match `ResultData` and
yields a phantom absence) found them to be `batch_poller.go`,
`handlers/diagnostics.go` ×3, `reconcile.go`, and `handlers/operations/handler.go`
— **none of them itunes**, which write no result payload at all.

`handleGetLatestMetadataFetch` will **not** get (b) for free: its picker would show
nothing until a new fetch ran.

### Two live bugs found by doing this, neither predicted by the plan

- **An interrupted iTunes path-repair preview resumed as a real apply** (#2724).
  Two mechanisms governed the same restart and disagreed: the v2 def said
  `ResumeDrop`, while `resumeLegacyOp` re-enqueued. The shim won — and it re-enqueued
  with `nil` params, which `EnqueueOp` normalizes to `"{}"`, decoding to the zero
  struct where `DryRun` is **false**. Maintenance jobs had already been fixed for this
  exact class (`maintenance_dryrun_default_test.go`); iTunes was the untreated twin.
  **Wherever a v1 shim re-enqueues a v2 op, check what it passes for params.**
- **Two dead frontend code paths** (#2723): a poller that stopped only on
  `completed`/`failed`, so cancel and every `interrupted_*` spun forever behind a live
  Cancel button; and a mount-time check comparing against a v1 type string the v2-fed
  store never emits — it exposes the def id's *tail* (`import`, not `itunes_import`).

### Two constraints that bound the order of work

**Stopping v1 row creation must precede dropping `LegacyOpID` wholesale.** Reversed,
new rows get created and never updated — recreating precisely the stranded-row defect
#2721 just repaired.

**The `resumeLegacyOp` shim cannot be deleted when the last subsystem migrates.**
~10k v1 rows remain in the table and the shim still sweeps them on every restart, so
its branches must stay *correct for legacy rows* until the v1 table is dropped
(decision: export, then drop). `legacyV1OpTypes` entries are likewise not dead the
moment a subsystem migrates — only when the table goes.

## The finding that should shape this

I measured the interface landscape before planning, and it inverts the obvious approach.

**250 consumer-side interfaces already exist outside `internal/database`.** Median **2**
methods, mean 3.2, and **87% declare 5 methods or fewer**. Six concepts are already
redeclared per-consumer rather than shared — `OperationsRegistry` in 6 packages,
`WriteBackEnqueuer` in 8.

So the narrow-interface idiom is not something to introduce. **It is already the dominant
idiom in this codebase.** The debt is confined to one package:

| Scope | Interfaces | Shape |
|---|---|---|
| `internal/database` | 57 | 428 direct methods; `Store` composes 40 of them → 398 |
| Everywhere else | 250 | median 2 methods, 87% ≤5 |

That means **there is no "narrow the sub-interfaces" project worth running as a refactor.**
Shrinking `BookReader` (35 methods) or `OperationStore` (30) in place would break every
consumer to reach a shape those consumers already achieve locally by declaring their own.
The work is to stop the wide types being *reachable*, and let the existing idiom fill in.

This is a direct correction to my own earlier framing, which treated "narrow the
sub-interfaces themselves" as the next big push.

---

## Phase 0 — Prerequisite (DONE, #2536 merged 2026-08-17)

Enforce `OperationDef.Permissions` on `POST /operations/v2`.

**Must land before phase 1.** `maintenance_dispatcher.go:95-96` is the only per-job
permission check in the system, and phase 1 deletes it. Mutation-verified: 5 of 6 tests
fail when the gate is disabled.

---

## Phase 1 — Kill the v1 maintenance stack

**Goal:** delete the legacy registry, dispatcher, and `MaintenanceJob` interface. After
#2533/#2534, v2 already owns registration, policy, and execution; v1 is a parallel spine.

**Surface:** 37 jobs across 41 non-test files in `internal/maintenance/jobs/`; **59 files**
reference the v1 registry (`maintenance.All()` / `Register` / `Get`);
`maintenance_dispatcher.go` (200 lines) and `job.go` (238 lines).

**Ordered steps**

1. **Thread `PermissionAware` into the OperationDefs.** One job implements it
   (`bulkFetchMetadataJob` → `library.edit_metadata`); its def currently declares
   `settings.manage`. `TestTriggerOperationV2_BulkFetchMetadataStillDeclaresSettingsManage`
   (added in #2536) pins the current state and is the test that will fail here — that is
   its purpose.
2. **Retire the v1 HTTP routes**, leaving `POST /operations/v2` as the only trigger path.
   Verify with the route table, not a grep.
3. **Delete `maintenance_dispatcher.go`** and the legacy `"maintenance:" + jobID` op type.
4. **Collapse `MaintenanceJob`.** `CanResume()` is already documented as dead-on-arrival
   once v1 goes; `Policy().ResumePolicy` is the live value.
5. **Delete `maintenance.All()` and the package-level registry**, replacing the 59
   references with direct v2 registration.

**Risk, stated honestly:** step 5 is the wide one. Steps 1–4 are independently landable and
each is a real reduction, so this splits into 4 small PRs plus one larger one rather than a
single 59-file change.

**Rollback:** each step is its own PR on a rebase-merge repo; revert is a single revert.

---

## Phase 2 — Make the wide types unreachable (the actual narrowing lever)

Not "shrink `BookStore`". Instead, remove the paths by which a new file can acquire 398
methods without deciding to.

1. **`maintenance.JobStore` (187 methods / 12 interfaces) → per-job interfaces.** This is
   option C from the earlier arbitration, deferred when you chose the shared union. It is
   now cheap per job: each `Run` body is unchanged and already compiles against a bounded
   set, so each job can declare its own 2–5 method interface and the type checker verifies
   it — the same estimate→proof move that made #2534 safe. 37 jobs, mechanical, parallelisable.
2. **A build gate on `database.Store` in new files.** Must be AST/`go-types`, not grep:
   inside `internal/database` the type is spelled `Store`, not `database.Store`, and grep
   undercounts by 15% for that reason (11 seen vs 87 real).
3. **Split `iface_misc.go`** — it holds 25 of the 40 interfaces, including `BookFileStore`
   (27 methods). A file named `misc` is where wide interfaces go to avoid review.

**What phase 2 does NOT do:** delete `database.MockStore`. It has 399 methods across 108
files and satisfies every narrowed interface too, so it survives until its last direct
reference goes. Claiming otherwise was an error I already corrected once; it stays corrected.

---

## Phase 3 — Only if measurement still justifies it

Shrink the `internal/database` interfaces in place. **Defer this.** After phases 1–2 the
question changes from "are these interfaces too wide" to "does anything still consume them
widely", and that is a different, smaller problem. Re-measure before committing.

---

## Test strategy

- **Type checker as the test** wherever a signature changes — it verifies transitively
  reachable helpers that call-site analysis cannot see.
- **Mutation-test every new gate.** A green test proves nothing until it has failed;
  commit before mutating.
- `make ci` currently **cannot pass on `main`** (10 staticcheck findings, 0 introduced by
  recent work). Use `go build ./...` + targeted `go test` as the real gate until that is
  fixed, and do not read a red `make ci` as a signal from these changes.

## Effort

Phase 1: 5 PRs, 4 small + 1 wide. Phase 2: 3 PRs, item 1 parallelisable across 37 jobs.
Phase 3: unscoped by design.
