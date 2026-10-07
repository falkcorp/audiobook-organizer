<!-- file: docs/plans/2026-10-06-bulk-search-dialog-background.md -->
<!-- version: 1.0.1 -->
<!-- guid: 4b3781e3-29aa-4ada-bf28-1e12e9bdfdaf -->
<!-- last-edited: 2026-10-06 -->

# Bulk Search Metadata dialog: stage picks per book, apply all on close, in the background

## Goal

Owner: "don't apply until I actually close the window so they all get applied
in one operation... Even if you were doing it individually that should be
kicking off a background job and I should be able to keep going quickly through
there." The Library page's multi-book Search Metadata wizard
(`BulkMetadataSearchDialog`) must stage one pick per book without ever
disabling anything, and apply every staged pick when the window closes,
without the UI waiting on any of it.

## Root cause

- `web/src/components/audiobooks/BulkMetadataSearchDialog.tsx` `applyCandidate`
  (~line 304) sets one `applying` flag and awaits a synchronous
  `POST /audiobooks/:id/apply-metadata` (no `background`), which waits up to 60s
  for the book's scan lock, then runs the rename preflight and the DB write.
- While it runs, every Apply / Apply Selected / No Match button is disabled
  (`disabled={applying || ...}`); after it, the book's buttons stay disabled as
  "Applied". So during a library scan each book costs up to a minute of a
  greyed-out wizard. Same defect #3814 (f37310b7f) fixed in the single-book
  `MetadataSearchDialog`, left out of scope there (its plan, decision 6).

## Batch endpoint check (prefer ONE request)

- `POST /audiobooks/metadata/batch-apply-cached`: a background op, but it applies
  the top-scored CACHED candidate per book (optionally pinned to it), not the
  reviewer's pick, and cannot carry a field subset or an ASIN override.
- `POST /metadata/batch-apply-candidates`: applies the stored candidate of a
  metadata-fetch operation; fill-only; same mismatch.
- No endpoint takes `{book_id, candidate, fields, override}` for many books.
  Adding one is a backend change (a new multi-book op with its own journal and
  per-book edit-mark stamps); out of scope for an urgent UI unblock. So the
  close fires the per-book `background: true` apply (already shipped in #3814:
  202 + `metadata.apply-when-scanned` op id) with bounded concurrency.

## Design

`stagedMetadataApply.ts` (shared, no duplication):
- Split `submitStagedApply` into two reusable steps:
  `startStagedApply(book, pick, writeToFiles)` (sends the background apply,
  returns `started {opId}` | `inline` | `conflict` | `failed`) and
  `awaitStagedApply(bookId, start)` (polls the op, returns `applied {book}` |
  `failed` | `lost`). `submitStagedApply` (single dialog) is rebuilt on them
  with identical toasts.
- New `submitStagedApplies({entries, writeToFiles, toast, onDone})`: phase 1
  sends every POST with at most 4 in flight; phase 2 follows the returned ops
  with at most 4 pollers (ops run server-side regardless, so later polls return
  at once). Toasts: one "Applying metadata to N books in the background", then
  one summary: success "Metadata applied to X of N books" with "Undo all"
  (undoLastApply per applied book, bounded 4); a warning listing failures; and
  for submit-time ASIN conflicts a warning with "Apply anyway (k)" that
  re-submits those books with the override. Detached from the dialog.

`BulkMetadataSearchDialog`:
- `staged: Map<bookId, {book, pick}>`. Pick / Stage selected record the pick for
  the current book (a new pick replaces it) and advance to the next book.
  Nothing is ever disabled by a pick. The `applying` flag is removed.
- Field ticks belong to one candidate (`{candidate, fields}`), as in #3814.
- ASIN conflict flagged by the search: confirmed at pick time; the override is
  staged with the pick.
- Header chip "N staged"; book card shows "Staged" + an "Unstage" button.
- Footer: with picks staged, "Discard all & close" (sends nothing) and
  "Apply N & close". Close / Escape / backdrop also apply what is staged.
- No Match on a staged book drops its staged pick, then marks no match.
- The in-session Undo / Undo Last buttons are removed: nothing is applied while
  the dialog is open. Undo moves to the completion toast ("Undo all").
- Closing with picks calls `onComplete` (reload + clear selection) at once, and
  `onLibraryChanged` (reload only) when the background batch settles.

Caller: `LibraryDialogs.tsx` is the only caller; its handlers already do not
block (onClose just hides; onComplete reloads + clears selection;
onLibraryChanged reloads). No change needed there.

## Files

- `web/src/components/audiobooks/stagedMetadataApply.ts`
- `web/src/components/audiobooks/BulkMetadataSearchDialog.tsx`
- `web/src/components/audiobooks/BulkMetadataSearchDialog.test.tsx` (rewritten
  for staging semantics)
- `web/src/components/audiobooks/stagedMetadataApply.test.ts` (new: bounded
  concurrency, one summary toast, conflict re-submit)
- `changelog.d/20261006_bulk_search_dialog_background_apply.md`

## Steps

1. Refactor `stagedMetadataApply.ts`; keep `MetadataSearchDialog` tests green.
2. Add `submitStagedApplies`. 3. Rework the bulk dialog. 4. Tests. 5. Fragment.

## Test strategy

`npx tsc --noEmit -p web`, `npx vitest run src/components/audiobooks
src/components/review` (single dialog + review lane use the shared helper).
Bulk tests: picks never disable; picking advances; close submits each staged
pick exactly once with `background: true`; Discard submits nothing; the dialog
closes before any apply resolves; concurrency never exceeds 4.

## Rollback

Revert the commit. Frontend only; the backend `background` flag stays (#3814).

## Decisions for owner to validate

1. **N per-book background requests, not one batch request.** WHY: no existing
   endpoint applies a reviewer-chosen candidate to many books; the batch ones
   apply the cached top candidate. Each request returns 202 in milliseconds, so
   closing never waits. A true single "apply-picks" op is a backend feature;
   say if you want it (it would show as one row in Operations instead of N).
2. **Concurrency 4** for both the POSTs and the op polling. WHY: keeps the
   browser from flooding the server with 100+ requests on a large selection.
3. **Picking advances to the next book; staged books stay in the list** (marked
   Staged) so you can go back and change or unstage them. WHY: "keep going
   quickly", without losing the ability to revise before close.
4. **Close / Escape / backdrop apply everything staged.** "Discard all & close"
   is the only way out without applying. WHY: your explicit ask; Undo all on
   the completion toast covers an accidental Escape.
5. **No Match on a staged book drops the staged pick.** WHY: the newer decision
   wins, same as a new pick replacing an old one; nothing is disabled.
6. **One summary toast, not one per book.** Failures and ASIN conflicts get
   their own warning toast naming the books. WHY: 2 toasts per book across 50
   books would bury the result.
7. **Leaving the Library page with picks staged drops them** (same as #3814
   decision 7). Only closing the dialog applies.
8. **"Write to files" is read once, at close, for every staged book** (same as
   the single-book dialog). WHY: it is one switch for the session; per-pick
   capture would make the switch's current position lie about what happens.
9. **Not guarded: closing, reopening on the same books and re-staging them
   before the first batch settles** sends a second apply per book; the server
   refuses the later one (it sees the earlier one's history rows as an edit
   made after it was queued) and the failure toast names it. WHY: the window
   is seconds wide; a cross-session in-flight registry with "Applying..."
   markers is possible if you hit it.
10. **The in-dialog "Undo" and "Undo Last (N)" buttons are gone.** WHY:
    nothing is applied while the dialog is open, so there is nothing to undo
    there; Unstage / Discard all replace them. After close, undo is the summary
    toast's "Undo all" (toasts stack, so a failure toast does not hide it);
    once it is dismissed, a book's apply is undone from its page's Metadata
    History (per-field undo), as for any other apply.
