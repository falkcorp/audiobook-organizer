<!-- file: docs/plans/2026-08-17-maintenance-jobs-to-v2-ops.md -->
<!-- version: 1.3.1 -->
<!-- guid: 4a71e8c3-92d6-4f15-b03a-7e8d5c1946fb -->
<!-- last-edited: 2026-08-17 -->

# Promote the 37 maintenance jobs to v2 OperationDefs

**Decided by the user 2026-08-17:** the `maintenance.job` bridge is dissolved — each of the
37 jobs becomes its own v2 `OperationDef`. Replacing the legacy resume sweep is a **blocking
prerequisite**, owned here, and must land before the v1 operations table is retired.

## Why this resolves the `MaintenanceJob.Run` question

The two signatures:

```go
// v1  internal/maintenance.MaintenanceJob (37 implementers)
Run(ctx context.Context, store database.Store, reporter ProgressReporter, dryRun bool) error

// v2  registry.OperationDef
Run func(ctx context.Context, params json.RawMessage, reporter Reporter) error
```

**v2 has neither a store parameter nor a dryRun parameter.** So the store-interface audit's one
open question ("should `MaintenanceJob.Run` be redesigned?") is not a separate refactor to
schedule — it is decided by whether the bridge survives. It does not. The 398-method parameter
is **deleted**, not narrowed, as a consequence of this migration.

The 37-file atomic edit the audit warned about still happens; it just happens here, once,
as part of work that was going to touch these files anyway.

## Measured current state (2026-08-17, main = `3ede9161`)

### The bridge

`internal/server/maintenance_job_op.go` registers **one** op-def, `maintenance.job`, which
dispatches all 37 jobs by a `job_id` param and supplies the store itself:

```go
job, _ := maintenance.Get(p.JobID)
store := s.Store()                          // not part of the v2 contract
return job.Run(ctx, store, adapter, p.DryRun)
```

It hardcodes, for all 37: `ResumePolicy: ResumeDrop`, `Liveness: LivenessManual`,
`Timeout: 4h`, `ConcurrencyKey: ""`. **Per-job variation is structurally impossible** while it
exists.

### Resumability — the part that blocks v1 retirement

| | count | files |
|---|---|---|
| Jobs declaring `CanResume() == true` | **9** | `backfill_file_hashes`, `bulk_deluge_import`, `bulk_fetch_metadata`, `cleanup_empty_folders`, `recompute_book_aggregates`, `refetch_missing_authors`, `repair_missing_files`, `retention_and_hygiene`, `scan_composer_tags` |
| …of those, using **legacy** checkpoint storage (`operations.SaveCheckpoint`) | **3** | `backfill_file_hashes`, `recompute_book_aggregates`, `retention_and_hygiene` |
| …of those, using **v2** `reporter.Checkpoint` | **0** | — |
| Jobs declaring `CanResume() == false` | 28 | |

The other 6 resumable jobs checkpoint nothing, so their "resume" already means *re-run from
scratch*.

> **🔴 Correction 2026-08-17 (v1.3.0) — this sentence used to end "…which is exactly
> `ResumeRestart`'s semantics." That was wrong, and it was wrong in the direction that writes
> strike rows and force-drops ops.** See
> [Correction: the 6 are `ResumeRequeue`, not `ResumeRestart`](#correction-the-6-are-resumerequeue-not-resumerestart)
> below. `ResumeRestart` means *reload last checkpoint and call Run again*
> (`internal/operations/registry/types.go:170`); *re-run from zero* is `ResumeRequeue` (`:171`).

Resume for the 9 does **not** use the v2 ResumePolicy mechanism — the bridge drops them.
It runs on a bespoke startup sweep, `resumeLegacyOp` in `internal/server/server_lifecycle.go:261`,
which reads interrupted rows from the **v1 operations table**, matches
`type = "maintenance:<jobID>"` (written by `maintenance_dispatcher.go:154`), checks
`CanResume()`, and re-enqueues through v2.

### The dry-run data-loss history — do not regress this

`maintenance_dispatcher.go:180` persists the operator's resolved `dry_run` via
`operations.SaveParams`. That call exists because resume previously had no record of the choice
and fell through to Go's zero value, **turning an interrupted PREVIEW into a real mutation under
the original operation's own ID**. Seven of the nine resumable jobs advertise `dry_run:true`,
and one of them (`cleanup-empty-folders`) removes directories from disk.

**What makes the migration tractable:** `database.OperationV2Row` already carries a `Params`
field (`internal/database/iface_ops_v2.go:54`), with `UpdateOperationV2Params` to match. v2
already persists enqueued params, so a v2-native resume replays the exact params — including
`DryRun` — **by construction**. The legacy `SaveParams` is only needed by `resumeLegacyOp`; once
resume is v2-native, both it and the legacy row become deletable.

### Two parallel state stores

| | params | checkpoint state |
|---|---|---|
| legacy | `SaveOperationParams(opID, …)` | `GetOperationState(opID)` |
| v2 | `OperationV2Row.Params` | `OpStateV2Row{OperationID, StateBlob}` via `reporter.Checkpoint` |

## Ordered steps

> **Re-sequenced 2026-08-17 after two findings.** The original draft opened with a standalone
> "v2-native resume" PR. Two things killed that:
>
> 1. **`maintenance.ProgressReporter` is 3 methods** — `SetTotal`, `Increment`, `Log`
>    (`internal/maintenance/job.go:30`). **No `Checkpoint`, no `IsCanceled`.** The 3
>    checkpointing jobs do not go through a reporter at all; they call
>    `operations.SaveCheckpoint(store, …)` directly. Migrating them to v2 checkpointing before
>    the bridge is gone would mean widening `ProgressReporter` first — churn that dissolving the
>    bridge then deletes. The v2 `registry.Reporter` (8 methods, including `Checkpoint`) arrives
>    at the jobs *as a consequence* of the migration, so the checkpoint migration rides along
>    with it.
> 2. **The dry-run invariant is already pinned on the legacy path** — four tests:
>    `TestRunMaintenanceJob_PersistsResolvedDryRun`,
>    `TestResumeLegacyOp_MaintenanceJobHonorsSavedDryRun`,
>    `TestAdvertisedDryRunDefault_AgreesWithEveryRegisteredJob`,
>    `TestResumeLegacyOp_NoSavedParamsIncrementsFallbackCounter`. There is nothing useful to add
>    *before* the migration; the gap is that no test asserts the invariant survives **without a
>    legacy row**, and that test cannot be written until the v2 path exists.

**PR-1 — declare per-job execution policy.** Give `MaintenanceJob` (or a sibling interface)
what `OperationDef` needs and `CanResume()` cannot express: `ResumePolicy`, `Timeout`,
`ConcurrencyKey`, `Liveness`, `Capabilities`. Mechanical and behaviour-preserving: every job
declares exactly what the bridge hardcodes today, except the **4** jobs identified in the
correction below. Nothing reads these yet — the bridge still runs. That is deliberate: it makes
PR-2 a wiring change against declarations already reviewed in isolation.

**Declare the policy methods on `MaintenanceJob` itself, not on an optional sibling interface.**
All 37 then fail to compile until each answers, so no job can be silently left unset. An optional
sibling degrades a missed job into `ResumeUnspecified` — which is `= iota` = **0**, the zero value —
and `registry.go:433` rejects that at *registration*, i.e. a server-startup failure instead of a
build failure. Never leave the zero value reachable.

### Correction: the 6 are `ResumeRequeue`, not `ResumeRestart`

The four `ResumePolicy` constants (`internal/operations/registry/types.go:169-173`):

| constant | meaning |
|---|---|
| `ResumeRestart` | reload last checkpoint, call Run again |
| `ResumeRequeue` | re-run from zero (**idempotent ops only**) |
| `ResumeDrop` | abandon on restart, mark `interrupted_dropped` |
| `ResumeAsk` | surface in UI, wait for user choice |

Declaring `ResumeRestart` on a job that never checkpoints is not a naming quibble — it changes
runtime behaviour on two paths:

1. **`uncheckpointed` strikes.** `watchdog.go:156` gates the strike on
   `def.ResumePolicy != ResumeRestart → continue`, and `:159-162` substitutes
   `defaultMinCheckpointInterval` when the def leaves `MinCheckpointInterval` at zero. So the
   strike fires for **every** `ResumeRestart` op, not — as the stale comment at `:154` claims —
   only those that set the field. A 4h job that never checkpoints writes one `op_strikes_v2` row
   per 5-minute window for its whole run.
2. **Forced drop.** `worker.go:158-163` → `checkInfiniteRestart` force-drops any `ResumeRestart`
   op once `ResumeCount >= 3` with `HighWaterProgress == 0` — the permanent state of a job that
   never checkpoints.

Neither path examines `ResumeRequeue`, which is fully wired (`resume.go:69`, `DeleteOpStateV2`,
`server_lifecycle.go:122`). Corrected mapping: **3** checkpointing → `ResumeRestart`; **6**
`CanResume`-but-checkpointless → `ResumeRequeue` *if safe*; **28** → `ResumeDrop`.

### 🔴 …but only 1 of the 6 is safe to declare in PR-1

`ResumeRequeue`'s own doc comment gates it on *idempotent ops only*, and there are **two live,
divergent implementations of the requeue path**. They disagree on exactly the value this plan
exists to protect:

| path | entry | params on re-enqueue | verdict |
|---|---|---|---|
| registry, walks **v2** rows | `Registry.Start` → `resumeAfterStartup` → `resumeRequeue` (`resume.go`) | `Params: row.Params` — **carried forward** | safe |
| server, walks **v1** rows | `resumeInterruptedOperations` → `resumeV2Op` (`server_lifecycle.go:122-127`) | `EnqueueOp(ctx, opType, nil)` — **literal `nil`** | 🔴 unsafe |

Both run at startup, against different tables. The `nil` is deliberate and commented
(`server_lifecycle.go:103-108`: *"the concrete params type is not known at this call site"*), and
for a `library.scan` it is harmless. For a maintenance job it is **bit-for-bit the preview→mutation
data-loss bug this plan is built not to regress**: `DryRun` unmarshals to Go's zero value, `false`.

Today the maintenance v1 row's `Type` is `maintenance:<jobID>`, which is not a registered def ID,
so it falls through to `resumeLegacyOp` — the path that *does* read saved params. That is what
makes it safe today, and it is precisely what PR-2 changes.

Measured `dry_run` default for each of the 9 (from each job's `DefaultParams()`):

| job | checkpoints | `dry_run` default | PR-1 declares |
|---|---|---|---|
| `recompute_book_aggregates` | ✅ | `true` | `ResumeRestart` |
| `retention_and_hygiene` | ✅ | `true` | `ResumeRestart` |
| `backfill_file_hashes` | ✅ | `false` | `ResumeRestart` |
| `bulk_fetch_metadata` | ✗ | *no `dry_run` field* | **`ResumeRequeue`** |
| `bulk_deluge_import` | ✗ | `true` | `ResumeDrop` + comment |
| `cleanup_empty_folders` | ✗ | `true` | `ResumeDrop` + comment |
| `refetch_missing_authors` | ✗ | `true` | `ResumeDrop` + comment |
| `repair_missing_files` | ✗ | `true` | `ResumeDrop` + comment |
| `scan_composer_tags` | ✗ | `true` | `ResumeDrop` + comment |

**Only `bulk_fetch_metadata` has no `dry_run` to lose**, so it is the sole safe `ResumeRequeue` in
PR-1. The other 5 keep `ResumeDrop` — matching what the bridge hardcodes today, so PR-1 stays
behaviour-preserving — each with a comment naming the params gap. Their upgrade moves to **PR-2**,
where the divergence can be resolved and the replay actually tested.

Two of those 5 are the reason this is not a paperwork distinction, and under nil-params requeue
both would silently run for real after a deploy-time restart:

- `cleanup_empty_folders` — `os.Remove(dir)` at `cleanup_empty_folders.go:85`, guarded by
  `if dryRun` at `:82`.
- `repair_missing_files` — rewrites `book_file` rows in place at `repair_missing_files.go:566`
  (`UpdateBookFile`, setting `FilePath` / `OriginalFilename` / `Missing=false` / `FileSize` /
  `Format`), guarded by `if dryRun` at `:532`.

> **⚠️ Correction (v1.3.1).** v1.3.0 of this doc said `repair_missing_files` *deletes* `book_file`
> rows. **It does not** — it has zero delete calls; it **repoints** them. I had conflated two
> different files with near-mirror-image names:
>
> | file | op/job ID | one of the 37? | mutation |
> |---|---|---|---|
> | `internal/maintenance/jobs/repair_missing_files.go` | `repair-missing-files` | ✅ yes | `UpdateBookFile` — repoint |
> | `internal/plugins/maintenance/missing_file_repair.go` | `maintenance.missing-file-repair` | ❌ no, already v2-native | `DeleteBookFilesByIDs` — delete |
>
> The delete lives in the file the **prod-ops lane** runs, not in the one PR-1 touches. The
> `ResumeDrop` decision for these 5 is unaffected — it rests on all 5 advertising `dry_run: true`,
> not on the severity of any one of them.

**PR-2 owes a conformance test.** One fixture, both requeue implementations, assert the resumed
params are equal — the two-implementations pattern that has bitten this repo before. Resolving the
divergence (rather than testing around it) is the better fix; the test is what keeps it resolved.

**PR-2 — dissolve the bridge.** Register 37 op-defs; each job's `Run` becomes
`func(ctx, params json.RawMessage, reporter registry.Reporter) error`.
- The store parameter is **deleted**. Each op-def registration closure captures the store and
  passes **narrow slices** (`internal/maintenance/jobs/store_slices.go`) to the helpers — the
  user's decision, and the option that keeps the narrowing work load-bearing instead of
  decorative. Not `maintenance.GetStore()`: a mutable package global is untestable in parallel.
- `dryRun` is unpacked from `params` per job.
- The **3** checkpointing jobs move from `operations.SaveCheckpoint` to `reporter.Checkpoint`,
  which is reachable for the first time here.
- Port the four legacy dry-run tests to the v2 path, asserting the invariant holds with **no
  legacy row present**. Mutation-test the ported resume test.
- Delete `maintenance.job`.

**PR-3 — retire the legacy writes.** Remove `CreateOperation`/`SaveParams` from
`maintenance_dispatcher.go` and the maintenance branch of `resumeLegacyOp`. **Only after PR-2 is
deployed and a real restart has been observed resuming correctly in prod** — this is the point of
no return for the legacy rows, and the bug it guards against silently converts a preview into a
mutation.

## Test strategy

- Per PR: `go build ./...`, **full-tree** `go vet ./...`, `go test` on every touched package.
- **PR-2's ported dry-run test is the one that matters** — mutation-test it (flip the resumed value
  to `false` and confirm the test fails). A green test here that does not measure is how the
  original bug shipped.
- PR-2: assert all 37 op IDs are registered — `maintenance.All()` count must equal the
  registered count, so a job silently dropped during conversion fails the build's tests, not
  production. 37 is the number to assert against (verified three ways: 37 `Run` receivers, 37
  files, 37 non-test `maintenance.Register` calls).
- ⚠️ **`registry.Reporter` has no mock gate.** It is hand-rolled in **25 test files under 21
  names** and appears **0×** in `.mockery.yaml`; `check-mock-fresh` is inert (0 `//go:generate`).
  This migration touches Reporter usage, so `go build` is the only thing catching drift. Fixing
  that gate is tracked separately and should land early.

## Rollback

PR-1 and PR-2 are independently revertible. **PR-3 is the point of no return** for the legacy
rows — hold it until a restart has been observed resuming correctly in prod.

## Session territory (2026-08-17)

Claimed here: `internal/maintenance/`, `internal/plugins/maintenance/`, `internal/database/`
(+ `mocks/`, `.mockery.yaml`), `internal/operations/registry/`, and in `internal/server/` only
`maintenance_job_op.go`, `maintenance_dispatcher.go`, `server_lifecycle.go`. The other session
stood down from these. Wave 0 errcheck (926 issues, repo-wide) is deferred until after this — it
touches every error return and is the worst possible overlap with a signature refactor.

### The `ResumeDrop` population — measured, and the earlier figures were wrong

Both of the numbers in circulation were undercounts from name-shaped surveys. Structural count on
`main = 3ede9161`, `grep -rn 'ResumePolicy:\s*sdk\.ResumeDrop' internal/plugins/`:

| directory | declarations | files | owner |
|---|---|---|---|
| `internal/plugins/maintenance/` | **36** | 26 | claimed here |
| `internal/plugins/dedup/` | **18** | 18 | ⚠️ **unclaimed by anyone** |
| `internal/plugins/acoustid/` | 5 | 5 | unclaimed (peer explicitly declined) |
| `internal/plugins/{deluge,itunes,metafetch}/` | 3 | 3 | unclaimed (peer explicitly declined) |
| **total** | **62** | **52** | |

Population context: 101 `ResumePolicy:` declarations in total under `internal/plugins/`, of which
18 are already `ResumeRestart` — so the pattern is discriminating, not matching everything.

Two retractions, both the same failure mode (counting by name instead of by structure, cf.
`feedback_naming_grep_is_not_a_census`): the figure **24** was mine and was low by 38, and the
peer's follow-up correction to **62 across 30 files** got the declaration count right but the file
count wrong — it is **52** files, which is what a policy sweep's blast radius is actually measured
in.

**`internal/plugins/dedup/` is the real coordination gap** — 18 files, more than double the 8 the
peer flagged, and neither session named it. It is *not* claimed here; it is recorded so it does not
sit in a mutual-assumption hole.

#### ⚠️ dedup/ is NOT a batch conversion — do not read "18 unowned" as "18 conversions"

The peer session flagged three ops as needing a safety determination before any
`ResumeDrop→ResumeRestart` sweep, and all three verify exactly (file present, one `ResumeDrop`
each, op ID as named):

| file | op ID |
|---|---|
| `auto_resolve.go` | `dedup.auto-resolve` |
| `purge_stale.go` | `dedup.purge-stale` |
| `purge_legacy_fp.go` | `dedup.purge-legacy-fp-candidates` |

`auto_resolve.go` is the one that matters and CLAUDE.md names its exact shape: an auto-merge apply
path must not double-merge a book processed twice, and the prescribed fix is partitioning into
disjoint sets, **not** naive resume. Its `ResumeDrop` is plausibly load-bearing rather than an
oversight.

**But "3 unsafe of 18" is itself an undercount, and the instrument that finds the right number is
already in the code.** Counting mutating calls *inside the op files* is worthless here — measured,
and it fails in both directions: the `Update|Save|Set` family is ~90% `reporter.UpdateProgress(`
and `reporter.SetTotal(`, and `auto_resolve.go` shows **zero** direct store writes because it
delegates everything to `dedup.Engine.AutoResolveCertain`. The op file is not where the writes
live, so a grep scoped to it reports the most dangerous op in the directory as inert.

The declared capability is the right instrument, because it is a property the op asserts about
itself rather than one inferred from its text:

```
grep -l 'sdk\.CapLibraryWrite' $(grep -rl 'ResumePolicy:\s*sdk\.ResumeDrop' internal/plugins/dedup/)
  -> 17 of 18
negative control, CapLibraryRead -> 18 of 18   (so the pattern discriminates: 17 ≠ 18)
```

**17 of the 18 declare `CapLibraryWrite`.** Exactly one — `calibrate_embedding_thresholds.go` — does
not. To be precise about what that does and does not establish: declaring a write is **not** the
same as being non-idempotent, so this is *not* a finding that 17 ops are resume-unsafe. What it does
establish is that under the "convert only long-running **and** idempotent-per-item ops; unsafe or
short ones stay `ResumeDrop` with a comment saying why" rule, a safety determination is the
**default** case in this directory, not the exception. The honest scoping is therefore
**1 read-only + 17 requiring a per-op determination** — not "15 conversions + 3 problems." Whoever
picks this up inherits 17 judgement calls, and a batch sweep here would convert an auto-merge apply
path by default.

One genuine anomaly surfaced by the same table: **`llm_review.go` declares `CapLibraryWrite` with an
empty `ConcurrencyKey`**, while all 16 other writing ops serialize against themselves with a key
matching their op ID. So `dedup.llm-review` can run concurrently with itself while holding a library
write — the same double-mutation hazard CLAUDE.md describes for auto-merge apply paths, but arriving
through the **scheduler** rather than through resume, which is why a resume-policy audit would not
catch it. Filed as `todo.d/20260817-dedup-llm-review-missing-concurrency-key.md`; unowned by either
session.

#### Every figure above reproduces on three independent instruments

Worth stating because two of the three earlier attempts at this census were inert:

| instrument | ResumeDrop | files | dedup write / read-control |
|---|---|---|---|
| `grep` here (a shell function dispatching to **ugrep**) | 62 | 52 | 17 / 18 |
| the peer session's independent run | 62 | 52 | 17 / 18 |
| **Python, no regex engine or shell splitting involved** | 62 | 52 | 17 / 18 |

Python also independently returns `calibrate_embedding_thresholds.go` as the sole non-writing op and
`llm_review.go` as the sole writing op without a non-empty `ConcurrencyKey`.

#### ⚠️ Two shell footguns that silently zero a count — both bite during PR-2's 37-file sweep

1. **zsh does not word-split an unquoted parameter expansion.** Measured in this repo's shell
   (zsh 5.9): `set -- $FILES` → **1** argv; `set -- $(cmd)` → **18**. So
   `grep -l 'X' $FILES` passes the whole newline-joined list as a *single bogus filename*, while
   `grep -l 'X' $(cmd)` works — command substitution *is* split, parameter expansion is not. The peer
   session's first probe returned **0 writing ops**, which would have read as *"no dedup op declares a
   library write, the directory is safe to sweep"* — exactly backwards, about ops that delete rows.
2. **Never put `2>/dev/null` on a counting command.** It converts *"your command was malformed"* into
   *"the answer is zero"*, and those two are indistinguishable downstream.

The habit that caught it: the negative control must **differ** from the positive probe, not merely be
non-empty. `0 and 0` is impossible for a working instrument, because two different questions cannot
return the same answer. Pipe file lists (`xargs grep -l 'X' < list.txt`) rather than interpolating
them.

### Already-v2-native resume, in this territory

`internal/plugins/maintenance/chapters_backfill.go` is its own sdk op-def
(`ID: "maintenance.chapters-backfill"`, line 176; `ResumePolicy: sdk.ResumeRestart`, line 195) and
is **not** one of the 37 bridged jobs — nothing named `chapters_backfill` exists in
`internal/maintenance/jobs/`. So the checkpoint/resume work in #2522 is live code, not inert, and it
is the working reference for what this plan's PR-2 is building toward: `ResumeFrom` watermark
(line 111), `chaptersBackfillCheckpointEvery = 200` (line 124), `CheckpointStateFn` (line 459),
over a sorted enumeration so ordering is a locally testable contract. Reuse this shape rather than
inventing a second one.

### Runtime coupling — not a file conflict

`internal/plugins/maintenance/missing_file_audit.go` and `missing_file_repair.go` are in this
territory, but the peer session runs those ops against **prod on :8484**. The collision is in *time*,
not in the tree: `missing-file-repair` deletes `book_file` rows. **Ping the peer before editing
either file**; it pings before starting an apply.

And read `docs/audits/2026-08-17-orphan-destination-rows-root-cause.md` before rehoming the dry_run
persistence in PR-3. It found that `resolveOrganizedFilePath`
(`internal/organizer/service.go:1254`) silently returns an unverified destination path when neither
source nor destination stats — so some "missing" rows may have bytes under a *different* filename
post-#2479 rather than being genuinely dead, and deleting those is data loss. That is the same
species of defect as the `SaveParams`/`dry_run` bug this plan is careful not to regress: a value
that looks resolved but was never verified.

Stale local branches, content fully upstream (`git cherry` all `-`), safe to delete:
`refactor/retire-operations-v1`, `refactor/server-package-split`.
