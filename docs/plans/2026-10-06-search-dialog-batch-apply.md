<!-- file: docs/plans/2026-10-06-search-dialog-batch-apply.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0b6f3c1e-7d2a-4f58-9e41-5a8c2d7b3e90 -->
<!-- last-edited: 2026-10-06 -->

# Search Metadata dialog: stage picks, apply once on close, in the background

## Goal

Owner report: in Search Metadata, Apply works once, then greys out and nothing
else can be picked. Wanted: picks are staged, everything staged is applied in
ONE operation when the window closes, and that operation runs in the
background so the reviewer keeps moving.

## Root cause

- `web/src/components/audiobooks/MetadataSearchDialog.tsx:85,203` one
  `applying` flag, set for the whole apply request, disables every Apply button
  (`:756`), Apply Selected (`:803`) and No Match (`:816`).
- That request is synchronous: `POST /audiobooks/:id/apply-metadata`
  (`internal/server/handlers/metadata/handler.go:669-677`) first waits up to
  `requestBookLockWait` = 60s for the book's scan lock
  (`book_scan_lock.go:185-186`, normal while the library scan runs), then runs
  the rename preflight and the DB apply inline (`applyCandidateCore`). Only then
  does the dialog close (`:208`). So during a scan every apply greys the dialog
  out for up to a minute, and the only outcome is the dialog closing: there is
  no way to pick anything else.

## History: what "used to work" (checked per the owner's "gone back in time")

`git log` over `MetadataSearchDialog.tsx` and the apply handler since July:
the dialog has never staged picks or applied through a background op. It has
always awaited one apply and closed (`99cdbb925`, `f3ad8ab0d`, `727f8954f`,
`4916e4736` all keep that flow). What changed is the backend:

- Before **`8f5446b43` (2026-09-30, "single-book apply, fetch and write-back
  wait per book instead of 409")** the request never waited: during a library
  scan it answered 409 at once, otherwise it wrote the DB and handed tags,
  cover and rename to the background file-I/O pool. Apply felt instant.
- `8f5446b43` made the request take the book's scan lock and wait up to 60s
  (`requestBookLockWait`) before falling back to a queued op. During a scan,
  which is most of the time, the dialog now sits greyed out for up to a minute.
  That is the regression the owner felt.
- Nothing to restore verbatim: the 409 path was removed on purpose (it refused
  applies during scans). This fix restores the instant feel by sending the
  apply straight to the queued op that commit introduced
  (`metadata.apply-when-scanned`), skipping the in-request wait.

## Design

Backend (minimal, reuses an existing durable op):
- `apply-metadata` gains `background: true`. After the synchronous ASIN
  pre-check (409 still answered at once), it stamps the apply (edit mark and
  batch id, the same `stampQueuedCandidate` the scan handoff uses) and hands it
  to the existing `metadata.apply-when-scanned` op, answering 202
  `{queued, background, operation_id, message, book}`. With no queuer wired it
  falls back to the synchronous path. The enqueue tail of
  `lockBookForRequest` is extracted into one helper both paths use.
- Rejected alternatives: `batch-apply-candidates` (fill-only, applies the
  stored candidate of a fetch op, not the reviewer's pick) and
  `batch-apply-cached` (applies the top-scored cached candidate). Enqueueing
  `apply-when-scanned` straight from the browser via `POST /operations/v2`
  skips the edit-mark stamp, and loses the "edited since" check and restart
  idempotency.

Frontend (`MetadataSearchDialog`):
- A candidate's Pick button / Stage selected fields STAGE `{candidate, fields}`
  (candidate object, not an index). Nothing is disabled; a new pick replaces the
  staged one, visibly. Field selection is per candidate (fixes the shared-Set
  bug where ticks on card A were sent with card B).
- ASIN-conflict confirm happens at stage time (the search already flags it), so
  the override travels with the staged pick.
- Footer: staged summary + count, "Discard" (clears staging, nothing sent),
  primary "Apply N fields & close". Close/Escape/backdrop with a staged pick
  submits it (owner's explicit ask). Nothing staged: plain "Close".
- Submit = one `applyMetadataCandidate(..., background)` call, not awaited by
  the dialog: dialog closes immediately; a detached task toasts "applying in the
  background", polls the op (`pollOperationV2`), then toasts success with Undo
  (and calls `onApplied` with a fresh `getBook`) or the failure. 409 on submit
  toasts with an "Apply anyway" action that resubmits with the override.
- "No Match Found" is disabled while something is staged.

Callers:
- `MetadataPanel`: `onApplied` no longer closes the dialog (it fired late would
  close whatever book the reviewer opened next); it only refreshes the lane.
- `BookDetail` (via `BookDetailDialogs`): `onApplied={setBook}` guarded so a
  late result for another book cannot replace the page's book.

## Files

- `internal/server/handlers/metadata/handler.go`, `book_scan_lock.go` (+ test)
- `web/src/services/api.ts` (`applyMetadataCandidate` background option)
- `web/src/components/audiobooks/MetadataSearchDialog.tsx` (+ test)
- `web/src/components/audiobooks/stagedMetadataApply.ts` (new: staged pick +
  the detached background submit; outside the component file for react-refresh)
- `internal/server/handlers/metadata/handler_background_apply_test.go` (new)
- `web/src/components/review/MetadataPanel.tsx` (+
  `ReviewWorkspace.manualSearch.test.tsx`: a late completion does not close the
  next book's search; completions are coalesced into one lane reload, 1.5s)
- `web/src/pages/BookDetail.tsx`
- `changelog.d/` fragment

## Steps

1. Backend helper + `background` flag + Go tests (202 stamped, 409 still sync,
   nil-queuer falls back to sync).
2. api.ts option. 3. Dialog staging + detached submit. 4. Callers.
5. Vitest: stage several picks without greying, close submits exactly one
   request with the staged pick, Discard submits nothing, dialog closes before
   the apply resolves.

## Test strategy

`go build ./... && go vet ./internal/server/...`, `go test
./internal/server/handlers/metadata/`, `npx vitest run src/components/audiobooks
src/components/review`, `npx tsc --noEmit`.

## Rollback

Revert the commit. The backend flag is opt-in (absent = old synchronous
behaviour), so a frontend-only revert is also safe.

## Decisions for owner to validate

1. **One staged candidate per book; a new pick replaces it.** WHY: one apply
   request carries one candidate, and two queued applies of the same book would
   refuse each other (the second sees the first's history rows as an edit made
   after it was queued). Mixing fields from two candidates needs a
   multi-candidate apply op; say if you want it.
2. **Closing with a staged pick applies it** (Escape and backdrop click too),
   per your ask. "Discard" is the way out. WHY: the risk is an accidental
   Escape; the Undo toast on completion covers it.
3. **No cross-book batch queue in the Review lane.** Each book's close is its
   own background op. WHY: you asked for apply-on-close; a lane-level queue
   would delay applies until a separate "apply all" action. Throughput is the
   same: closing never waits.
4. **The background op is `metadata.apply-when-scanned`** ("Apply Metadata After
   Scan" in Operations). WHY: it is already the durable, restart-safe single-book
   apply with the later-edit check; with no scan running it runs at once.
5. **"No Match Found" is disabled while a pick is staged.** WHY: marking no
   match and applying a match in the same close contradict each other.
6. **Not changed: `BulkMetadataSearchDialog`** uses the same blocking
   apply-then-wait pattern (`BulkMetadataSearchDialog.tsx:316`). Same fix
   applies; left out to stay on the reported dialog. Say the word.
7. **Leaving the page with a pick staged drops it** (navigating away, or the
   book page unmounting). Only closing the dialog applies. WHY: "apply when I
   close the window" was the ask; submitting on unmount would also fire on
   route changes nobody meant as a decision. Say if you want unmount to submit.
8. **Lane reloads are coalesced**: background completions landing within 1.5s
   of each other trigger one Review-lane reload, not one each. WHY: every reload
   refetches the rail; one per book would yank the list while you work.
