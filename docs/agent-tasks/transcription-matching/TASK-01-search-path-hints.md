<!-- file: docs/agent-tasks/transcription-matching/TASK-01-search-path-hints.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e5f7a91-2c4d-4f60-ab1c-8d9e0f1a2b3c -->
<!-- last-edited: 2026-06-28 -->

# TASK-01 — Search-path transcription hints

**Priority:** P1 · **Effort:** S · **Recommended subagent:** go-backend subagent
(optionally a code-exploration subagent first to confirm the line numbers) ·
**Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/tm-search-path-hints" -b agent/tm-search-path-hints origin/main
cd "$REPO/.worktrees/tm-search-path-hints"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.

## Goal

The **automatic** metadata fetch already boosts candidates that match a book's
audio-derived (transcribed) title/author/narrator. The **manual search** path
(the search dialog) does NOT — it scores candidates without the transcription
hints, so a user searching a book with good transcription gets worse ranking than
the auto-fetch would. Make the manual search path pass the same hints into
scoring so both paths rank identically.

## Background (verify line numbers — they drift)

- `internal/metafetch/service_search.go` — `SearchMetadataForBook` /
  `SearchMetadataForBookWithOptions` build a candidate list and score each
  candidate inline (around lines 304–415: author ×1.5 / ×0.7, narrator ×1.3,
  series ×1.4, audiobook ×1.15/0.85). These inline multipliers do NOT include the
  transcription boost.
- `internal/metafetch/service_scoring.go` already has the reusable helpers:
  - `hintsFromBook(book *database.Book) transcriptionHints`
  - `transcriptionBoost(score float64, r metadata.BookMetadata, h transcriptionHints) float64`
  - `(transcriptionHints).empty() bool`
- The manual search path has the `*database.Book` in scope (it loads the book by
  id), so you can call `hintsFromBook(book)` there.

Use the **code-exploration subagent** or `grep -n` to confirm:
```bash
grep -n "func (mfs \*Service) SearchMetadataForBook" internal/metafetch/service_search.go
grep -n "score \*= 1.5\|score \*= 1.3\|narrator\|audiobook" internal/metafetch/service_search.go
grep -n "func hintsFromBook\|func transcriptionBoost\|func (th transcriptionHints) empty" internal/metafetch/service_scoring.go
```

## Exact files to change

- `internal/metafetch/service_search.go` — apply the transcription boost in the
  inline scoring loop.
- `internal/metafetch/transcription_match_test.go` — add a test (or a new
  `service_search_transcription_test.go`).

## Step-by-step

1. In `service_search.go`, find where the book is resolved and where each
   candidate's score is adjusted inline (the block with the `*= 1.5` author
   boost etc.). Just before/after the narrator multiplier, compute the hints once
   (outside the per-candidate loop):
   ```go
   th := hintsFromBook(book)   // book is the *database.Book already in scope
   ```
2. Inside the per-candidate loop, after the existing narrator/audiobook
   multipliers and before the candidate's score is finalized, add:
   ```go
   if !th.empty() {
       score = transcriptionBoost(score, candidate, th)
   }
   ```
   Use the same candidate variable name already in the loop (likely `r` or
   `result`); match the existing style.
3. If the function does not currently have the `*database.Book` in scope (only a
   bookID), load it once via `mfs.db.GetBookByID(id)` near the top and reuse it.
   Do NOT add a DB call inside the per-candidate loop.
4. Bump the file header `version` + `last-edited` on every file you touch.

## How to test

Add a test asserting that, given two candidates where the lower-base-score one
matches the transcribed title, the transcription-matching candidate wins via the
manual search path. Mirror the existing cases in
`internal/metafetch/transcription_match_test.go`.

```bash
go build ./...
go test ./internal/metafetch/ -count=1
go vet ./internal/metafetch/
```

## Acceptance criteria

- [ ] Manual search scoring applies `transcriptionBoost` when hints are present.
- [ ] Hints are computed once per search, not once per candidate (no per-candidate DB calls).
- [ ] New test proves a transcription-matching candidate is preferred in the search path.
- [ ] `go test ./internal/metafetch/ -count=1` passes; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
feat(metafetch): apply transcription boost in the manual search path

The auto-fetch path already boosts candidates matching a book's audio-derived
title/author/narrator; the manual search dialog did not. Compute hintsFromBook
once per search and apply transcriptionBoost in the inline candidate scoring so
both paths rank identically.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/tm-search-path-hints
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency

If `service_search.go` already calls `transcriptionBoost`/`hintsFromBook` in its
scoring loop, this task is already done — verify the test exists and stop.

## Rollback

Revert the single commit; the change is additive and isolated to the search
scoring loop.
