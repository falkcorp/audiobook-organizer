<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-02-metadata-fuzzy-getter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1efae52d-2030-448f-aab6-d2f9a8eaedb1 -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Implement GetDuplicateBooksByMetadataCore on both backends; revive dedup tier 3 (INIT-2 T2) [⚠ review-critical]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate.
**File-ownership:** none for this task's files (INIT-2 owns `internal/dedup/engine.go` and `internal/database/embedding_store.go`, but this task touches neither). WAVE ORDER: this task shares `pebble_store.go`/`memdb_reads.go`/`mock_store.go` with TASK-01 — do not start until TASK-01's PR is merged and this worktree is rebased on it.

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · store-getter + fuzzy-grouping subagent · **Why:** grouping precision and O(N²) avoidance need judgment; coordinator line-reviews the loop shape · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-pipeline-hardening-metadata-fuzzy-getter" -b agent/dedup-pipeline-hardening-metadata-fuzzy-getter origin/main
cd "$REPO/.worktrees/dedup-pipeline-hardening-metadata-fuzzy-getter"
git rebase origin/main
# Precondition: TASK-01 must already be MERGED (this task depends on it and
# copies its implementation shape). Verify (>=1 hit):
grep -nE 'func \(m \*MemStore\) GetFolderDuplicatesCore' internal/database/memdb_reads.go
# 0 hits here means TASK-01's PR has not merged yet — that is a SEQUENCING
# stop, not a broken brief: STOP, report "blocked on TASK-01", and wait.
# (On pre-TASK-01 main this grep is EXPECTED to return 0 hits — the MemStore
# folder getter does not exist until TASK-01 creates it.)
```

## Goal

Replace the no-op stub `GetDuplicateBooksByMetadataCore(threshold float64)` with a real
implementation on BOTH backends (PebbleStore + MemStore twin) plus MockStore default, so dedup
tier 3 (metadata-fuzzy) in `ScanBookDuplicates` receives candidate GROUPS it can refine with
its existing `metadataPairSimilarity`/`applyTranscriptionMetadataTiebreaker` logic. The store
returns coarse fuzzy groups at ≥ threshold; fine-grained pair scoring stays downstream —
REUSE, do not duplicate, the downstream similarity logic. Guard against O(N²): bucket first,
compare only within buckets (the PR #1451 index-then-narrow pattern; the 2026-07-07 #19
incident — a full-library scan per book, ~16h — is the failure mode this forbids).

## Background (verify before editing)

- The stub lives directly below the `GetFolderDuplicatesCore` getter in
  `internal/database/pebble_store.go` and still says `return nil, nil`. NOTE ON
  TIMING: on today's main BOTH getters are `return nil, nil` stubs (the doc
  comment at ~pebble_store.go:1040 says "known-unimplemented stub on both
  storage backends"). TASK-01 — a hard dependency of this task — implements the
  folder getter first; by the time this task legitimately starts (precondition
  grep above hits), the folder getter is the implemented reference you mirror.
- Caller passes `metadataBorderlineFloor = 0.80` from `internal/dedup/book_dedup.go`; groups
  below threshold must NOT be returned. Downstream refinement
  (`metadataPairSimilarity`, tiebreaker) exists in `book_dedup.go` and is currently starved.
- Bucketing design (locked, spec Decision 3): normalize author + take the first significant
  title token as the bucket key; run pairwise title/author similarity ONLY within a bucket;
  union-find (or simple transitive merge) similar pairs into groups. Add unexported consts
  `metadataFuzzyBucketCap = 200` — a bucket larger than the cap is SKIPPED with a
  `slog.Warn` (log the bucket key + size), never processed pairwise. Skipping is
  non-disqualifying: the run continues and returns all other groups.
- Similarity metric — REUSE IS THE DEFAULT (master plan T2 / spec §C2: "reuse the existing
  fuzzy similarity path", never a second metric). `internal/matcher/fuzzy.go` imports ONLY
  stdlib (`strings`, `unicode` — verify:
  `grep -n 'import' -A4 internal/matcher/fuzzy.go`), so `internal/database` →
  `internal/matcher` is expected to import cleanly (the forbidden direction is database →
  dedup, which matcher does not touch). Confirm that with a build BEFORE considering any
  alternative. ONLY if the import provably cycles (show the `go build` error in the PR) may
  you fall back to a minimal local normalized-token-overlap helper: name it
  `metadataFuzzyGroupScore`, keep it unexported, and doc-comment it as INTENTIONALLY COARSE,
  grouping-only — never a second scoring authority; fine-grained pair scoring stays in
  `book_dedup.go`'s `metadataPairSimilarity`. Expect this fallback branch to be dead.
- Nil/unknown semantics (spell-out): empty title ⇒ book skipped (never bucketed); empty
  author ⇒ book is still bucketed by title token alone (missing data is non-disqualifying,
  it just widens the bucket); nil `threshold` cannot happen (value param), but
  `threshold <= 0` must behave as "no floor" and still terminate via the bucket cap.
- MockStore already has the `GetDuplicateBooksByMetadataFunc` hook — keep it; only ensure the
  non-hook default still returns `nil, nil` (tests inject via the hook).
- Fail-open: consumer logs `"metadata dedup failed"` and continues — return `nil, err`.

- **Re-verify these anchors before editing** — line numbers drift. All commands use
  `grep -E` (POSIX extended regex) so alternation (`|`) and escaped parens work
  identically under GNU grep, BSD/macOS grep, and ripgrep shims — do not rewrite
  them into BRE `\|` form.

  These resolve on today's main (0 hits on any of them = STOP and report — the
  repo does not match this brief):
  ```bash
  # Edit target: the metadata stub (~pebble_store.go:1054, 1 hit)
  grep -nE 'func \(p \*PebbleStore\) GetDuplicateBooksByMetadataCore' internal/database/pebble_store.go
  # Sibling folder getter on PebbleStore (>=1 hit today as a stub; after TASK-01
  # merges this is the implemented delegation + paged scan you copy from)
  grep -nE 'func \(p \*PebbleStore\) GetFolderDuplicatesCore' internal/database/pebble_store.go
  # Caller + threshold + downstream logic (context only — do not edit book_dedup.go)
  grep -nE 'metadataBorderlineFloor|GetDuplicateBooksByMetadataCore' internal/dedup/book_dedup.go
  grep -nE 'func metadataPairSimilarity|func applyTranscriptionMetadataTiebreaker' internal/dedup/book_dedup.go
  # MockStore hook (>=1 hit)
  grep -n 'GetDuplicateBooksByMetadataFunc' internal/database/mock_store.go
  # Existing fuzzy helpers to consider reusing (>=1 hit expected)
  grep -n 'func ' internal/matcher/fuzzy.go
  ```

  This one exists ONLY after TASK-01 merges (it is the same command as the
  START-HERE precondition; 0 hits = blocked on TASK-01, wait — do not report
  the brief as broken):
  ```bash
  # Copy-from source: TASK-01's MemStore twin shape (>=1 hit once TASK-01 merged)
  grep -nE 'func \(m \*MemStore\) GetFolderDuplicatesCore' internal/database/memdb_reads.go
  ```

## Step-by-step

1. Run the anchor greps. Open `internal/database/pebble_store.go`, locate the metadata stub.
2. Pebble path: memdb delegation first (`p.UseMemDB && p.mem() != nil` →
   `p.mem().GetDuplicateBooksByMetadataCore(threshold)`), then the paged
   `GetAllBooksCore` scan fallback — mirror TASK-01's implemented folder getter for both the
   delegation and the pager.
3. Build buckets in one pass: key = normalized author + first significant title token; skip
   empty-title books; enforce `metadataFuzzyBucketCap` with a `slog.Warn` skip.
4. Within each bucket, score pairs with the reused `internal/matcher` helper (or, only with a
   proven import cycle per the Background bullet, the local fallback); group pairs scoring
   ≥ `threshold` transitively; emit groups of ≥2 as `[]BookCore`.
5. MemStore twin in `internal/database/memdb_reads.go` with identical bucketing/grouping
   (extract the shared bucket+group logic into ONE unexported helper both backends call — do
   not paste it twice).
6. Purely additive elsewhere: no signature changes, no consumer edits, no changes to
   TASK-01's folder getter, no import reordering beyond gofmt. Remove the stale
   "not efficiently supported" stub comment for THIS getter.
7. NEW `internal/database/pebble_store_metadata_dups_test.go`: (a) two near-identical
   title/author books → grouped at threshold 0.80; (b) clearly-different books in the same
   bucket → NOT grouped; (c) sub-threshold pair → not grouped; (d) oversized bucket (cap+1
   same-key books) → skipped with run completing and other groups still returned
   (anti-over-suppression + termination); (e) empty-title book skipped; (f) Pebble scan vs
   MemStore twin parity on the same fixture.
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
9. Gates below — FULL `go test ./... -short`, never a subset (store-getter discipline).

## How to test

```bash
make ci
go test ./... -short
```

Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck
to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -nE 'func \(m \*MemStore\) GetDuplicateBooksByMetadataCore' internal/database/memdb_reads.go` hits (twin exists)
- [ ] `grep -n "metadataFuzzyBucketCap" internal/database/*.go` hits (O(N²) cap present)
- [ ] No all-pairs loop over the full library: the pairwise loop is provably inside a bucket iteration (reviewer line-checks; test (d) proves termination on oversized buckets)
- [ ] Anti-over-suppression: test (d) — oversized bucket skipped, other groups still returned; test (a) — a real near-dup pair IS grouped
- [ ] Tests green: `make ci` exits 0 AND full `go test ./... -short` exits 0; vet/lint clean on changed files.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: " <file>` shows 2026-07-10 or later).

## Commit message

```
feat(database): implement GetDuplicateBooksByMetadataCore on Pebble + MemStore (INIT-2 T2)

Revives dedup tier 3 (metadata fuzzy). Bucketed grouping (author +
title-token, capped buckets) avoids the O(N^2) all-pairs shape that froze
full-scan twice (PR #1451 / #1857 precedents); fine-grained pair scoring
stays in book_dedup.go's existing metadataPairSimilarity path.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-pipeline-hardening-metadata-fuzzy-getter
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -nE 'func \(m \*MemStore\) GetDuplicateBooksByMetadataCore' internal/database/memdb_reads.go` hits AND `grep -n "metadataFuzzyBucketCap" internal/database/pebble_store.go internal/database/memdb_reads.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; tier 3 returns to empty groups (today's behavior); no data or schema is touched.
