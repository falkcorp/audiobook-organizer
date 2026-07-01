<!-- file: docs/agent-tasks/transcription-matching/TASK-04-batch-auto-match.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6182930a-5f70-4293-bc4f-1a2b3c4d5e6f -->
<!-- last-edited: 2026-06-28 -->

# TASK-04 — Batch auto-match operation

**Priority:** P1 · **Effort:** L · **Recommended subagent:** code-exploration
subagent first (map the op + apply plumbing), then go-backend subagent, then
test-writing subagent · **Depends on:** TASK-02 (apply auto-confirm)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/tm-batch-auto-match" -b agent/tm-batch-auto-match origin/main
cd "$REPO/.worktrees/tm-batch-auto-match"
git rebase origin/main
```

> Do not start until TASK-02 is merged — this builds on the audio-confirmed apply.

## Goal

Add an **operation** that walks the library and, for every unreviewed book whose
best metadata candidate both (a) scores above a threshold AND (b) exactly matches
the transcription, **auto-applies** it and marks it `matched` (audio-confirmed).
This turns the new transcription signal into bulk, hands-off metadata correction.
It must be **dry-run-gated** (report what it would do) before applying.

## Background (verify before editing)

- Operation definitions live in the plugins/registry. A good model is
  `internal/plugins/maintenance/intro_transcribe.go` — see `introTranscribeDef()`
  (an `sdk.OperationDef`) and `runIntroTranscribe`, including the `RunItems`
  fan-out and checkpointing. Copy that shape.
  ```bash
  grep -rn "sdk.OperationDef\|registry.RunItems\|ListBookIDs" internal/plugins/maintenance/intro_transcribe.go
  ```
- Apply logic: `internal/metafetch/service_apply.go` `ApplyMetadataCandidate`
  (extended by TASK-02). Fetch-and-score: `FetchMetadataForBook` /
  `bestTitleMatchForBook` in `internal/metafetch/`.
- Books: `store.ListBookIDs()` → full ordered id list (the uncapped primitive);
  `store.GetBookByID(id)`. Reuse `hintsFromBook` + exact-title compare.

## Step-by-step

1. **Explore first** (code-exploration subagent): confirm how an op is registered
   (the `Def()` + `Run` pattern), how `RunItems` drives a paged/checkpointed loop,
   and the exact signature to apply a candidate to a book.
2. Add a new op, e.g. `maintenance.auto-match-transcribed`, with params:
   ```go
   type autoMatchParams struct {
       DryRun     *bool   `json:"dry_run,omitempty"`   // default true
       MinScore   float64 `json:"min_score,omitempty"` // default e.g. 0.75
       LastBookID string  `json:"last_book_id,omitempty"` // checkpoint
   }
   ```
   Default `DryRun=true`. Default `MinScore` to a conservative value (0.75).
3. For each book with a non-garbage transcription and `MetadataReviewStatus == nil`:
   - fetch + score candidates (reuse the existing fetch/score path),
   - take the best candidate; require `score >= MinScore` AND exact normalized
     transcribed-title match (+ author substring when present),
   - if `DryRun`: log/count "would apply <candidate> to <book>",
   - else: call `ApplyMetadataCandidate` and rely on TASK-02 to mark it
     audio-confirmed.
4. Use `RunItems` for paging, checkpointing (`LastBookID`), progress, and ctx
   cancellation — mirror `runIntroTranscribe`. Set a sane `ProgressTimeout`.
5. Emit a final summary log: scanned / eligible / applied (or would-apply).
6. Register the op in the same place the maintenance ops are registered.
7. Bump file headers.

## Safety

- **Dry-run by default.** The op must NOT mutate anything unless `dry_run=false`
  is explicitly passed.
- Only touch books with `MetadataReviewStatus == nil` (never override a human
  `no_match` or an existing `matched`).
- Respect ctx cancellation and checkpoint so a long run can resume.

## How to test

`internal/plugins/maintenance/` (or wherever the op lives): a fake/in-memory
store with a few books — one with a transcription matching a high-score
candidate (applied when dry_run=false; only counted when dry_run=true), one with
no transcription (skipped), one already `matched` (skipped).

```bash
go build ./...
go test ./internal/plugins/maintenance/ ./internal/metafetch/ -count=1
go vet ./...
```

## Acceptance criteria

- [ ] New op registered and discoverable via the operations API.
- [ ] Defaults to dry-run; applies only when `dry_run=false`.
- [ ] Applies only to `MetadataReviewStatus == nil` books with an exact transcription match above `min_score`.
- [ ] Paged + checkpointed via `RunItems`; cancellable; final summary logged.
- [ ] Tests cover apply / dry-run-count / skip-no-transcription / skip-already-reviewed.
- [ ] `go test` green for changed packages; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
feat(maintenance): auto-match-transcribed batch operation (dry-run gated)

Walks the library and auto-applies the best metadata candidate to unreviewed
books whose candidate exactly matches the audio-derived transcription above a
score threshold. Dry-run by default; checkpointed + cancellable via RunItems.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/tm-batch-auto-match
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency

Re-running with the same params is safe: already-`matched` books are skipped, and
the checkpoint (`LastBookID`) resumes a partial run. Verify an op named
`auto-match-transcribed` doesn't already exist before adding it.

## Rollback

Revert the commit. No data migration. If a dry-run-false run applied bad
metadata, the existing metadata revert/changelog tooling can undo per-book.
