<!-- file: docs/plans/2026-08-05-multidisc-apply-canary-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: fa550401-5c6d-4334-a07b-09849b3026fe -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** Authored by an agent on
> 2026-08-05; the adversarial verification pass did not run (the workflow was
> halted by API rate limiting). Treat every code citation as a claim, not a fact.
> The design reasoning and measured production numbers are sound; the code
> references need checking before execution.


# Multidisc apply canary — IMPLEMENTATION PLAN

Design: [`docs/specs/2026-08-05-multidisc-apply-canary-design.md`](../specs/2026-08-05-multidisc-apply-canary-design.md).
Fragment: [`todo.d/20260805_220100_multidisc_apply_canary.md`](../../todo.d/20260805_220100_multidisc_apply_canary.md).

**Sequencing.** Land after `review-queue-recommendations-and-overrides` (design §2.3).
Steps S1–S3 are independent of it and can be built in parallel; S4 and S6 touch files
that workstream also edits.

**Worktree.** `git worktree add ../audiobook-organizer-multidisc-canary -b feat/multidisc-apply-canary`.
Never edit the primary checkout.

---

## 1. Goal

Make it impossible to run a `regroup.multidisc` / `regroup.anthology` apply without a
durable on-disk record of everything it is about to destroy, give the operator a ranked
list so the first applies are a checkable handful, and close the one-click 132-merge path.

---

## 2. Ordered steps

Each step is one commit and one PR-able unit. Each leaves the tree green.

### S1 — `internal/applysnapshot` package (no callers yet)

**Create**

| File | Intent |
|---|---|
| `internal/applysnapshot/snapshot.go` | `Snapshot`, `Member`, `File`, `RiskSignals`, `Diff` types (design §4). Pure data + JSON tags. `SchemaVersion = 1`. |
| `internal/applysnapshot/recorder.go` | `Recorder` interface; `FileRecorder` with `O_APPEND\|O_CREATE\|O_WRONLY` + `f.Sync()` before returning; `Has`, `Path`. `NewFileRecorder(dir, kind string)` errors on empty dir and on `config.UnderFrozenITunesTree(dir)` (design D9). |
| `internal/applysnapshot/dir.go` | `DefaultDir()` = `filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "apply-snapshots")`; returns an error when `DatabasePath` is empty (design D3 / §9.4). |
| `internal/applysnapshot/build.go` | `BuildSnapshot(store database.Store, item database.ReviewItem, payloadJSON string, presentIDs []string, chosenPrimary string) (Snapshot, error)` — resolves members via `GetBookByID` / `GetBookFiles`, normalizes with `database.NormalizeDurationSec`, resolves author/series names via `GetAuthorByID` / `GetSeriesByID` (failures leave the name empty, never an error), computes `RiskSignals`. |
| `internal/applysnapshot/risk.go` | `bookLengthSec = 90 * 60`; `majorityBookLength(long, present int) bool` = `long*2 > present`; `band(RiskSignals) string`. Duplicates `fs_regroup_shape.go` deliberately — design §9.1. |
| `internal/applysnapshot/load.go` | `LoadLast(path string) (map[string]Snapshot, error)` — last record per `ItemID` (design D12). |
| `internal/applysnapshot/*_test.go` | See §4. |

**Note.** `internal/applysnapshot` imports `internal/database` and `internal/config`.
It must **not** import `internal/plugins/maintenance` or `internal/merge` — the gate
depends on it, not the other way round.

**Why first.** It has no callers, so it cannot break anything, and every later step
depends on its types.

---

### S2 — Gate `ApplyMultidisc` on the recorder

**Modify** `internal/plugins/maintenance/regroup_apply.go` (bump header to `1.3.0`,
`last-edited: 2026-08-05`).

- Signature → `ApplyMultidisc(store database.Store, combiner bookCombiner, rec applysnapshot.Recorder) func(context.Context, database.ReviewItem) error`.
- Between `primaryID := pickPrimary(present)` (line 99) and
  `combiner.CombineBooks(...)` (line 100):

```go
if rec == nil {
    return fmt.Errorf("regroup multidisc apply: no snapshot recorder configured — "+
        "refusing to combine %q (%d books): the absorbed rows are hard-deleted and "+
        "the on-disk snapshot is the only record", p.Folder, len(present))
}
snap, serr := applysnapshot.BuildSnapshot(store, item, item.Payload, present, primaryID)
if serr != nil {
    return fmt.Errorf("regroup multidisc apply: build snapshot for %s: %w", item.ID, serr)
}
snap.Origin = "apply-gate"
if serr := rec.Record(ctx, snap); serr != nil {
    return fmt.Errorf("regroup multidisc apply: record snapshot for %s: %w", item.ID, serr)
}
```

- Extend the `DATA-LOSS SAFETY` block in the file header (lines 23-38) with the gate's
  rationale.
- Leave `ApplyVersionGroup` untouched (design D2).

**Modify** `internal/server/wire_handlers.go` (bump header). Inside the existing
`if s.Store() != nil {` block at line 598:

```go
snapDir, derr := applysnapshot.DefaultDir()
if derr != nil {
    return fmt.Errorf("review apply wiring: %w", derr)   // or slog.Error + skip registration
}
snapRec, rerr := applysnapshot.NewFileRecorder(snapDir, "regroup-combine")
if rerr != nil { ... }
combine := maintenanceplugin.ApplyMultidisc(s.Store(), mergeSvc, snapRec)
```

If `wireHandlers` cannot return an error, log at `slog.LevelError` and **do not register
the combine handler at all** — an unregistered handler makes approve fall through to
`"approved"` with a note ([`handler.go:195-207`](../../internal/server/handlers/review/handler.go)),
which is the fail-safe direction. Never register with a nil recorder.

**Post-sibling adjustment.** The two `RegisterApplyHandler(Kind…, combine)` lines become
one `RegisterActionHandler(itunesservice.ActionCombine, combine)`. The `ApplyMultidisc`
call is identical either way (design §2.3).

**Modify** `internal/plugins/maintenance/regroup_apply_test.go` — every existing
`ApplyMultidisc(store, combiner)` call site gains a third argument.

---

### S3 — The canary op

**Create** `internal/plugins/maintenance/multidisc_apply_canary.go`:

- `canaryParams` (design §6.1) — **no `apply` field**.
- `(p *Plugin) multidiscApplyCanaryDef() sdk.OperationDef` per design §6.
- `runMultidiscApplyCanary(ctx, raw, reporter)` dispatching on `Mode`.
- `before`: `store.ListReviewItems` per kind → `registry.RunItems` at
  `runtime.NumCPU()` with `ErrMode: registry.ErrModeCollect` → sort by `ItemID` →
  single-threaded `rec.Record` with `Origin = "canary-before"` → `linkintegrity.Report`
  + `RECONCILE:` line + `report.UnreconciledPhases()` check, mirroring
  [`relink_unlinked.go:113-225`](../../internal/plugins/maintenance/relink_unlinked.go).
- `after`: `applysnapshot.LoadLast` → per-record `Diff` (design §6.3) → band-style
  summary with `ok` / `attention` counts that reconcile against the record count.

**Modify** `internal/plugins/maintenance/plugin.go` — add `p.multidiscApplyCanaryDef(),`
to the `Register` slice ([`plugin.go:32`](../../internal/plugins/maintenance/plugin.go)),
directly after `p.regroupShatteredAIDef()` at line 108. Bump header.

**Create** `internal/plugins/maintenance/multidisc_apply_canary_test.go`.

---

### S4 — Refuse kind-scoped bulk approve while apply is on

**Modify** `internal/server/handlers/review/handler.go` (bump header to `1.2.0`).
Insert the D8 guard immediately after the existing
`if req.Kind == "" && len(req.IDs) == 0` block (lines 267-270), exactly as written in
design §7. Requires adding `"net/http"` to the imports.

**Modify** `internal/server/handlers/review/replay.go` — drive-by fix for the swapped
argument pair at lines 106-108. `RespondWithError` is
`(c, statusCode, message string, code string)`
([`httputil/respond.go:18`](../../internal/httputil/respond.go)); replay.go passes code
then message, so that 409 currently returns `error: "REVIEW_APPLY_DISABLED"` and
`code: "review apply is globally disabled; …"`. Swap them and bump the header. One line,
same function family, worth carrying rather than leaving for someone to trip over while
reading the new guard beside it.

**Modify** `internal/server/handlers/review/handler_test.go` (or a new
`bulk_apply_guard_test.go` if the existing file is crowded) — see §4.

**Conflict note.** The sibling workstream rewrites `bulkRequest`, `bulkResult` and the
dispatch inside this same function. Rebase onto it; the guard is additive and sits above
all of its changes.

---

### S5 — Frontend confirmation on bucket-level approve

**Modify** `web/src/pages/ReviewQueue.tsx` (bump header):

- `handleBulkAction` (line 300): before the `api.bulkReviewAction` call, `window.confirm`
  naming the kind and the bucket's pending count.
- The existing `catch` already routes the error through `addNotification`; the 409's
  message text surfaces unchanged. Verify the API client does not swallow the body.

Cosmetic only — S4 is the enforcement.

---

### S6 — Docs and fragments

- `changelog.d/` fragment (required by `changelog-check.yml`).
- `TODO.md`: tick task #6 only after the §7 gate is observed on prod; do **not** tick on
  merge.
- Executive summary: this qualifies (`docs/process/executive-summaries.md` — "fixes
  something that could have silently caused data loss"). Update the **current month's**
  file in `docs/executive-summaries/` in the same PR as the CHANGELOG fragment.

---

## 3. Files touched — summary

**Create (8)**

```
internal/applysnapshot/snapshot.go
internal/applysnapshot/recorder.go
internal/applysnapshot/dir.go
internal/applysnapshot/build.go
internal/applysnapshot/risk.go
internal/applysnapshot/load.go
internal/plugins/maintenance/multidisc_apply_canary.go
docs/… (this plan + the design, already written)
```

**Modify (6)**

```
internal/plugins/maintenance/regroup_apply.go          # gate + header rationale
internal/plugins/maintenance/regroup_apply_test.go     # third arg at every call site
internal/plugins/maintenance/plugin.go                 # register the op
internal/server/wire_handlers.go                       # construct + pass the recorder
internal/server/handlers/review/handler.go             # D8 bulk refusal
web/src/pages/ReviewQueue.tsx                          # confirm dialog
```

**Tests (4 new)**

```
internal/applysnapshot/recorder_test.go
internal/applysnapshot/build_test.go
internal/applysnapshot/risk_test.go
internal/plugins/maintenance/multidisc_apply_canary_test.go
```

Every created and modified file carries the mandatory 4-line header (path / version /
guid / last-edited); modified files get a version bump.

---

## 4. Test strategy

### 4.1 Unit — the gate is unbypassable

`internal/plugins/maintenance/regroup_apply_test.go`:

| Test | Green means |
|---|---|
| `TestApplyMultidisc_NilRecorderRefusesToCombine` | error returned **and** the fake `bookCombiner` recorded **zero** `CombineBooks` calls |
| `TestApplyMultidisc_RecorderErrorRefusesToCombine` | recorder returns `errors.New("disk full")` → same assertions |
| `TestApplyMultidisc_SnapshotWrittenBeforeCombine` | the fake recorder appends to a shared `[]string` ordering log and the fake combiner appends after it; assert `["record","combine"]` |
| `TestApplyMultidisc_SnapshotCapturesAbsorbedRowsBeforeDeletion` | snapshot's `Members` contains every absorbed book's ID, title, file IDs and durations; the combiner then reports them deleted |
| existing tests | still pass with the third argument |

```bash
go test ./internal/plugins/maintenance/ -run 'TestApplyMultidisc' -race -count=1 -v
```

### 4.2 Unit — recorder durability and refusal

`internal/applysnapshot/recorder_test.go`:

- `TestFileRecorder_AppendsOneLinePerRecord` — two records → two lines, each valid JSON.
- `TestFileRecorder_SurvivesReopen` — reopen the same path, `Has(id)` is true.
- `TestFileRecorder_RefusesFrozenITunesDir` — a dir containing the `books/itunes`
  segment errors (design D9).
- `TestDefaultDir_ErrorsOnEmptyDatabasePath` — with `config.AppConfig.DatabasePath = ""`,
  `DefaultDir()` returns an error, **not** a usable path (design §9.4).
- `TestLoadLast_TakesLastRecordPerItem` — two records for one ID → the later one wins.

```bash
go test ./internal/applysnapshot/ -race -count=1 -v
```

### 4.3 Unit — risk banding refuses to launder absent evidence

`internal/applysnapshot/risk_test.go`:

| Test | Green means |
|---|---|
| `TestBand_AbsentDurationIsNeverGreen` | one member with `DurationEvidence == "absent"` → band is `amber` (or `red`), never `green` — rule 3 |
| `TestBand_MajorityBookLengthIsRed` | 3 of 5 members ≥ 5400s → `red` |
| `TestBand_NormalizedIsAmberNotMeasured` | a member with a millisecond-shaped raw duration bands `amber` and reports `NormalizedMembers == 1` (design D5) |
| `TestBookLengthThresholdIsNinetyMinutes` | asserts the local constant `== 90*60`. ⚠️ This pins the **literal**, not the classifier's `bookLengthSec` — that constant is unexported and unimportable (design §9.1), so a change to `fs_regroup_shape.go` will **not** fail this test. The duplication is real and unguarded; the test only stops *this* copy drifting silently. Say so in the test comment. |

### 4.4 Unit — bulk refusal

`internal/server/handlers/review/` (mirroring `replay_test.go`'s `doReq` helper style):

| Test | Green means |
|---|---|
| `TestBulk_KindScopedApproveRefusedWhenApplyEnabled` | apply gate returns true + a handler registered for the kind → **409**, `BULK_APPLY_REQUIRES_IDS`, and the store saw **zero** `SetReviewItemStatus` calls |
| `TestBulk_KindScopedApproveAllowedWhenApplyDisabled` | gate false → 200, items transition to `approved` (review-only bookkeeping still works) |
| `TestBulk_ExplicitIDsAllowedWhenApplyEnabled` | `ids:[a,b]` → 200 |
| `TestBulk_KindScopedRejectAlwaysAllowed` | `action:"reject"` → 200 in both gate states |

```bash
go test ./internal/server/handlers/review/ -race -count=1 -v
```

### 4.5 Unit — canary op

`internal/plugins/maintenance/multidisc_apply_canary_test.go`:

- `TestCanaryBefore_WritesOneRecordPerHold` — 3 holds in a fake store → 3 JSONL lines.
- `TestCanaryBefore_ReconcilesCounts` — `examined == snapshotted + skipped + errored`;
  a hold with an undecodable payload lands in `skipped` with a reason, never dropped
  (rule 6).
- `TestCanaryBefore_TouchesNoBooks` — the fake store records zero `UpdateBook`,
  `UpdateBookFile`, `CreateBookFile`, `DeleteBook` calls.
- `TestCanaryBefore_ChosenPrimaryMatchesPickPrimary` — smallest ULID among **present**
  members, and a soft-deleted smallest-ULID member is excluded.
- `TestCanaryAfter_DetectsMissingFileID` — remove one file from the survivor →
  `MissingFileIDs` non-empty and `Verdict == "attention"`.
- `TestCanaryAfter_DetectsSurvivingAbsorbedRow` — an absorbed row still resolving →
  `AbsorbedSurvived` non-empty.

### 4.6 Full suite

Per `feedback_storegetter_migration_full_suite_test`, a signature change to a
store-consuming function needs the whole suite, not a subset:

```bash
go build ./...
go test ./... -short -count=1
make ci
```

Green = `go build` clean, no test failures, `make ci` passes its 30% coverage gate.
Frontend: `cd web && npm run build && npm test`.

---

## 5. Deployment

Deploy from the **primary checkout**, not the worktree — `Makefile.local`'s `LOCAL_ROOT`
is hardcoded to it and deploying from a worktree ships main's binary
(`reference_worktree_deploy_gotchas`). After any server-side merge, `git pull --ff-only`
**before** `make deploy` or you ship a stale binary.

```bash
git -C <primary-checkout> pull --ff-only
make deploy
```

`review_apply_enabled` stays **false**. Nothing in this change flips it.

---

## 6. Rollback

| Stage | Rollback |
|---|---|
| Any step, pre-deploy | `git revert` the commit. S1 has no callers; S3 adds an op nothing calls; S4/S5 are additive guards. |
| After deploy, gate misbehaving | Revert S2 only. The op (S3) and the guard (S4) are independent and can stay. |
| After deploy, op misbehaving | Do not run it. It is manual (no `Schedule`), read-only over the library, and its only write is an append to its own JSONL file. |
| Snapshot dir wrong / unwritable | Pass `{"mode":"before","snapshotDir":"<writable path>"}`. For the **gate**, an unwritable dir means applies fail closed — that is correct behavior, not an incident. |
| A canary apply produced a wrong merge | **Do not delete anything** (rule 4). Use the snapshot to re-create the members as separate books and re-associate; a deletion-based "undo" is not idempotent because rescan regenerates any file no `book_file` row claims. |

---

## 7. Dry-run acceptance gate

🔴 **No apply — not one — until every line below is observed and recorded.**

### 7.1 Run the before-pass

The v2 enqueue endpoint is `POST /api/v1/operations/v2`
([`wire_operations_routes.go:27`](../../internal/server/wire_operations_routes.go) →
`opsV2H.TriggerOperationV2`). Its body is `{"def_id": …, "params": …}` and it returns
`202` with `{"op_id": …}` (`operations_v2.go:149-164`). Note `def_id`, **not** `type` —
`POST /api/v1/operations` is a GET-only listing route.

```bash
curl -sS -X POST https://<server>:8484/api/v1/operations/v2 \
  -H "Authorization: Bearer $ABK_TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.multidisc-apply-canary",
       "params":{"mode":"before","kinds":["regroup.multidisc"],"status":"pending"}}'
```

Then poll `GET /api/v1/operations/:id/status` and `…/logs` with the returned `op_id`, and
read the JSONL file.

### 7.2 Numbers that MUST be observed

| # | Metric | Required value | Why |
|---|---|---|---|
| G1 | `holdsExamined` | **132** | Fragment's count. A different number means the queue moved since 2026-08-05 — re-baseline before proceeding, do not assume. |
| G2 | Reconciliation | `examined == snapshotted + skipped + errored`, exactly | Rule 6. `UnreconciledPhases()` fails the op otherwise. |
| G3 | JSONL line count | `== snapshotted` | The file is the deliverable; a short file is a failed run. |
| G4 | `FrozenTreeMembers` summed over all holds | **0** | Rule 5. Nonzero = a stale hold predating the 2026-08-05 frozen-tree fix; STOP. |
| G5a | Holds with `MajorityBookLength == true` | Expect ≈ **9** | The fragment's measured count. Materially higher = the classifier is producing more of the 41-of-43 shape than believed; **STOP** and re-open the classifier question (design §9.3). |
| G5b | Holds with `DistinctSeriesIDs > 1` | **Record only — do not gate** | This signal has never been measured on this queue. Gating on it means tripping a STOP with no baseline to interpret. Report the number; it becomes the baseline for the next run. |
| G5c | Total holds banded **red** | Reported as the sum of G4 + G5a + G5b | The red band is broader than the fragment's measurement, so the aggregate alone is uninterpretable. Always read the three sub-counts, never the total. |
| G6 | `ZeroDurationMembers` summed | Recorded, and each such hold banded non-green | Absent ≠ refuted (rule 3). A green hold with any zero-duration member is a spec violation; STOP. |
| G7 | Holds banded **green** | ≥ 3, else there is nothing safe to canary | With < 3, do not flip the switch. **Predicted cause if this fails:** directory-shaped members carry seeded-zero per-file durations — `relink_unlinked.go:351-354` deliberately leaves per-file duration 0 for the directory shape ("Seeding the BOOK's total onto every track would be actively wrong"), so those members are `absent` and can never be green. The unblock is **task #3** (tier-2 duration probe for the 1,019 review-held directories), *not* a relaxation of this gate. |
| G8 | Every green hold's `Members[].Title`, `DurationSec`, `Files[].FilePath` | Present and non-empty in the JSONL | The fragment's stated minimum. A record missing them cannot serve as the only record. |
| G9 | Every hold's `ChosenPrimary` | Equals the smallest ULID in `PresentBookIDs` | Matches `pickPrimary` ([`regroup_apply.go:364`](../../internal/plugins/maintenance/regroup_apply.go)); a mismatch means the snapshot does not describe what the apply will do. |
| G10 | Book/file mutations during the run | **0** | Confirm via the activity log / op capabilities: the op declares no `CapLibraryWrite`. |

### 7.3 Human step — no automation substitutes for it

Pick **3 to 5** green holds. For each, open the recorded `Files[].FilePath` list and
**listen** to the first ~30 seconds of the first two files. Confirm they are consecutive
parts of one work, not two different books. Record the verdict per hold ID.

The 41-of-43 near-miss was caught by exactly this and by nothing else.

### 7.4 The canary apply

Only after G1–G10 and §7.3:

1. Owner flips `review_apply_enabled` to `true` and restarts.
2. Approve **by explicit ids**, one request, ≤ 5 ids:

```bash
curl -sS -X POST https://<server>:8484/api/v1/review/bulk \
  -H "Authorization: Bearer $ABK_TOKEN" -H 'Content-Type: application/json' \
  -d '{"action":"approve","ids":["<id1>","<id2>","<id3>"]}'
```

   The kind-scoped form is now refused server-side while the switch is on (design D8) —
   attempt it once and confirm the **409** before doing anything else. That is the proof
   the footgun is closed.

### 7.5 The after-pass

```bash
curl -sS -X POST https://<server>:8484/api/v1/operations/v2 \
  -H "Authorization: Bearer $ABK_TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.multidisc-apply-canary","params":{"mode":"after"}}'
```

| # | Metric | Required value |
|---|---|---|
| A1 | `MissingFileIDs` across all diffs | **empty** — every pre-snapshot `BookFile.ID` is on the survivor (design D7) |
| A2 | `DurationAfterSec` vs `DurationBeforeSec` per hold | **equal** |
| A3 | `FingerprintedAfter` vs `FingerprintedBefore` | `after >= before` for every hold |
| A4 | `SurvivorPresent` / `SurvivorSoftDeleted` | `true` / `false` for every hold |
| A5 | `AbsorbedSurvived` | **empty** — every absorbed ID resolves to `(nil, nil)` |
| A6 | `TrackOrderOK` | `true`, or the group-level "already numbered" guard demonstrably fired ([`regroup_apply.go:155-162`](../../internal/plugins/maintenance/regroup_apply.go)) |
| A7 | `ExtraFileIDs` | Recorded and explained per hold (`ensureOwnFile` / `attachVirtualFile` materialization). Not an assertion — design D7. |
| A8 | Verdicts | `ok == records`, `attention == 0` |

Then **listen again** to the merged survivor. If A1–A8 are clean and the audio is right,
widen to the next batch of green holds — still by explicit ids, still in batches, still
with an after-pass each time. Amber holds require the tier-2 duration probe first; red
holds are not approved at all under this plan.

### 7.6 Reporting

Per CLAUDE.md status honesty, every status update on this workstream ends with:

```
COMPLETED: <n> — <hold ids applied and verified>
REMAINING: <n> — <hold ids still pending>
BLOCKED:   <n> — <hold ids red/amber and why>
```

Starting point: `COMPLETED: 0`, `REMAINING: 132`, `BLOCKED: ~9 (book-length members)`.
