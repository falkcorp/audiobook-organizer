<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-14-duration-coverage-backfill.md -->
<!-- version: 1.0.0 -->
<!-- guid: 38ef7e96-6bb2-498f-91df-c01f795cb219 -->
<!-- last-edited: 2026-07-03 -->

# TASK-14 — Duration-coverage backfill to unblock `not_dup` catchers (DEDUP-4)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 3 · **Depends on:** TASK-13

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-14-duration-coverage-backfill" -b agent/cr-14-duration-coverage-backfill origin/main
cd "$REPO/.worktrees/cr-14-duration-coverage-backfill"
git rebase origin/main
```

## Goal

`partVsWhole` (the dataset catcher that rejects part-vs-whole pairs as
`not_dup`) deliberately refuses to fire whenever either side's duration is
unknown (`TotalDurationSec <= 0`), and `implausibleAudio` only catches the
zero-duration+tiny-file stub case — a zero-duration book with a *large*
unscanned file passes through unlabeled by design. That means every pending
dedup candidate touching a Duration=0-with-files book is stuck "unjudgeable"
forever, even though the fix is cheap: just measure the duration. This task
adds an **opt-in duration-only scoping filter** to the existing
`maintenance.duration-reextract` op so it can be run cheaply and specifically
over the residual (rather than the whole library), and documents/tests the
two-step sequence — reextract (scoped) → `dedup.dataset-backfill` — that
converts a slice of the unjudgeable backlog into rule-labeled `not_dup`.

This is a **prod-data-adjacent maintenance op change**, not a data migration —
you are not writing a new backfill job from scratch. `maintenance.duration-reextract`
already re-derives real durations for zero-duration books (see Background); the
only gap is that it always scans the *entire* library, which is wasteful when
you specifically want to target the Duration=0 residual. Do not build a
parallel ffprobe pipeline — extend the existing op with a filter flag.

## Background (verify before editing)

- `internal/dedup/dataset/rules.go` — `partVsWhole` (~line 106) explicitly
  does not fire when `ex.A.TotalDurationSec <= 0 || ex.B.TotalDurationSec <= 0`
  (comment above it says so directly: "when either side has
  `TotalDurationSec == 0` ... this catcher deliberately does not fire"). This
  citation was verified against the current file — accurate as of this task.
- `internal/plugins/dedup/dataset_backfill.go` — the package doc comment
  (top of file) states the dominant unlabeled residual class is exactly this:
  "stub/unscanned-file pairs with one side duration=0" that "is NOT caught by
  the duration-ratio or missing-file catchers when file records exist but the
  file is 0-second." This is the op you re-run in step 2 below — no changes
  needed to this file itself.
- `internal/dedup/engine.go` `hasPlausibleAudio` (grep for the exact line
  below) also treats zero-duration + large-file books as "plausible audio"
  and lets them through rather than suppressing — consistent with `rules.go`'s
  `implausibleAudio`, which only suppresses zero-duration + **small** file
  (< `minPlausibleAudioBytes`, 256 KiB). Large-file zero-duration books are the
  gap this task closes (by giving them a real duration, not by changing the
  suppression logic).
- **The actual fix lever is `internal/plugins/maintenance/duration_reextract.go`**
  (op `maintenance.duration-reextract`). Read its package doc comment in full
  before editing — it already:
  - Iterates every book via `sdk.PageBooks` (paged, 500/page).
  - For books with `BookFile` segments, re-derives each segment's real
    duration: fingerprint-stored value first (`AcoustIDFingerprintDurationSec`,
    fast/no-shell-out), then a stored non-iTunes `Duration` fast path, then
    ffprobe as the last resort (`extractWithTimeout`).
  - For virtual single-file books (no `BookFile` rows), probes `Book.FilePath`
    directly with ffprobe.
  - Writes corrected segments and calls `RecomputeBookAggregates` (via the
    write path used elsewhere in the op) so `Book.Duration` reflects the sum.
  - Is dry-run by default (`DryRun` param), idempotent, cancellable, and
    already supports a `Workers` param (default 4, max 16) controlling ffprobe
    concurrency — this is the "existing ffprobe infra" and "4-worker pattern"
    referenced by this task; there is nothing new to build there.
  - This op **already handles zero-duration books** — `processBookForReextract`
    computes `oldDur` from `book.Duration` (0 if `nil`) and always attempts to
    derive `newDur`, regardless of what the stored value was. The only
    inefficiency is that it does this over the *entire* library on every run,
    including books whose duration is already known-good, which wastes the
    fingerprint-fast-path lookups (cheap) but especially the ffprobe fallback
    tail (slow) on files that don't need re-checking.
  - There is an existing precedent for an opt-in boolean scoping param in this
    same package: `internal/plugins/maintenance/intro_transcribe.go`'s
    `OnlyMissing *bool` (defaults to skip-if-already-present semantics). Match
    that naming/nil-handling convention for the new param.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func partVsWhole\|TotalDurationSec == 0\|TotalDurationSec <= 0" internal/dedup/dataset/rules.go
  grep -n "func sideImplausibleAudio\|minPlausibleAudioBytes" internal/dedup/dataset/rules.go
  grep -n "func hasPlausibleAudio" internal/dedup/engine.go
  grep -n "ID:.*maintenance.duration-reextract\|type durationReextractParams\|func processBookForReextract\|func (p \*Plugin) runDurationReextract\|sdk.PageBooks" internal/plugins/maintenance/duration_reextract.go
  grep -n "OnlyMissing \*bool" internal/plugins/maintenance/intro_transcribe.go
  grep -n "ID:.*dedup.dataset-backfill\|type datasetBackfillParams" internal/plugins/dedup/dataset_backfill.go
  ```
  Confirm `durationReextractParams` and the producer callback still look like
  this (fields may have grown — do not delete existing fields):
  ```bash
  sed -n '/type durationReextractParams struct/,/^}/p' internal/plugins/maintenance/duration_reextract.go
  ```

## Step-by-step

1. Open `internal/plugins/maintenance/duration_reextract.go` and re-verify the
   anchors above.
2. Add a new field to `durationReextractParams`:
   ```go
   // OnlyMissingDuration, if true, skips books whose Duration is already known
   // and positive (Book.Duration != nil && *Book.Duration > 0). Use this to
   // scope a run to the Duration=0/nil residual (DEDUP-4) instead of
   // re-checking the whole library. Default false (preserves existing
   // whole-library behavior for all current callers/schedules).
   OnlyMissingDuration bool `json:"onlyMissingDuration"`
   ```
3. In the producer closure inside `runDurationReextract` (the
   `sdk.PageBooks(ctx, store, reporter, pageSize, func(book database.Book) error { ... })`
   callback), add the filter immediately after the `params.Limit` check and
   before `dispatched++`/the `jobCh <- book` send:
   ```go
   if params.OnlyMissingDuration && book.Duration != nil && *book.Duration > 0 {
       return nil // skip: duration already known, out of scope for this run
   }
   ```
   Do not touch `params.Limit` semantics — a skipped book must not count
   against `dispatched`/`Limit`, since `Limit` bounds *examined* eligible
   books, not raw library size. Place the new check so skipped books never
   increment `dispatched`.
4. Update the op's `Description` string in `durationReextractDef()` to mention
   the new param (one clause, e.g. "Set onlyMissingDuration=true to scope the
   run to books with no known duration.").
5. Add a test in `internal/plugins/maintenance/duration_reextract_test.go`
   (model it on `TestDurationReextract_MixedFingerprintAndFfprobe` /
   `TestDurationReextract_EmptyLibrary` for store-fixture setup) that:
   - Seeds two books: one with `Duration` already set to a positive value and
     a `BookFile`, one with `Duration == nil` (or `0`) and a `BookFile`.
   - Runs `runDatasetBackfill`... no — runs `runDurationReextract` with
     `OnlyMissingDuration: true, DryRun: true`.
   - Asserts the already-durationed book is NOT examined/eligible (does not
     appear in the eligible count or trigger an ffprobe/fingerprint lookup —
     use the existing counters/log fields the other tests already assert on),
     and the zero-duration book IS eligible.
   - Add a second small test (or extend the same one) confirming
     `OnlyMissingDuration: false` (default) still examines both books —
     proving the new param is additive and does not change default behavior.
6. Do **not** modify `internal/dedup/dataset/rules.go` or
   `internal/plugins/dedup/dataset_backfill.go` — both already behave exactly
   as needed (see Background); this task's code change is scoped entirely to
   the reextract op's filter.
7. Bump the file header (version + `last-edited`) on every file you touch,
   per `.standards/instructions/file-headers.md`.
8. Do **not** run the ops against the production database as part of this
   task. This brief ships the code change and its unit-test proof only. The
   actual prod sequencing (run `maintenance.duration-reextract` with
   `onlyMissingDuration=true, dryRun=true` first, review the eligible/would-change
   counts, then `apply=false` → `apply=true` on `dedup.dataset-backfill`) is an
   **owner-greenlit operational step**, not something this PR executes.

## How to test

```bash
go build ./...
go test ./internal/plugins/maintenance/... -run TestDurationReextract -count=1 -v
go test ./internal/plugins/maintenance/... -count=1
go vet ./internal/plugins/maintenance/...
```

## Acceptance criteria

- [ ] `durationReextractParams` has a new `OnlyMissingDuration bool` field
      (json tag `onlyMissingDuration`), default `false`.
- [ ] When `OnlyMissingDuration: true`, books with a known positive
      `Book.Duration` are skipped by the producer before dispatch (not merely
      filtered post-hoc) and do not consume `Limit`.
- [ ] When `OnlyMissingDuration: false` (or omitted), behavior is byte-for-byte
      identical to before this change — full-library scan, unaffected.
- [ ] New/updated tests prove both the scoped-skip and the default-unchanged
      behavior; `go test ./internal/plugins/maintenance/...` is green.
- [ ] `go vet ./internal/plugins/maintenance/...` is clean.
- [ ] `internal/dedup/dataset/rules.go` and
      `internal/plugins/dedup/dataset_backfill.go` are untouched by this PR
      (verified already correct for the stated purpose — see Idempotency).
- [ ] File headers bumped on every changed file.
- [ ] **STOP HERE for this PR.** Running `maintenance.duration-reextract`
      with `onlyMissingDuration=true` against the production database, or
      following it with `dedup.dataset-backfill --apply=true`, requires an
      explicit owner greenlight after reviewing a dry-run report first. Do
      not execute either op against prod as part of this task or PR.

## Commit message

```
feat(maintenance): scope duration-reextract to unknown-duration books (dedup-residual)

partVsWhole and implausibleAudio in the dedup dataset catchers both leave
Duration=0-with-large-files pairs unjudgeable by design, since duration is
the only signal that could resolve them. maintenance.duration-reextract
already re-derives real durations (fingerprint-first, ffprobe fallback) but
always scans the whole library. Add an opt-in onlyMissingDuration filter so
the Duration=0 residual can be probed cheaply on its own, ahead of a
dataset-backfill re-run that will convert a slice of the unjudgeable backlog
into rule-labeled not_dup.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-14-duration-coverage-backfill
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

- If `durationReextractParams` already has an `OnlyMissingDuration` (or
  equivalently-named) field wired into the `sdk.PageBooks` producer callback,
  this task is done — verify with
  `grep -n "OnlyMissingDuration" internal/plugins/maintenance/duration_reextract.go`
  and confirm the producer callback references it.
- If `partVsWhole` or `implausibleAudio` have since been changed to fire on
  zero-duration pairs directly (making this filter moot), stop and re-check
  DEDUP-4 against the current `rules.go` before proceeding — the premise of
  this task (duration is the missing signal) may no longer hold, and the
  right fix might live in `rules.go` instead. As of this brief's verification
  pass, `rules.go` still explicitly skips zero-duration pairs by design (see
  Background), so no rules.go change was made.
- Rollback = revert the commit. The new field is additive and defaults to
  `false`, so no scheduled/existing invocation of `maintenance.duration-reextract`
  changes behavior; reverting is a pure no-op for anyone not passing the new
  param.
