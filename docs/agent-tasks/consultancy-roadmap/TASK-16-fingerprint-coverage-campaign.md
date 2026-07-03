<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-16-fingerprint-coverage-campaign.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1cb71e9-055b-41d2-a39e-6988aadcf285 -->
<!-- last-edited: 2026-07-03 -->

# TASK-16 — Fingerprint-coverage KPI in cached library stats (consultancy NEWF-1, scope corrected)

**Priority:** P1 · **Effort:** M (scoped down from consultancy brief — see Background) · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-16-fingerprint-coverage-campaign" -b agent/cr-16-fingerprint-coverage-campaign origin/main
cd "$REPO/.worktrees/cr-16-fingerprint-coverage-campaign"
git rebase origin/main
```

## Goal

Add a library-wide **fingerprint-coverage KPI** (fingerprinted / partial /
unfingerprinted book counts + a coverage percentage) to the existing cached
`LibraryStats` (`stats:library`) so operators can see, without a manual query,
how close the library is to the coverage level the dedup positive-oracle
auto-resolution work (`docs/consultancy/02-dedup.md` DEDUP-1/steelman) needs.
Surface it in the `GET /dashboard` response alongside the other stats fields.

**This brief intentionally does NOT build a new "campaign op."** See
Background — the campaign already exists and is already scheduled. Read that
section before writing any code; it changes the scope of this task
substantially from what a naive reading of the consultancy finding would
suggest.

## Background (verify before editing)

### The consultancy citation is half right, half already-shipped — re-verify, don't take it on faith

`docs/consultancy/05-features.md` NEWF-1 ("Missing: fingerprint-coverage
campaign op + coverage KPI") claims **both** the campaign orchestration and
the KPI are missing, citing `internal/plugins/maintenance/optimize.go:66` and
`internal/plugins/acoustid/backfill.go:247` as supporting ingredients only.
Direct code inspection shows this undersells what already exists:

- **The resumable, checkpointed, rate-limited, tombstone-respecting campaign
  op already exists and already runs nightly.** It is `acoustid.backfill`
  (`internal/plugins/acoustid/backfill.go`, `func (p *Plugin) runBackfill`):
  - `ResumePolicy: sdk.ResumeRestart` with a `Schedule: "0 3 * * *"` (nightly
    cron) — already what NEWF-1 asks for.
  - Checkpointed via `BackfillParams{LastProcessedBookID}` and
    `reporter.Checkpoint(cp)` inside the `registry.RunItems(...,
    CheckpointFn: ...)` callback — resumable across restarts, exactly the
    "resumable via checkpoints" requirement.
  - Rate-limited via `fingerprintThrottle = 10 * time.Millisecond` sleep
    after each successful fingerprint (`time.Sleep(fingerprintThrottle)`).
  - Tombstone-respecting: every file goes through
    `fingerprintEligibility(f, force)` (`internal/plugins/acoustid/backfill.go`),
    which returns `fingerprintOutcomeIneligible, "permanent_failure", true`
    when `f.FingerprintFailedAt != nil && !force` — permanently-failed files
    (PR #1422's tombstones) are never re-enqueued.
  - Uses the exact memdb-safe "has fingerprint" proxy named in the task spec:
    `len(f.AcoustIDFingerprint) > 0 || f.AcoustIDSeg0 != "" ||
    f.AcoustIDFingerprintDurationSec > 0`.
  - There is a **second**, on-demand op, `acoustid.fingerprint-rescan`
    (`internal/plugins/acoustid/fingerprint_rescan.go`), with `scope:
    "missing"|"all"|"books"` — this is the one referenced at
    `optimize.go:66` inside the `library.optimize` sweep. It is not
    checkpointed (`ResumePolicy: sdk.ResumeDrop`) but shares the same
    `fingerprintEligibility` gate, so it also respects tombstones.

  **Do not build a third op.** If a future task wants the on-demand
  `fingerprint-rescan` op to also be checkpoint-resumable, that is a
  separate, narrowly-scoped follow-up — out of scope here.

- **The coverage KPI genuinely does not exist** — this part of NEWF-1 is
  correct and is the actual deliverable of this task:
  - `database.Book` has denormalized `FingerprintStatus`, `CoveragePercent`,
    `LastFingerprintedAt` fields (`internal/database/store.go:289-293`), but
    **they are never persisted** — grep confirms the only writers are
    transient API-response enrichment in
    `internal/server/audiobooks_helpers.go:84` and
    `internal/server/handlers/audiobooks/handler.go:373`, both computed
    per-request from `fingerprint.ComputeFingerprintFields(fpFiles)`
    (`internal/fingerprint/calculator.go:31`). Raw `Book` rows read directly
    from Pebble/memdb during stats computation always have these fields
    empty — do not read `b.FingerprintStatus` inside `computeLibraryStats` /
    `MemStore.ComputeLibraryStats`; it will be blank.
  - `database.LibraryStats` (`internal/database/store.go:965-986`, cached
    under `stats:library`, computed by `PebbleStore.computeLibraryStats`
    (`internal/database/pebble_store.go:3673`) and the ~150×-faster memdb
    fast path `MemStore.ComputeLibraryStats`
    (`internal/database/memdb_reads.go:648`) has zero fingerprint-related
    fields today.
  - The per-file "has fingerprint" proxy needed to compute this per book is
    `BookFile.GetAcoustIDSeg0()` (`internal/database/bookfile_fingerprint.go:24`),
    which already falls back to `AcoustIDFingerprintDurationSec > 0` when
    `AcoustIDSeg0` has been stripped from memdb rows
    (`internal/database/memdb_strip.go` documents this fallback explicitly)
    — reuse this method, do not re-derive the proxy logic.
  - A **quick-filter count for "no fingerprints" already exists** —
    `internal/database/pebble_quick_queries.go:63` defines
    `{id: "no_fingerprints", label: "No fingerprints", filter:
    {"fingerprintStatus": "none"}}` as one of the six cached quick-query
    presets. This gives a raw count of `fingerprintStatus=="none"` books but
    no percentage-of-library KPI and no "partial" breakdown — the new
    `LibraryStats` fields are a natural complement to it, not a replacement.
    No change to `pebble_quick_queries.go` is required by this task.
  - `GET /dashboard` (`internal/server/handlers/system/handler.go`, function
    `GetDashboard`) hand-builds a `gin.H{...}` response from the `LibraryStats`
    struct and does not pass the whole struct through — every new field must
    be added to this literal explicitly or it will silently not appear in the
    API response even after the struct gains the field.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (p \*PebbleStore) computeLibraryStats" internal/database/pebble_store.go
  grep -n "func (m \*MemStore) ComputeLibraryStats" internal/database/memdb_reads.go
  grep -n "type LibraryStats struct" internal/database/store.go
  grep -n "func (h \*Handler) GetDashboard" internal/server/handlers/system/handler.go
  grep -n "func (bf \*BookFile) GetAcoustIDSeg0" internal/database/bookfile_fingerprint.go
  grep -n "id:.*no_fingerprints" internal/database/pebble_quick_queries.go
  ```
  Confirm the pass-2 book_file loops still look the way this brief describes
  (Pebble does a key-only scan for perf; memdb already deserializes each
  `*BookFile`):
  ```bash
  sed -n '3760,3800p' internal/database/pebble_store.go
  sed -n '710,735p' internal/database/memdb_reads.go
  ```

## Step-by-step

1. **Add the KPI fields to `LibraryStats`** in `internal/database/store.go`
   (near the existing `BrokenFiles` field, same comment style):
   ```go
   // FingerprintedBooks/PartiallyFingerprintedBooks/UnfingerprintedBooks
   // classify every non-deleted book by whether its active book_files have
   // any/all/none of them fingerprinted (BookFile.GetAcoustIDSeg0() != "",
   // which already falls back to the memdb-safe AcoustIDFingerprintDurationSec
   // proxy — see bookfile_fingerprint.go). Computed alongside the existing
   // book_file pass so no extra scan is added.
   FingerprintedBooks           int `json:"fingerprinted_books"`
   PartiallyFingerprintedBooks  int `json:"partially_fingerprinted_books"`
   UnfingerprintedBooks         int `json:"unfingerprinted_books"`
   // FingerprintCoveragePercent = FingerprintedBooks * 100 / (TotalBooks
   // excluding non-primary duplicates counted elsewhere); 0 when TotalBooks==0.
   FingerprintCoveragePercent int `json:"fingerprint_coverage_percent"`
   ```

2. **`MemStore.ComputeLibraryStats`** (`internal/database/memdb_reads.go`,
   pass 2 / book_file loop): the loop already does
   `bf := obj.(*BookFile)` and tallies `bookActiveFiles[bf.BookID]++` for
   primary books only. Add a parallel `map[string]int` (e.g.
   `bookFingerprintedFiles`) incremented when `bf.GetAcoustIDSeg0() != ""`.
   After the existing `for id := range primaryBookIDs { ... }` block that
   finalizes `TotalFiles`, classify each primary book:
   - `bookFingerprintedFiles[id] == 0` → `UnfingerprintedBooks++`
   - `bookFingerprintedFiles[id] == bookActiveFiles[id]` (and `> 0`) →
     `FingerprintedBooks++`
   - otherwise → `PartiallyFingerprintedBooks++`

   This mirrors the status semantics of `fingerprint.ComputeFingerprintFields`
   (none/partial/complete) — do not import that function here (it takes a
   `[]FileWithFingerprint` slice, which would mean building a throwaway slice
   per book for no benefit); replicate only the three-way classification
   inline as a comment-documented equivalent.

3. **`PebbleStore.computeLibraryStats`** (`internal/database/pebble_store.go`,
   pass 2 / book_file range scan): this pass is currently a **key-only**
   scan (comment: "Optimized: key-only scan to count files without
   deserializing") for performance, because this path only runs as a rare
   fallback when memdb is unavailable (`p.mem()` returns `nil` — see the
   fast-path branch at the top of `computeLibraryStats`). Extend it to
   `json.Unmarshal` each file's value into a `BookFile` (same pattern
   pass 1 already uses for `Book`) and apply the identical classification
   logic from step 2. Document in a comment that this path is the rare
   fallback so the added deserialization cost is acceptable.

4. **Wire into `GetDashboard`**
   (`internal/server/handlers/system/handler.go`): add four keys to the
   `gin.H{...}` response literal:
   ```go
   "fingerprintedBooks":          stats.FingerprintedBooks,
   "partiallyFingerprintedBooks": stats.PartiallyFingerprintedBooks,
   "unfingerprintedBooks":        stats.UnfingerprintedBooks,
   "fingerprintCoveragePercent":  stats.FingerprintCoveragePercent,
   ```

5. **Set `FingerprintCoveragePercent`** in both `computeLibraryStats` and
   `MemStore.ComputeLibraryStats` right before returning `stats`:
   ```go
   if stats.TotalBooks > 0 {
       stats.FingerprintCoveragePercent = stats.FingerprintedBooks * 100 / stats.TotalBooks
   }
   ```

6. **Tests** — extend `TestMemStore_ComputeLibraryStats`
   (`internal/database/memdb_reads_test.go:422`): add book_files with
   `AcoustIDFingerprintDurationSec` set on some and left zero on others
   (covering all three buckets: none/partial/complete) across the existing
   `b1`/`b2`/`b3` fixture books (or add a new book), and assert
   `FingerprintedBooks`, `PartiallyFingerprintedBooks`,
   `UnfingerprintedBooks`, and `FingerprintCoveragePercent` match the
   expected counts. Also add one case where `AcoustIDSeg0` (not duration) is
   set, to prove the primary (non-fallback) path is still honored by
   `GetAcoustIDSeg0()`.

7. Bump the file header (version bump + `last-edited`) on every file
   touched, per `.standards/instructions/file-headers.md`:
   `internal/database/store.go`, `internal/database/memdb_reads.go`,
   `internal/database/pebble_store.go`,
   `internal/server/handlers/system/handler.go`,
   `internal/database/memdb_reads_test.go`.

## How to test

```bash
go build ./...
go test ./internal/database/... -run ComputeLibraryStats -count=1 -v
go test ./internal/database/... -count=1
go test ./internal/server/... -count=1
go vet ./internal/database/... ./internal/server/...
```

## Acceptance criteria

- [ ] `LibraryStats` gains `FingerprintedBooks`, `PartiallyFingerprintedBooks`,
      `UnfingerprintedBooks`, `FingerprintCoveragePercent`, all persisted in
      the `stats:library` cache like every other field on the struct (no new
      cache key, no new TTL/invalidation path).
- [ ] Both `MemStore.ComputeLibraryStats` (fast path) and
      `PebbleStore.computeLibraryStats` (fallback path) populate all four
      fields identically for the same input data — verified by a test that
      seeds the same books/files into both a `MemStore` and a `PebbleStore`
      (if a Pebble test harness exists in-package; otherwise memdb coverage
      alone is acceptable given memdb is the production fast path).
- [ ] `GetAcoustIDSeg0()` (existing method, with its existing
      `AcoustIDFingerprintDurationSec` memdb fallback) is reused for the
      per-file "has fingerprint" check — no new proxy logic invented.
- [ ] Permanently-failed files (`FingerprintFailedAt` set) are **not**
      specially excluded from the coverage denominator by this task — they
      correctly count as "unfingerprinted" (this task reports raw coverage,
      not campaign-eligible coverage; that distinction is a documented
      follow-up, not a defect).
- [ ] `GET /dashboard` response includes `fingerprintedBooks`,
      `partiallyFingerprintedBooks`, `unfingerprintedBooks`,
      `fingerprintCoveragePercent`.
- [ ] No new operation, no new plugin, no new cron schedule was added — the
      existing `acoustid.backfill` (nightly, checkpointed) and
      `acoustid.fingerprint-rescan` (on-demand) ops are untouched.
- [ ] `go build ./...`, targeted `go test`, and `go vet` all clean per "How
      to test".
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(database): add fingerprint-coverage KPI to cached library stats

NEWF-1 asked for a campaign op + coverage KPI. The campaign already exists
(acoustid.backfill: nightly, checkpointed, tombstone-respecting) — only the
KPI was missing. Add FingerprintedBooks/PartiallyFingerprintedBooks/
UnfingerprintedBooks/FingerprintCoveragePercent to LibraryStats, computed in
the existing book_file pass of both the memdb fast path and the Pebble
fallback, reusing BookFile.GetAcoustIDSeg0()'s existing memdb-safe proxy.
Surfaced in GET /dashboard.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-16-fingerprint-coverage-campaign
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

**Scope correction, not a dry-run gate:** unlike prod-data tasks in this
package, there is no owner-greenlight gate here — this is pure code/schema
addition to an existing cache struct, no prod data mutation, no backfill run.

If `LibraryStats` already has `FingerprintedBooks`/`FingerprintCoveragePercent`
(or equivalently-named fields) populated by both `computeLibraryStats` and
`MemStore.ComputeLibraryStats`, and `GetDashboard` already returns them, this
task is done — verify with:
```bash
grep -n "FingerprintedBooks\|FingerprintCoveragePercent" internal/database/store.go internal/database/memdb_reads.go internal/database/pebble_store.go internal/server/handlers/system/handler.go
```
Rollback = revert the commit; `stats:library` is recomputed on next TTL
expiry/invalidation with the old field set (extra JSON fields are additive
and backward-compatible — no migration needed either direction).

**Do not re-litigate the campaign-op question** on retry: `acoustid.backfill`
and `acoustid.fingerprint-rescan` are confirmed (by direct code read, not by
citation) to already satisfy the "resumable, rate-limited, tombstone-aware
campaign" half of NEWF-1. If a future coverage read shows the KPI stuck at a
low percentage, the correct next action is triggering/monitoring
`acoustid.backfill` runs (already scheduled nightly) or widening its
concurrency/throttle — not building new orchestration.
