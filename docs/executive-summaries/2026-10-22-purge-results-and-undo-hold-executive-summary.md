<!-- file: docs/executive-summaries/2026-10-22-purge-results-and-undo-hold-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 7c1e9a3f-52d8-4b60-8e14-d3a09f6b2c75 -->
<!-- last-edited: 2026-10-22 -->

# Merging, combining and purging no longer freeze the page, and purging no longer destroys undo

## Executive Summary

Four problems around the Trash, "Merge as versions" and "Merge into one book"
are fixed. The most serious could have silently made an operation impossible to
undo. The others made merges and purges freeze the page or look like they did
nothing.

## What was wrong

- **Purging the trash could destroy the way back from an operation.** Several
  operations put a leftover book in the trash so they can be undone later: a
  combine of several books into one, a folder regroup or retire, and an
  automatic duplicate merge. "Purge soft-deleted" did not know that, and would
  delete those leftover books for good while the undo was still possible. After
  that, the undo could no longer restore them, and an undo that had already
  moved some books stopped half-way.
- **Three buttons froze the page.** "Merge into one book" ran inside the web
  request, and "Merge as versions" and "Purge deleted" waited for the work to
  finish before the page came back. On the real library each took long enough
  that the page hung.
- **"Purge soft-deleted" did not show a result.** When it finished you could not
  tell how many books were removed or why others were kept, and a purge that was
  cancelled or timed out said nothing about stopping early.
- **"Merge as versions" failed when one of the books was already in the
  trash**, and the page said "Merged" anyway because it only reported that the
  request had been queued, not what the merge did.

## What changed

- A leftover book that one of three kinds of undo still needs is now **kept**,
  by every purge path (the Purge button, the nightly purge, "Purge now" and
  "Discard progress and purge"): a combine that can still be undone, an
  operation (folder regroup or retire) that has not been reverted, and the loser
  of an automatic merge that has not been reversed. It is counted as "kept:
  undo possible". Those records never expire, so these books stay in the trash
  until the undo is run.
- The check is made again for each book at the moment it would be deleted, not
  only once when the purge starts, so an operation that finished while a purge
  was running cannot slip past it. If the records cannot be read at that moment,
  the book is kept and reported with the reason. Until the server has built its
  per-book index of the operation records after a start (it does this on every
  start), the purge refuses to run at all rather than guess.
- Reversing an automatic merge now checks that the merged-away book still exists
  before it moves anything, so it can no longer stop half-way for that reason.
- **Merge as versions, Merge into one book and Purge deleted now run in the
  background.** The dialog closes at once, a notice says the work is running and
  links to the operation, and the real outcome is shown when it ends (merged N,
  skipped M already in the trash, or failed with the reason). A failed merge is
  never reported as done. Books that have an operation running cannot be
  submitted again. Requests that cannot succeed (a book that is gone, in the
  trash, or in the iTunes library) are refused up front with the reason.
- Purge shows a **preview first** ("N can be purged, M are kept and why"), then
  runs with a progress bar, then shows the full breakdown, including whether it
  stopped early and why. The trash list also shows how many books are purgeable
  now versus kept, and why.
- Merge as versions **skips books already in the trash**, says which book was
  skipped and why, and shows clearly which copy is kept (title, file count and
  path) with a radio choice.
- Deleting a book no longer **blocks its file fingerprint** by default. If you
  do ask for that and another live book still has the same file, the delete goes
  ahead, the fingerprint is not blocked, and the message names the other books:
  a blocked fingerprint makes the scanner skip that other book's own file.
  Deleting a book also no longer erases the lookup entry of another book that
  shares its file; a new repair operation checks those entries and, only when
  you start it with apply on, fixes the broken ones.
- Merging no longer refuses just because a version group contains an iTunes
  copy you did not select. The merge changes only that copy's group and
  "primary" marker in the database; its file, location and tags are never
  touched. An iTunes book you do select is still refused, and combining books
  (which moves files) still refuses iTunes inputs. The owner approved this on
  22 October. The iTunes track audit also stopped calling a track "left over"
  when its book is a version in a group that still has a live primary.

## What did not change

Which books are purgeable is the same as before, apart from the three kinds of
undo above. Books that own files are still never purged.

## Things to know

- The purge request and the combine request now answer "accepted" (202) with an
  operation id instead of the finished result. Anything calling them directly
  must read the result from the operation.
- Because the undo records never expire, kept leftover books stay in the trash
  permanently until their undo is run. Older folder-regroup leftovers will now
  show as "kept: undo possible".
- One related problem was found and not fixed here: editing a book's file
  fingerprint can still erase another book's lookup entry. It is filed in the
  TODO.
