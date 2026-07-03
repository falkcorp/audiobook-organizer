<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-13-stale-candidate-drain.md -->
<!-- version: 1.0.0 -->
<!-- guid: 128d31ac-5703-4821-8766-f0ed9edaa08d -->
<!-- last-edited: 2026-07-03 -->

# TASK-13 — Stale-candidate drain op (~384K importer-bug candidates, dry-run gated) (consultancy-roadmap: DEDUP-1 / DEDUP-5)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus · go-backend subagent · **Wave:** 2 · **Depends on:** TASK-04

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-13-stale-candidate-drain" -b agent/cr-13-stale-candidate-drain origin/main
cd "$REPO/.worktrees/cr-13-stale-candidate-drain"
git rebase origin/main
```

## Goal

Build a new op, `dedup.drain-stale`, that re-evaluates the ~383,902 exact-layer
dedup candidates emitted **before** the CONS-16 (duration-ms) and CONS-17
(multi-file title-leak) importer bugs were fixed against **today's** emission
gates in `upsertExactCandidate`. Most of that backlog is poisoned data from the
two now-fixed bugs, not real duplicates (per `TODO.md` CONS-9/CONS-10 and
`docs/consultancy/02-dedup.md` DEDUP-1: this is the single highest-yield,
zero-fingerprint lever for shrinking the candidate backlog). The op must be
**dry-run by default**, produce a counts+samples report, and gate any write
("apply") behind an explicit owner greenlight — do not purge or dismiss
anything as part of this task's acceptance criteria. Must also respect the
DEDUP-5 memory bound: never materialize the full ~384K-row candidate set or a
full-`database.Book` cache in one shot.

## Background (verify before editing)

- **DEDUP-1** (`docs/consultancy/02-dedup.md`): the ~383,902 stale exact
  candidates recorded in `TODO.md` were computed before two importer bugs were
  fixed:
  - CONS-16 (duration stored in milliseconds instead of seconds — fixed,
    `trackDurationSeconds()` in `internal/itunes/service/importer.go`, plus the
    `maintenance.duration-backfill` op that already ran on prod: 17,684 files /
    1,210 books corrected);
  - CONS-17 (multi-file title leak — a chapter's per-track tag name leaking in
    as the book title — fixed in `buildBookFromAlbumGroup` and the filesystem
    scanner's `AssembleBookMetadata` routing).
  Candidates created while those bugs were live were emitted against corrupt
  duration/title data and never re-checked once the data self-healed. The
  `dedup.quarantine-chapter-artifacts` op (already shipped, CONS-9) found only
  ~53 short idents this way — confirming most of the 380K is corrupt-metadata
  false positives, not chapter artifacts, and needs a **different, gate-based**
  drain (this task), not a title-collision heuristic.
- **The chokepoint to re-run candidates through** is `upsertExactCandidate` in
  `internal/dedup/engine.go`. As of this writing (verify below — TASK-01
  already landed and added two of these gates) its body runs, in order:
  `isNonPrimaryVersion` → `identifiersConflict` → `isBoilerplateTitle` (either
  side) → `hasKnownShortDuration` (either side, threshold
  `minFingerprintMatchSeconds` = 60s) → `de.isPartVsWholeMismatch`. A pending
  `layer="exact"` candidate whose two books **would not pass this chain today**
  (using their current, corrected `Duration`/`Title`/file data) is exactly the
  kind of stale-from-bug row DEDUP-1 wants drained. This task does **not**
  change `upsertExactCandidate` itself — it reuses the same predicates against
  existing rows.
- **DEDUP-5 memory bound** (`docs/consultancy/02-dedup.md`): both
  `ReevaluateAcoustIDConflicts` and `dataset_backfill.go` call
  `de.embedStore.ListCandidates` with `Limit: 1000000`, materializing the
  entire candidate set, and `ReevaluateAcoustIDConflicts` additionally builds
  an unbounded `map[string]bookMeta`-shaped cache holding full `BookSigV1`
  strings. On the same host that hit a 69GB warm-up incident, doing this again
  at ~384K candidates is a foot-gun. This new op must page `ListCandidates` in
  bounded batches (the existing `checkExactISBNScan` 500-row batching pattern
  in `internal/dedup/engine.go` is the reference; `CandidateFilter` already has
  `Limit`/`Offset` fields — verify below) and must cache only the small set of
  book fields the gates need (id, title, duration, isbn/asin, version-group,
  file count/paths) — mirror `PurgeStaleCandidates`'s `bookMeta` struct
  pattern, which already avoids caching full `Book`/`BookSigV1` blobs. Do NOT
  add a second unbounded per-book cache.
- **Owner-gated apply, M0 precedent**: `dedup.purge-legacy-fp-candidates`
  (`internal/plugins/dedup/purge_legacy_fp.go`) is the precedent for this
  pattern — dry-run by default, `apply=true` param to write, a versioned
  "done" flag in Settings to prevent an accidental double-run, and rows are
  **soft-classified** (`status` changed to a new value, e.g. `"stale-drain"`)
  rather than hard-deleted, so the run is auditable and reversible. This task
  builds the **dry-run report only** (see Acceptance criteria) — the apply
  path may be scaffolded but must not be exercised or relied upon without a
  separate, explicit user greenlight per CLAUDE.md's data-loss-gate discipline
  (same gate `TODO.md` CONS-10 already documents: "Quarantining before
  backfill = DATA LOSS... get explicit user OK before any apply").
- **Checkpoint/resumable**: `internal/operations/state.go` exposes
  `SaveCheckpoint(store database.OperationStore, opID, opType, phase string,
  index, total int) error`, `LoadCheckpoint(store, opID) (*OperationState,
  error)`, and `ClearState(store, opID) error` — the pattern used by
  `internal/maintenance/jobs/backfill_file_hashes.go` (load a checkpoint at
  start, save one every ~50 items in the main loop, clear on clean
  completion). Use the candidate offset as the checkpoint index so a
  restarted run resumes mid-backlog instead of rescanning from zero.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (de \*Engine) upsertExactCandidate\|func hasKnownShortDuration\|func isBoilerplateTitle\|func identifiersConflict\|func (de \*Engine) isPartVsWholeMismatch\|const minFingerprintMatchSeconds\|func (de \*Engine) checkExactISBNScan\|func (de \*Engine) PurgeStaleCandidates" internal/dedup/engine.go
  grep -n "type CandidateFilter\|func (s \*EmbeddingStore) ListCandidates\|func (s \*EmbeddingStore) UpdateCandidateStatus\|func (s \*EmbeddingStore) DeleteCandidate" internal/database/embedding_store.go
  grep -n "func SaveCheckpoint\|func LoadCheckpoint\|func ClearState" internal/operations/state.go
  grep -n "purgeLegacyFPDoneFlag\|purgeLegacyFPDefaultCutover" internal/plugins/dedup/purge_legacy_fp.go
  ```
  Confirm the exact ordering/names of `upsertExactCandidate`'s guard chain
  matches the Background section above — if it has drifted (more/fewer
  guards, renamed helpers), rebuild the re-evaluation predicate list to match
  what the function **actually does today**, not what this brief says.

## Step-by-step

1. Add a `DrainStaleCandidates` method on `*dedup.Engine` in
   `internal/dedup/engine.go` (or a new `internal/dedup/drain_stale.go` file if
   you prefer to keep `engine.go`'s size down — match whichever the repo's
   recent convention favors; `PurgeStaleCandidates` lives directly in
   `engine.go` today) with signature:
   ```go
   type DrainStaleResult struct {
       Inspected int
       WouldPurge int
       Kept int
       // ReasonCounts buckets WouldPurge by which gate rejected the pair:
       // "boilerplate_title", "short_duration", "part_vs_whole",
       // "identifier_conflict", "missing_book".
       ReasonCounts map[string]int
       // Samples holds up to a small cap (e.g. 50) per reason, for the
       // dry-run report — candidate IDs + book IDs + the rejecting reason.
       Samples map[string][]DrainStaleSample
   }
   type DrainStaleSample struct {
       CandidateID int64
       BookAID, BookBID string
       Reason string
   }
   func (de *Engine) DrainStaleCandidates(ctx context.Context, opID string, apply bool) (*DrainStaleResult, error)
   ```
2. Implement it to only ever touch `layer="exact"`, `status="pending"`
   candidates (mirror `PurgeStaleCandidates`'s status filter — merged/dismissed
   rows are historical and must never be touched).
3. Page through candidates in bounded batches (e.g. 500 per page) via
   `de.embedStore.ListCandidates(database.CandidateFilter{EntityType: "book",
   Status: "pending", Layer: "exact", Limit: batchSize, Offset: offset})`,
   looping until a short page ends the scan — do not pass `Limit: 1000000`.
4. For each candidate in a page, look up both books via a small local cache
   (memoize by book ID within the run, storing only: `ID`, `Title`,
   `Duration`, `ISBN10/13`, `ASIN`, `IsPrimaryVersion`, `VersionGroupID`, file
   count/paths needed by `isPartVsWholeMismatch` — NOT the full `database.Book`
   struct and never `BookSigV1`). Missing book on either side → count as
   `WouldPurge` with reason `"missing_book"` (same conservative treatment
   `PurgeStaleCandidates` gives missing books).
5. Re-run the same predicate chain `upsertExactCandidate` uses (excluding
   `isNonPrimaryVersion`, which `PurgeStaleCandidates` already separately
   drains today — do not duplicate it here unless it is missing; verify with
   the grep above) against the pair's **current** data:
   `identifiersConflict` → `isBoilerplateTitle` (either side) →
   `hasKnownShortDuration` (either side) → `de.isPartVsWholeMismatch`. First
   predicate that fires decides the reason bucket; if none fire, the candidate
   is `Kept`.
6. On `apply=false` (default): only tally `Inspected`/`WouldPurge`/`Kept`/
   `ReasonCounts`/`Samples` — write nothing to the store.
7. On `apply=true`: for each `WouldPurge` candidate, call
   `de.embedStore.UpdateCandidateStatus(c.ID, "stale-drain")` (soft
   reclassification, matching the M0 `purge_legacy_fp` precedent — never hard
   `DeleteCandidate` here, so the run is auditable/reversible). Gate this apply
   path behind a versioned Settings done-flag analogous to
   `purgeLegacyFPDoneFlag` (e.g. `dedup_stale_drain_v1_done`) so a second
   `apply=true` run after completion is a safe no-op, mirroring
   `purge_legacy_fp.go`'s pattern — verify the exact flag-read/write helper it
   uses and reuse the same one.
8. Wire checkpoint/resume: at the top, if `opID != ""`, call
   `operations.LoadCheckpoint(store, opID)` and resume from its `PhaseIndex` as
   the starting `Offset`; inside the page loop, after each page, call
   `operations.SaveCheckpoint(store, opID, "dedup:drain-stale", "scanning",
   offset, totalPendingExactCount)`; on clean completion call
   `operations.ClearState(store, opID)`. `totalPendingExactCount` can come from
   a first `ListCandidates` call with a small `Limit` to read the total count
   the store returns (or from an existing stats helper — check
   `de.embedStore` for a `CandidateStat`/count method before writing a new
   one).
9. Create `internal/plugins/dedup/drain_stale.go` following the
   `purge_legacy_fp.go` op-wrapper shape: an `sdk.OperationDef` with
   `ID: "dedup.drain-stale"`, `Capabilities: []sdk.Capability{sdk.CapLibraryRead,
   sdk.CapLibraryWrite}`, dry-run-by-default `apply bool` JSON param, a
   `Run` function that calls `p.engine.DrainStaleCandidates(ctx, opID, params.Apply)`
   and reports progress/results through `reporter` (log the full
   `ReasonCounts` breakdown and total counts via `reporter.Logger().Info(...)`
   and `reporter.UpdateProgress(...)`, matching `runPurgeStale`'s /
   `runDatasetBackfill`'s shape).
10. Register the new op in `internal/plugins/dedup/plugin.go`'s `Register`
    method's `ops := []sdk.OperationDef{...}` slice (add
    `p.drainStaleDef(), // DEDUP-1: drain CONS-16/17-era stale exact candidates`
    near `p.purgeStaleDef()` / `p.quarantineChapterArtifactsDef()`).
11. Add `internal/dedup/drain_stale_test.go` (or extend `engine_test.go`)
    covering:
    - a pending exact candidate whose current book data now fails
      `isBoilerplateTitle` → counted `WouldPurge`, reason `boilerplate_title`;
    - a pending exact candidate whose current book data now fails
      `hasKnownShortDuration` → counted `WouldPurge`, reason `short_duration`;
    - a pending exact candidate that still passes every gate → counted `Kept`,
      untouched;
    - a pending exact candidate referencing a since-deleted book ID → counted
      `WouldPurge`, reason `missing_book`;
    - `apply=false` never calls `UpdateCandidateStatus`/`DeleteCandidate`
      (assert via a fake/mock store call-count of zero, or by re-listing
      candidates after the dry run and asserting status is unchanged);
    - `apply=true` sets status to `"stale-drain"` on `WouldPurge` rows only,
      and a second `apply=true` run after the done-flag is set is a no-op
      (candidate count / status unchanged).
    - a paging test with more than one batch's worth of candidates (e.g. set
      the batch size low via a test-only constant or table-drive the loop) to
      prove the loop doesn't skip or double-count rows across page boundaries.
12. Add `internal/plugins/dedup/drain_stale_test.go` for the op wrapper: dry
    run produces a report and writes nothing; verify `OperationDef.ID`,
    capabilities, and default `apply=false`.
13. Bump the file header (version bump + `last-edited`) on every file you
    touch per `.standards/instructions/file-headers.md`.
14. Update `TODO.md`'s CONS-10 entry to note that `dedup.drain-stale` now
    exists as the dry-run tool for the drain, and that the dry-run report must
    still be shown to the user and explicitly approved before any
    `apply=true` run against prod — do not change CONS-10's checkbox state to
    checked; this task ships the tool, not the executed drain.

## How to test

```bash
go build ./...
go test ./internal/dedup/... ./internal/plugins/dedup/... -count=1
go vet ./internal/dedup/... ./internal/plugins/dedup/...
```

## Acceptance criteria

- [ ] New `dedup.drain-stale` op exists, registered in
      `internal/plugins/dedup/plugin.go`, dry-run (`apply=false`) by default.
- [ ] Dry run pages through pending `layer="exact"` candidates in bounded
      batches (never `Limit: 1000000`) and never caches a full `database.Book`
      or `BookSigV1` string per candidate.
- [ ] Dry run re-evaluates each candidate's current book data against
      `upsertExactCandidate`'s guard chain (identifier conflict, boilerplate
      title, short duration, part-vs-whole) and produces a report: total
      inspected, total would-purge, total kept, a reason-code breakdown, and
      capped samples per reason.
- [ ] Dry run (`apply=false`) writes nothing to the candidate store — verified
      by test.
- [ ] `apply=true` path is implemented and covered by tests (soft
      reclassification to `"stale-drain"`, versioned done-flag prevents a
      double-run) but this task's acceptance **stops at the dry-run report
      being correct and safe** — running `apply=true` against production data
      requires a separate, explicit owner greenlight after the dry-run
      counts/samples have been reviewed (per `TODO.md` CONS-10's existing
      data-loss gate). Do not run `apply=true` against prod as part of this
      task.
- [ ] Checkpoint/resume wired via `operations.SaveCheckpoint` /
      `LoadCheckpoint` / `ClearState` so an interrupted dry run resumes from
      its last offset instead of rescanning from zero.
- [ ] All new/updated tests green; `go build ./...`, `go vet
      ./internal/dedup/... ./internal/plugins/dedup/...` clean.
- [ ] File headers bumped on every changed file.
- [ ] `TODO.md` CONS-10 entry updated to reference the new tool without
      marking the drain itself as executed/complete.

## Commit message

```
feat(dedup): add dry-run-gated dedup.drain-stale op for CONS-16/17-era stale candidates

~383,902 exact-layer dedup candidates were emitted before the duration-ms
(CONS-16) and multi-file title-leak (CONS-17) importer bugs were fixed, and
have never been re-checked against corrected book data. Add
dedup.drain-stale: pages pending exact candidates in bounded batches (DEDUP-5
memory bound — no 1M-row loads, no full-Book caching), re-runs each pair
through upsertExactCandidate's current guard chain, and reports counts/samples
by rejection reason. Dry-run by default; apply path soft-reclassifies to
stale-drain behind a versioned done-flag (M0 purge precedent) but is not
exercised against prod by this change — that requires a separate owner
greenlight per the existing CONS-10 data-loss gate.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-13-stale-candidate-drain
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/plugins/dedup/drain_stale.go` and a `DrainStaleCandidates` method
on `*dedup.Engine` already exist and are registered in `plugin.go`'s op list,
this task is done — verify with:
```bash
grep -n "drain-stale\|DrainStaleCandidates" internal/plugins/dedup/*.go internal/dedup/engine.go
```
If the grep for `upsertExactCandidate`'s guard chain (Background section)
shows the gates have changed shape (e.g. a guard was removed, or the CONS-16/17
bugs were fixed differently than described here), stop and re-derive the
re-evaluation predicate list from the function's actual current body rather
than proceeding with a stale predicate list — do not invent gates that no
longer exist. If the ~384K candidate backlog has already been drained by some
other mechanism (check via
`grep -n "stale-drain\|dedup_stale_drain" TODO.md CHANGELOG.md` and a candidate
count check against prod), narrow this task to just landing the tool for
future regressions and note in the PR description that the historical backlog
is already clear.

Rollback: this change is purely additive (new op, new engine method, new
tests) and does not modify `upsertExactCandidate` or any other existing
emitter/gate — revert the commit to remove it. The `apply=true` path, even if
merged, defaults to off and requires an explicit param plus (per this task's
gate) a separate owner greenlight before ever running against prod, so merging
this PR does not by itself change any candidate's status.
