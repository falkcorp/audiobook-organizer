<!-- file: docs/agent-tasks/transcription-matching/TASK-05-dedup-tiebreaker.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7293a40b-6081-43a4-bc50-2b3c4d5e6f70 -->
<!-- last-edited: 2026-06-28 -->

# TASK-05 — Dedup tiebreaker via transcription

**Priority:** P3 · **Effort:** M · **Recommended subagent:** code-exploration
subagent first, then go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/tm-dedup-tiebreaker" -b agent/tm-dedup-tiebreaker origin/main
cd "$REPO/.worktrees/tm-dedup-tiebreaker"
git rebase origin/main
```

## Goal

Dedup groups books by metadata similarity at a fuzzy threshold (~0.85). For
**borderline** pairs (just under/over the line), the audio-derived transcribed
title/author is a strong independent signal: if two candidate-duplicate books
have matching transcribed titles, raise confidence they ARE duplicates; if their
transcribed titles clearly differ, lower it (they're distinct books that merely
share fuzzy metadata). Use transcription as a tiebreaker, not as a primary
grouping key.

## Background (verify line numbers — they drift)

- `internal/dedup/book_dedup.go` — `ScanBookDuplicates` runs a three-tier scan;
  the metadata fuzzy tier calls something like
  `store.GetDuplicateBooksByMetadata(0.85)` (~lines 78–84). Find it:
  ```bash
  grep -n "func.*ScanBookDuplicates\|GetDuplicateBooksByMetadata\|0.85\|threshold" internal/dedup/book_dedup.go
  ```
- The transcribed fields are on `Book` (`TranscribedTitle`, `TranscribedAuthor`).
  Reuse a normalized-title compare (`util.NormalizeTitle`) and `containsCI`.
- Tests: `internal/dedup/book_dedup_test.go`.

## Step-by-step

1. **Explore first**: confirm where borderline metadata pairs are produced and
   what data each pair carries (do both books' transcribed fields reach this
   point, or must you load them via `GetBookByID`?). Decide the cheapest place to
   apply the tiebreaker.
2. Define a small helper, e.g.:
   ```go
   // transcriptionAgreement returns +1 if both books' transcribed titles match,
   // -1 if both are present and clearly differ, 0 if unknown (missing data).
   func transcriptionAgreement(a, b *database.Book) int { ... }
   ```
   Use exact normalized title match for +1; present-but-different for -1; any
   missing/garbage transcribed title → 0 (no signal).
3. Apply it to borderline pairs only (e.g. similarity in [0.80, 0.88]): promote a
   +1 pair to a higher-confidence band / keep it; demote a -1 pair (drop or flag).
   Do NOT change behavior for pairs far from the threshold.
4. Keep it conservative — when in doubt (0), leave the existing decision intact.
5. Bump file headers.

## How to test

`internal/dedup/book_dedup_test.go`: a borderline pair with matching transcribed
titles is kept/promoted; a borderline pair with clearly different transcribed
titles is demoted/dropped; a pair with missing transcription is unchanged.

```bash
go build ./...
go test ./internal/dedup/ -count=1
go vet ./internal/dedup/
```

## Acceptance criteria

- [ ] Transcription agreement only affects borderline pairs (near the threshold).
- [ ] Matching transcribed titles raise confidence; clearly-different lower it.
- [ ] Missing/garbage transcription = no change (signal = 0).
- [ ] Tests cover promote / demote / no-signal; `go test ./internal/dedup/` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
feat(dedup): transcription tiebreaker for borderline metadata pairs

For dedup candidate pairs near the fuzzy threshold, use the audio-derived
transcribed title as an independent signal: matching titles raise duplicate
confidence, clearly-different titles lower it. Conservative — no effect when
transcription is missing or pairs are far from the threshold.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/tm-dedup-tiebreaker
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency

If `book_dedup.go` already consults transcribed fields for borderline pairs, this
is done.

## Rollback

Revert the commit; dedup grouping returns to metadata-only.
