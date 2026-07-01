<!-- file: docs/agent-tasks/dedup-hardening/TASK-02-part-vs-whole-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: ed65b960-7db1-4692-8dd0-2d970a7abc12 -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Part-vs-whole defense-in-depth guard in the exact emitter (CONS-15)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** TASK-01 (same file, `internal/dedup/engine.go` — must be merged and rebased onto first; do NOT run this task in parallel with TASK-01)

## ⛔ START HERE (do this first, exactly)

**Do not start this task until TASK-01's PR (`agent/dh-exact-emitter-guard`) has
been merged to `origin/main`.** Both tasks edit `internal/dedup/engine.go` near
the same function; running them concurrently guarantees a merge conflict.

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dh-part-vs-whole-guard" -b agent/dh-part-vs-whole-guard origin/main
cd "$REPO/.worktrees/dh-part-vs-whole-guard"
git rebase origin/main
```

## Goal

Add a defense-in-depth guard so a single-part book (one file, short relative to
a multi-file book) cannot be paired as a 100%-confidence exact duplicate
against the whole multi-file book — even if upstream metadata is wrong and
would otherwise make the pair look identical. This protects against
over-aggressive merges when a lone chapter/part gets mis-tagged with the same
title/ISBN as the full audiobook it belongs to.

## Background (verify before editing)

- There is currently **no** part-vs-whole guard in the live exact-matching
  emitter path. A `partVsWhole` concept exists only in
  `internal/dedup/dataset/rules.go` (used for tuning-dataset labeling, not
  enforcement) — search to confirm it is not already wired into `engine.go`:
  ```bash
  grep -rn "partVsWhole\|PartVsWhole" internal/dedup/
  ```
- `chapter_sibling.go` in `internal/dedup/` guards chapter-vs-chapter
  relationships *within a single book*, not part-vs-whole comparisons *across
  two different books* — do not confuse the two; this task needs a NEW guard.
- Per-book file counts and durations are available via
  `store.GetBookFiles(bookID) ([]database.BookFile, error)` (see
  `internal/database/iface_misc.go`). `database.BookFile.Duration` is an `int`
  (seconds, non-pointer). `database.Book.Duration` is `*int` (seconds).
- Re-verify the current shape of `upsertExactCandidate` before editing — TASK-01
  will have already added guards here, so read the current body first:
  ```bash
  grep -n "func (de \*Engine) upsertExactCandidate" -A 25 internal/dedup/engine.go
  ```

## Step-by-step

1. Confirm TASK-01 has merged: `git log origin/main --oneline | grep -i "dedup-residual\|exact-emitter-guard"`.
   If not merged yet, stop and wait — do not proceed on a stale base.
2. In `internal/dedup/engine.go`, add a helper function (near
   `upsertExactCandidate` and the other guard helpers like `isNonPrimaryVersion`)
   that determines whether one book is a plausible single-part fragment of the
   other. Suggested signature:
   ```go
   // isPartVsWholeMismatch reports whether a and b look like a single-file
   // part being compared against a multi-file whole — e.g. one side has
   // exactly one BookFile whose duration is a small fraction of the other
   // side's total duration. Such pairs must not be emitted as 100%-confidence
   // exact duplicates even if titles/identifiers otherwise match, since a
   // mis-tagged chapter file is common and an incorrect merge is destructive.
   func (de *Engine) isPartVsWholeMismatch(a, b *database.Book) bool
   ```
3. Implement it using `de.store` (or whatever field the `Engine` struct already
   uses to reach the book-file store — check the `Engine` struct fields near
   the top of `engine.go`) to fetch each side's `BookFiles` via `GetBookFiles`.
   Logic: if one side has exactly 1 file and the other has 2+ files, AND the
   one-file side's total duration is less than roughly half (pick a
   conservative fraction, e.g. `< 0.6 *` the other side's total duration),
   treat it as a part-vs-whole mismatch. Treat missing/zero durations as
   "unknown" and do NOT flag as a mismatch in that case (conservative — mirrors
   the pattern in TASK-01's duration guard and `hasPlausibleAudio`).
4. Wire the new check into `upsertExactCandidate`, alongside the existing
   guards added by TASK-01, so it applies to every exact emitter uniformly.
   On a mismatch, log at `slog.Debug` (matching the existing guard log shape)
   and `return nil` without upserting.
5. Add a unit test in `internal/dedup/*_test.go`:
   - A single-file book (short duration) vs. a 10-file book with a much larger
     total duration, both carrying identical titles/ISBN → NO candidate emitted.
   - Two genuinely single-file books of comparable duration → candidate still
     emitted (proves no over-suppression of normal single-file duplicates).
6. Bump file headers on every changed file.

## How to test

```bash
go build ./...
go test ./internal/dedup/... -count=1
go vet ./internal/dedup/...
```

## Acceptance criteria

- [ ] A part-vs-whole guard exists inside (or is called directly from)
      `upsertExactCandidate`, so it applies to every exact emitter.
- [ ] A single-part book cannot pair as a 100%-confidence exact match against a
      clearly larger multi-file whole book.
- [ ] Two ordinary single-file books of comparable size still pair normally.
- [ ] Unknown/zero durations do not trigger the guard (no over-suppression).
- [ ] Unit test covers both the suppressed and the still-allowed case; `go test
      ./internal/dedup/...` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
fix(dedup): add part-vs-whole defense-in-depth guard to exact emitter (CONS-15)

No guard existed to stop a single-file part from pairing as a 100%-confidence
exact duplicate against a much larger multi-file whole book, even when upstream
metadata coincidentally matched. Add a file-count/duration heuristic in the
upsertExactCandidate chokepoint so all exact emitters are protected.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dh-part-vs-whole-guard
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/dedup/engine.go` already has a part-vs-whole style guard wired
into `upsertExactCandidate` (search `grep -n "partVsWhole\|PartVsWhole\|isPartVsWhole" internal/dedup/engine.go`),
this task is done. Rollback = revert the commit; the TASK-01 guards are
independent and remain in effect.