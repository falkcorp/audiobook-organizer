<!-- file: docs/plans/2026-08-05-duplicate-detect-version-group-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0bf1d605-f8e1-49dc-984b-fd4b0c33389e -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** This document was authored by
> an agent on 2026-08-05 but the adversarial verification pass (which grep-verifies
> that every cited function, struct field, op ID and file path actually exists) did
> NOT run — the workflow was halted by API rate limiting before stage 2.
>
> **Treat every code citation as a claim, not a fact.** The most common failure mode
> in generated plans is a confidently-cited symbol that does not exist. Verify before
> executing. The design reasoning and the measured production numbers are still sound
> and were drawn from real observations; the code references are what needs checking.


# Implementation plan — Duplicate detection, combine-by-template, version-group

**Slug:** `duplicate-detect-version-group`
**Design:** [`docs/specs/2026-08-05-duplicate-detect-version-group-design.md`](../specs/2026-08-05-duplicate-detect-version-group-design.md)
**Read the design first.** Every locked decision referenced below as `LD1`…`LD10` is defined there.

---

## 0. Worktree setup (mandatory — CLAUDE.md worktree discipline)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git worktree list                       # confirm you are NOT about to edit main
git fetch origin main
git worktree add .worktrees/dupmatch -b feat/duplicate-detect-version-group origin/main
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/dupmatch
```

Every path below is relative to that worktree root. Every file created or modified needs
a version header (`// file:` / `// version:` / `// guid:` / `// last-edited:` for Go,
`<!-- ... -->` for MD/TS) and a version bump on modification.

Each step below is one commit and one reviewable PR. Steps 1–3 are pure additions that
touch nothing existing; step 4 is the only refactor of shipped code; steps 5–7 wire it up.

---

## Step 1 — `internal/dupmatch`: the pure matcher

**Commit:** `feat(dupmatch): duration-template matcher for redundant-copy detection`

### Files to create

| File | Intent |
|---|---|
| `internal/dupmatch/match.go` | `DurSource`, `Track`, `Tier`, `Assignment`, `Skip`/`SkipReason`, `TemplateResult`, `Config`, `DefaultConfig()`, `MatchTemplate`. Exactly the shapes in design §3.1. No imports beyond `sort`, `strings`, `fmt` — **no `database`, no `os`**, so the package cannot grow I/O by accident. |
| `internal/dupmatch/histogram.go` | `DeltaHistogram` (`map[int]int`) with `Add`, `Render`, `FractionWithin`. `Render` emits fixed buckets `0,1,2,3,4,5,6-10,>10` with counts and cumulative percentages, deterministic ordering. |
| `internal/dupmatch/primary.go` | `PrimaryCandidate`, `PickMostComplete` — LD6: order by `MatchedTracks` desc, then `FileCount` desc, then `BookID` asc (earliest ULID). |
| `internal/dupmatch/match_test.go` | Table tests, see below. |
| `internal/dupmatch/histogram_test.go` | Bucketing + `FractionWithin` boundaries. |
| `internal/dupmatch/primary_test.go` | The full D2 ordering incl. both tie-break levels. |

### Non-obvious implementation notes

- `MatchTemplate` must be **deterministic**: sort candidate pairs by the total order
  `(tierRank, |delta|, -FileSize, -DurSec, DebrisFileID)` before the greedy assign. There
  must be no `range` over a map in any code path that affects output ordering.
- The histogram is fed **every** considered delta within `ProbeToleranceSec`, accepted or
  not. That is what makes it usable for calibration — a histogram of only accepted
  matches is circular.
- `Source == SourceUnknown` short-circuits to `SkipNoDuration` before any comparison
  (LD2). Write the assertion as an explicit early branch, not as a numeric guard, so it
  survives refactoring.
- `MaxBucketFanout` is evaluated per **debris track** (count of candidate slots), not per
  index bucket, so the matcher stays independent of how the caller indexes.

### Tests — what green means

```bash
go test ./internal/dupmatch/... -race -run 'TestMatchTemplate|TestPickMostComplete|TestDeltaHistogram' -v
```

Required cases (each an explicit named subtest):

1. `TestMatchTemplate_SuccessorsFragment` — reference = 13 synthetic tracks; debris = the
   four real durations `2474, 1620, 3018, 868` from the measured case
   (`.claude/notes/2026-08-05-unlinked-books-investigation.md:96-99`) → assignments to
   slots `3, 4, 6, 13`, zero skips, `MissingSlots` contains the other nine.
2. `TestMatchTemplate_UnknownDurationNeverMatches` — a debris track with
   `Source: SourceUnknown` and a `DurSec` that exactly equals a slot → `SkipNoDuration`,
   **zero** assignments. Guards LD2.
3. `TestMatchTemplate_MixedSourceRequiresCorroborator` — fingerprint reference vs
   container debris, delta 0, no corroborators → dropped; same pair with
   `TitleStem` equal → `TierProbable` assignment.
4. `TestMatchTemplate_RedundantLoserPrefersLongerLarger` — two debris files at delta 0 on
   one slot, differing `FileSize` → the larger wins, the smaller carries
   `Redundant: true` and keeps `RefTrackIndex`.
5. `TestMatchTemplate_AmbiguousBucketSkipped` — 100 identical-duration slots with
   `MaxBucketFanout: 64` → `SkipAmbiguousBucket`, not a silent first-match.
6. `TestMatchTemplate_BelowMinLength` — a 30 s debris file with `MinMatchableSec: 60` →
   `SkipTooShort`.
7. `TestMatchTemplate_Deterministic` — run the same input 50 times, assert byte-identical
   JSON of the result.
8. `TestMatchTemplate_Reconciles` — for a randomised input, assert
   `len(uniqueDebrisIDs) == len(nonRedundantAssignments) + len(redundantAssignments) + len(Skipped)`.
9. `TestPickMostComplete_D2Ordering` — `{A: 12 slots}` vs `{B: 13 slots}` → B;
   equal slots + differing file counts → higher file count; both equal → smaller ULID.

Green = all pass under `-race`, and `go vet ./internal/dupmatch/...` is clean.

---

## Step 2 — `maintenance.detect-redundant-copies`: the detector op

**Commit:** `feat(maintenance): detect-redundant-copies dry-run detector`
**Depends on:** Step 1.

### Files to create

| File | Intent |
|---|---|
| `internal/plugins/maintenance/detect_redundant_copies.go` | The op def, params, the five phases (design §4.1), the duration index, hold payload marshalling, `UpsertReviewItem`, the `RECONCILE:` line and the histogram render. |
| `internal/plugins/maintenance/detect_redundant_copies_test.go` | Phase-level tests against a fake store. |

### Files to modify

| File | Change |
|---|---|
| `internal/plugins/maintenance/plugin.go` | Register `p.detectRedundantCopiesDef()` in the op list. Put it next to `p.relinkUnlinkedBooksDef()` (line 83) under the reconcile group, with a one-line comment saying it is tier 2 and consumes what relink produced. Bump the file's version header. |

### Non-obvious implementation notes

- **Concurrency is mandatory** (CLAUDE.md): phases 2 and 4 use
  `registry.RunItems(ctx, reporter, ids, fn, registry.RunItemsOptions{Concurrency: runtime.NumCPU(), ProgressTotal: len(ids), ErrMode: registry.ErrModeCollect, Label: ...})`
  — copy the call shape from `internal/plugins/maintenance/relink_unlinked.go:117-156`.
  Results are appended under a `sync.Mutex`; **all writes happen in single-threaded phase 5**,
  so two workers can never touch the same row.
- **Frozen-iTunes exclusion** uses `config.UnderFrozenITunesTree` on both
  `BookFile.FilePath` and `BookFile.ITunesPath`, mirroring
  `internal/plugins/maintenance/regroup_shattered_ai.go:191`. Applied to **both** the
  debris and the reference candidate sets (LD3), with a per-reference `excludedFrozen`
  list in the payload.
- **Duration source resolution** is one small helper:
  ```go
  func trackDuration(bf database.BookFile) (int, dupmatch.DurSource) {
      if bf.AcoustIDFingerprintDurationSec > 0 {
          return int(math.Round(bf.AcoustIDFingerprintDurationSec)), dupmatch.SourceFingerprint
      }
      if d := database.NormalizeDurationSec(bf.FileSize, bf.Duration); d > 0 {
          return d, dupmatch.SourceContainer
      }
      return 0, dupmatch.SourceUnknown
  }
  ```
  `NormalizeDurationSec` is at `internal/database/duration_sanity.go:61`; skipping it
  would let a millisecond-valued row (~1.9 % of history) match nothing and be counted as
  a real negative.
- **Index entries are the slim struct from design §5**, never `database.BookFile`.
- `HistogramOnly: true` runs phases 1–4 and logs the histogram, then returns before any
  `UpsertReviewItem`. This is the calibration mode and it is what the acceptance gate
  runs first.
- The op must **not** declare `sdk.CapFilesRead` (LD5).
- Reconcile assertion is in-process: build the counters, compare, and
  `return fmt.Errorf(...)` if they do not tie (LD10).

### Tests — what green means

```bash
go test ./internal/plugins/maintenance/... -race -run 'TestDetectRedundantCopies' -v
```

Required cases:

1. `TestDetectRedundantCopies_EmitsHoldForSuccessorsShape` — fake store with one 13-file
   reference and 11 debris books/17 files whose durations match 12 slots →
   exactly **one** `UpsertReviewItem` call, `Kind == "firstaid.redundant-copy"`,
   payload `coveredTracks` length 12, `missingTracks == [7]`, `internallyRedundant`
   length 5.
2. `TestDetectRedundantCopies_SkipsFrozenITunesReference` — reference under
   `.../books/itunes/...` → zero holds, `skipped-frozen` counted.
3. `TestDetectRedundantCopies_SkipsFrozenITunesDebris` — 10 frozen debris rows appear in
   `excludedFrozen` and are **not** in `debrisBookIds`.
4. `TestDetectRedundantCopies_SingleFileDebrisNeedsCorroborator` — one debris file, one
   slot, no corroborator → no hold, `SkipNoCorroborator` counted; add a matching title
   stem → hold emitted.
5. `TestDetectRedundantCopies_ZeroWritesToBooks` — the fake store fails the test if
   `UpdateBook`, `UpdateBookFile`, `CreateBookFile`, `DeleteBook`, or
   `MoveBookFilesToBook` is called at all. This is the "dry-run means dry-run" assertion.
6. `TestDetectRedundantCopies_Reconciles` — the `RECONCILE:` counters tie and the op
   returns nil; a deliberately-broken counter makes it return an error.
7. `TestDetectRedundantCopies_HistogramOnly` — zero `UpsertReviewItem` calls, histogram
   present in the reporter log.
8. `TestDetectRedundantCopies_Idempotent` — running twice produces the same `DedupKey`
   and the same payload bytes.

Green = all pass under `-race`; no book/file mutation in any test.

---

## Step 3 — Frontend: label + payload typing

**Commit:** `feat(web): review-queue label and payload fields for firstaid.redundant-copy`
**Depends on:** nothing (can land in parallel with 1–2).

| File | Change |
|---|---|
| `web/src/lib/reviewKinds.ts` | Add `'firstaid.redundant-copy': 'Redundant copies (assembled original exists)'` to `REVIEW_KIND_LABELS` (currently lines 10-15). Version → 1.1.0. |
| `web/src/lib/reviewPayload.ts` | Add optional typed fields `referenceBookId`, `referenceTitle`, `referenceTrackCount`, `coveredTracks`, `missingTracks`, `debrisBookIds`, `internallyRedundant`, `excludedFrozen` to `ReviewPayload`; extend `memberIDs()` (line 46) with a `debrisBookIds` fallback so `memberCount()` renders. Version → 1.1.0. |
| `web/src/lib/__tests__/reviewPayload.test.ts` | Add a case parsing a real `firstaid.redundant-copy` payload and asserting `memberIDs()` returns the debris IDs. |

```bash
cd web && npm run test -- src/lib/__tests__/reviewPayload.test.ts
```

Green = the new case passes and no existing case changed.

---

## Step 4 — Extract `linkVersionGroup` (the only refactor of shipped code)

**Commit:** `refactor(maintenance): extract linkVersionGroup with injected primary selection`
**Depends on:** nothing. **Land this before step 5.**

| File | Change |
|---|---|
| `internal/plugins/maintenance/version_group_link.go` (new) | `linkVersionGroup(store, memberIDs, pick, logKV...) (groupID, primaryID string, err error)` carrying, verbatim, the three invariants from `regroup_apply.go`: existing-group reuse (lines 222-224, 246-252), cross-group refusal (236-245), stale-primary demotion (286-312). Plus `pickEarliestULIDBook([]*database.Book) string`. Re-fetch-and-patch only; mutate **only** `VersionGroupID` and `IsPrimaryVersion`. |
| `internal/plugins/maintenance/regroup_apply.go` | `ApplyVersionGroup` (line 188) becomes a thin wrapper: decode payload → `linkVersionGroup(store, p.MemberBookIDs, pickEarliestULIDBook, "item", item.ID, "folder", p.Folder)`. Delete the inlined logic. Version header → 1.3.0. |

**This step must change zero behaviour.** The proof is that `regroup_apply_test.go` is
**not edited** and still passes:

```bash
go test ./internal/plugins/maintenance/... -race -run 'TestApplyVersionGroup|TestApplyMultidisc' -v
git diff --stat internal/plugins/maintenance/regroup_apply_test.go   # must be empty
```

Green = every existing `ApplyVersionGroup` test passes with an unmodified test file. If a
test needs editing, the extraction changed behaviour — stop and fix the extraction.

---

## Step 5 — `ApplyRedundantCopy`: the fixer

**Commit:** `feat(maintenance): ApplyRedundantCopy — combine debris by template, then version-group`
**Depends on:** Steps 1, 2, 4.

| File | Change |
|---|---|
| `internal/plugins/maintenance/redundant_copy_apply.go` (new) | `ApplyRedundantCopy(store, combiner)` per design §4.2 (10 ordered steps), plus `applyTemplateTrackNumbers` per §4.3. Reuses `presentMembers` (`regroup_apply.go:338`), `pickPrimary` (`regroup_apply.go:364`) and the `bookCombiner` interface (`regroup_apply.go:68`) — do not duplicate or widen any of them. |
| `internal/plugins/maintenance/redundant_copy_apply_test.go` (new) | The invariant suite below. |

### Non-obvious implementation notes

- `CombineBooks(present, survivorID, **nil**)` — the nil override is load-bearing. A
  non-nil override is the only thing that makes `merge.Service` call `UpdateBook` on the
  survivor (`internal/merge/service.go:444-457`).
- `applyTemplateTrackNumbers` has **no** group-level "already numbered → bail" guard, and
  its doc comment must say why, citing `relink_unlinked.go:355` (LD7). Reviewers will try
  to reunify it with `applyDiscTrackNumbers`; the comment is the defence.
- Redundant and skipped files are written `TrackNumber = 0, DiscNumber = 0` — "position
  unknown, deliberately".
- The step-3 guard (`referenceID ∉ debrisBookIds`) and the step-4 frozen-tree re-check
  both run **before** any store write.

### Tests — what green means

```bash
go test ./internal/plugins/maintenance/... -race -run 'TestApplyRedundantCopy' -v
```

Required cases:

1. `TestApplyRedundantCopy_ReferenceNeverInCombineCall` — the fake combiner records its
   `bookIDs` argument; assert the reference ID is absent and that `primaryID` is a debris
   ID. **This is the LD4 guard and the single most important test in the workstream.**
2. `TestApplyRedundantCopy_RefusesReferenceInDebrisList` — hand-corrupted payload with
   the reference listed as debris → error, and the fake store records **zero** writes.
3. `TestApplyRedundantCopy_ReferenceStaysPrimary` — after apply, reference has
   `IsPrimaryVersion == true` and the debris survivor `false`; both share one
   `VersionGroupID`; neither is soft-deleted.
4. `TestApplyRedundantCopy_PreservesFingerprintAndAuthor` — survivor `BookFile` rows keep
   `AcoustIDFingerprint`; the reference `Book`'s `Author`, `Series` and `Narrator` are
   byte-identical before and after. Guards the repo's dominant incident class.
5. `TestApplyRedundantCopy_NumbersOverPreExistingRelinkTrackNumbers` — survivor files
   arrive carrying `TrackNumber = 1..n` from relink; assert they are **rewritten** to the
   template slots. This is the regression that reusing `applyDiscTrackNumbers` would
   cause (LD7).
6. `TestApplyRedundantCopy_RedundantFilesGetTrackZero` — the 5 internally-redundant files
   end at `TrackNumber == 0` and are counted in the returned `leftUnnumbered`.
7. `TestApplyRedundantCopy_Idempotent` — apply twice; second run performs zero combines,
   reuses the same `VersionGroupID`, and writes no `UpdateBookFile` (per-file values
   already equal).
8. `TestApplyRedundantCopy_SoftDeletedDebrisSkipped` — a `MarkedForDeletion` debris book
   is never passed to `CombineBooks` and never made primary.
9. `TestApplyRedundantCopy_RefusesFrozenITunesMember` — reference or debris under
   `books/itunes/**` → error, zero writes.
10. `TestApplyRedundantCopy_RefusesCrossVersionGroups` — reference in group A, survivor in
    group B → error from `linkVersionGroup`, zero `UpdateBook` calls.
11. `TestApplyRedundantCopy_SingleDebrisBookSkipsCombine` — one debris book → no
    `CombineBooks` call at all, straight to numbering + version-group.

Green = all 11 pass under `-race`, plus the whole package:

```bash
go test ./internal/plugins/maintenance/... -race -short
```

---

## Step 6 — Wire the apply handler

**Commit:** `feat(server): register the firstaid.redundant-copy apply handler`
**Depends on:** Step 5.

| File | Change |
|---|---|
| `internal/server/wire_handlers.go` | Inside the existing `if s.Store() != nil` block (lines 598-608), add `reviewH.RegisterApplyHandler("firstaid.redundant-copy", maintenanceplugin.ApplyRedundantCopy(s.Store(), mergeSvc))`. Reuse the `mergeSvc` already resolved on line 600. Extend the block comment to say the handler is inert until `config.ReviewApplyEnabled` is flipped. Bump the version header. |
| `internal/plugins/maintenance/kinds.go` (new, ~20 lines) | Export `const KindRedundantCopy = "firstaid.redundant-copy"` with a comment marking it LOAD-BEARING (the frontend maps it verbatim, mirroring `fs_regroup_shape.go:68-74`). Use it from the detector, the apply wiring and the tests — no string literals in three places. |

```bash
go build ./... && go test ./internal/server/... -race -short
```

Green = build clean, server tests pass. **Note:** this step changes nothing observable in
prod — `config.ReviewApplyEnabled` defaults OFF and is not set in prod
(`internal/server/handlers/review/handler.go:16-23`), so approving a hold records the
decision and executes nothing.

---

## Step 7 — Docs and fragments

**Commit:** `docs(dupmatch): changelog + todo fragments for redundant-copy detection`

| File | Change |
|---|---|
| `changelog.d/<scriv-generated>.md` | Required by `changelog-check.yml` CI. Run `scriv create`. |
| `todo.d/20260805_*_first_aid_library_validate_repair.md` | Do **not** edit (add-only fragment system). Tick the sub-task in `TODO.md` directly once merged — that is a normal direct edit. |
| `docs/executive-summaries/2026-08.md` | Qualifies: wide blast radius (merge path + review queue) and a data-corruption-adjacent class. Prepend, never replace. |

```bash
make ci
```

Green = `make ci` passes (mocks / staticcheck / short tests / 30 % coverage gate).
**Known:** local `make ci` staticcheck is red on `main` itself; the real gate is the
"Minimal CI" GitHub workflow. Compare against a `main` baseline run before treating a
staticcheck finding as yours.

---

## Full verification suite (run before opening any PR)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/dupmatch

go build ./...
go vet ./internal/dupmatch/... ./internal/plugins/maintenance/...
go test ./internal/dupmatch/... -race -count=1
go test ./internal/plugins/maintenance/... -race -count=1
go test ./internal/merge/... -race -count=1          # CombineBooks contract unchanged
go test ./internal/server/... -race -short -count=1
go test ./... -short                                  # store-getter footgun: subset runs hide vacuous mocks
cd web && npm run test -- src/lib/
```

`go test ./... -short` in full is not optional: a getter/interface change makes old mocks
silently vacuous-pass in *other* packages, which a subset run cannot see.

---

## Dry-run acceptance gate on prod (BEFORE any apply)

Deploy from the **main checkout** — `Makefile.local`'s `LOCAL_ROOT` is hardcoded to it, so
deploying from a worktree ships main's binary:

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git pull --ff-only        # required: server-side merges must be local before deploy
make deploy
```

Get an API key with the `server-bootstrap` skill (writes `.claude/.api-token`), then:

### Gate run A — histogram only, whole library

```bash
TOKEN=$(cat /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.claude/.api-token)
curl -sS -X POST https://<server>:8484/api/v1/operations/v2 \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.detect-redundant-copies",
       "params":{"histogramOnly":true,"probeToleranceSec":10}}'
# -> {"op_id":"..."}; then poll:
curl -sS -H "Authorization: Bearer $TOKEN" \
  https://<server>:8484/api/v1/operations/<op_id>/logs
```

### Gate run B — single-reference canary on the measured case

```bash
curl -sS -X POST https://<server>:8484/api/v1/operations/v2 \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.detect-redundant-copies",
       "params":{"referenceBookId":"01KQAXKG7HYMT44GRNCGSJBXG1","probeToleranceSec":10}}'
```

### Gate run C — whole library, holds written

```bash
curl -sS -X POST https://<server>:8484/api/v1/operations/v2 \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.detect-redundant-copies","params":{"probeToleranceSec":10}}'
curl -sS -H "Authorization: Bearer $TOKEN" \
  'https://<server>:8484/api/v1/review/count'      # byKind["firstaid.redundant-copy"]
```

### Numbers that MUST be observed before anyone flips `review_apply_enabled`

| # | Requirement | Where observed |
|---|---|---|
| A1 | **≥95 %** of accepted matches at `\|delta\| <= 1 s`, with a visible trough before the noise floor | Gate run A histogram |
| A2 | If the histogram is **flat** out to 10 s → **STOP**. Duration matching is not separating signal at this data quality; do not ship a tolerance | Gate run A |
| B1 | `01KQAXKG7HYMT44GRNCGSJBXG1` selected as reference | Gate run B payload |
| B2 | `coveredTracks` length **12**, `missingTracks == [7]` | Gate run B payload |
| B3 | `debrisBookIds` length **11**, `debrisFileCount` **17** | Gate run B payload |
| B4 | `internallyRedundant` length **5** | Gate run B payload |
| B5 | `excludedFrozen` length **10** — present and counted, not silently dropped | Gate run B payload |
| C1 | `RECONCILE:` line ties: `examined == matched + unmatched + skipped-no-duration + skipped-frozen + skipped-short + skipped-ambiguous-bucket + errors`, and `examined == len(ListBookIDs())` (≈44,887) | Gate run C log |
| C2 | Library-wide hold count reported, **split** into "debris books contributing ≥2 slots" vs "exactly 1 slot (corroborator-gated)". If the single-slot share exceeds ~20 %, tighten the corroborator rule before proceeding | Gate run C summary |
| C3 | `skipped-no-duration` reported with `maintenance.duration-reextract` named as the producer. A large value is a signal to run that op and re-run, **not** a reason to widen tolerance | Gate run C summary |
| C4 | Zero book/file mutations: `GET /api/v1/audiobooks?limit=1` total count identical before and after gate run C | prod API |

Only when A1, B1–B5 and C1 all hold is `tolFingerprintSec` locked to the value the
histogram supports (proposed 2) and the design doc updated with the observed
distribution. **Applying anything is a separate, explicit human decision** made via
`AskUserQuestion`, not a text reply.

---

## Rollback

Rollback is cheap by construction, in four independent layers:

1. **Nothing is on by default.** The detector has no apply flag (LD9) and the apply
   handler is gated by `config.ReviewApplyEnabled`, which defaults OFF and is **not set
   in prod**. Merging steps 1–7 changes zero prod behaviour until someone flips it.
2. **Un-flip the switch.** If applies misbehave, set `review_apply_enabled` back to false
   and restart. Approving a hold then records the decision and executes nothing
   (`internal/server/handlers/review/handler.go:16-23`).
3. **Purge the holds.** The holds are review-queue rows keyed by a stable `DedupKey`.
   Delete the pending `firstaid.redundant-copy` items via
   `DELETE /api/v1/review/items/:id`, or simply leave them — they mutate nothing.
4. **Revert the code.** Steps are independent commits. Step 4 (the `linkVersionGroup`
   extraction) is the only one that touches shipped code; reverting it restores
   `ApplyVersionGroup`'s inlined body exactly, and its unmodified test file proves
   equivalence in both directions.

**What is NOT cheaply reversible:** an *applied* hold. `CombineBooks` hard-deletes
absorbed debris rows (`internal/merge/service.go:432-436`). The files survive on the
survivor, and re-splitting is manual. This is why the gate above is a gate and not a
checklist. Before the first apply, take a DB snapshot on prod (the ZFS dataset holding
PebbleDB) so a bad batch can be rolled back wholesale.

---

## Sequencing notes

- **This workstream assumes relink has already run.** Tasks #1/#2 are marked complete;
  if durations are still missing for the debris population, run
  `maintenance.duration-reextract` and re-run the detector rather than widening tolerance
  (LD5, gate C3). That re-run-until-clean loop *is* the First Aid convergence property.
- Do **not** run this in parallel with `maintenance.regroup-shattered-ai` on prod: both
  write review-queue rows, and interleaved holds make the reconcile numbers unreadable.
  They have different `ConcurrencyKey`s so the registry will not stop you.
- The First Aid orchestrator (separate workstream) will eventually sequence this op; do
  not build sequencing here (non-goal §7.7).
