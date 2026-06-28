<!-- file: docs/agent-tasks/transcription-matching/TASK-02-apply-auto-confirm.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f60718a-3d5e-4071-bc2d-9e0f1a2b3c4d -->
<!-- last-edited: 2026-06-28 -->

# TASK-02 — Apply auto-confirm on exact transcription match

**Priority:** P1 · **Effort:** M · **Recommended subagent:** go-backend subagent,
then test-writing subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/tm-apply-auto-confirm" -b agent/tm-apply-auto-confirm origin/main
cd "$REPO/.worktrees/tm-apply-auto-confirm"
git rebase origin/main
```

## Goal

When metadata is applied to a book and the chosen candidate **exactly matches the
transcription** (audio-derived title + author), record that the match is
audio-confirmed. This raises trust in the result and lets a later batch job
(TASK-04) auto-apply such matches without human review. The apply path currently
sets `MetadataReviewStatus = "matched"` for any user-applied candidate with no
notion of *why* it matched.

## Background (verify line numbers — they drift)

- `internal/metafetch/service_apply.go` — `ApplyMetadataCandidate(id, candidate, fields)`
  applies a candidate and sets `MetadataReviewStatus = "matched"` (around lines
  520–535). Find it:
  ```bash
  grep -n "func (mfs \*Service) ApplyMetadataCandidate\|MetadataReviewStatus" internal/metafetch/service_apply.go
  ```
- Reuse the matching helpers from `internal/metafetch/service_scoring.go`:
  `hintsFromBook`, `containsCI`, and `util.NormalizeTitle` (in
  `internal/util/normalize.go`) for exact normalized title comparison.

## Design decision (you choose the exact mechanism)

The book has no dedicated "match source" column. Pick ONE, simplest-first:

- **(A) Provenance/notes** — if the apply path already writes a provenance or
  `VersionNotes`/metadata-source record, append an `audio_confirmed` marker there.
- **(B) New boolean field** — add `Book.MetadataAudioConfirmed *bool` to
  `internal/database/store.go`, set it true on exact transcription match. Heavier
  (touches the model + memdb projection); only do this if (A) has no natural home.

Prefer (A). Whatever you choose, the **observable behavior** is: an exact
transcription match during apply is recorded so TASK-04 can query it.

## Step-by-step

1. In `ApplyMetadataCandidate`, after the candidate is chosen and before/where
   `MetadataReviewStatus` is set, compute:
   ```go
   th := hintsFromBook(book)
   audioConfirmed := !th.empty() &&
       th.title != "" && util.NormalizeTitle(candidate.Title) == util.NormalizeTitle(th.title) &&
       (th.author == "" || containsCI(candidate.Author, th.author))
   ```
   (Title must match exactly-normalized; author, when present, must be a
   case-insensitive substring match.)
2. When `audioConfirmed`, record the marker via your chosen mechanism (A or B).
   Keep `MetadataReviewStatus = "matched"` as today.
3. Add a slog line: `slog.Info("metadata apply: audio-confirmed match", "book_id", id, "title", candidate.Title)`.
4. Bump file headers.

## How to test

`internal/metafetch/` test (new `service_apply_transcription_test.go` or extend
existing apply tests). Assert: applying a candidate whose title equals the
transcribed title records the audio-confirmed marker; applying a non-matching
candidate does not.

```bash
go build ./...
go test ./internal/metafetch/ ./internal/database/ -count=1
go vet ./internal/metafetch/
```

## Acceptance criteria

- [ ] Exact normalized transcribed-title match (+ author substring when present) is detected at apply time.
- [ ] An audio-confirmed marker is durably recorded (mechanism A or B).
- [ ] Non-matching applies are unaffected (still `"matched"`, no marker).
- [ ] Tests cover both branches; `go test` green; `go vet` clean.
- [ ] If you added a DB field, the memdb projection + `UpdateBook` full-column write still round-trip it (see `internal/database/memdb_summaries.go`).
- [ ] File headers bumped.

## Commit message

```
feat(metafetch): record audio-confirmed metadata matches on apply

When an applied candidate exactly matches the book's audio-derived (transcribed)
title and author, mark the match as audio-confirmed so a later batch job can
auto-apply such high-trust matches without human review.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/tm-apply-auto-confirm
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency

If an audio-confirmed marker is already written on apply, this is done.

## Rollback

Revert the commit. If you added a DB field (option B), no migration is needed —
the field is optional/omitempty and defaults to nil.
