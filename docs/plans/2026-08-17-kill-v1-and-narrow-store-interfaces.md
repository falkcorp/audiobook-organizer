<!-- file: docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f2c9a41-8e77-4d0b-9a15-6c2b84ef1d37 -->
<!-- last-edited: 2026-08-17 -->

# Killing the v1 maintenance stack, then narrowing the store interfaces

**Status: PROPOSED — awaiting approval. Nothing in phases 1–3 has been started.**

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

## Phase 0 — Prerequisite (IN FLIGHT, PR #2536)

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
