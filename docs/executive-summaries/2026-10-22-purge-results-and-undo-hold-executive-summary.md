<!-- file: docs/executive-summaries/2026-10-22-purge-results-and-undo-hold-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c1e9a3f-52d8-4b60-8e14-d3a09f6b2c75 -->
<!-- last-edited: 2026-10-22 -->

# Purging the trash now shows what it did, and no longer destroys undo

## Executive Summary

Three problems around the Trash and "Merge as versions" are fixed. The most
serious one could have silently made a combine impossible to undo. The other
two made merges and purges look like they did nothing.

## What was wrong

- **Purging the trash could destroy the way back from a combine.** When two or
  more books are combined into one, the leftover books go to the trash so the
  combine can be undone later. "Purge soft-deleted" did not know that, and
  would delete those leftover books for good while the undo was still
  possible. After that, the undo could no longer restore them.
- **"Purge soft-deleted" did not show a result.** It ran for over a minute with
  nothing on screen, and when it finished you could not tell how many books
  were removed or why others were kept.
- **"Merge as versions" failed when one of the books was already in the
  trash**, and the page said "Merged" anyway because it only reported that the
  request had been queued, not what the merge did.

## What changed

- A leftover book from a combine that can still be undone is now **kept**, by
  every purge path: the Purge button, the nightly purge, "Purge now" and
  "Discard progress and purge". It is counted as "kept: combine undo possible".
  These books stay in the trash until the combine is undone, because undo
  records never expire.
- Purge now shows a **preview first** ("N can be purged, M are kept and why"),
  then runs in the background with a progress bar, then shows the full
  breakdown. The trash list also shows how many books are purgeable now versus
  kept, and why.
- Merge as versions **skips books already in the trash**, says which book was
  skipped and why, reports a failed merge with its reason, and shows clearly
  which copy is kept (title, file count and path) with a radio choice.
- Deleting a book no longer **blocks its file fingerprint** by default. If you
  do ask for that and another live book still has the same file, the delete
  goes ahead, the fingerprint is not blocked, and the message names the other
  books. Blocking it would have stopped that other book from being re-imported.

## What did not change

Which books are purgeable is the same as before, with the one exception above.
Books that own files are still never purged.

## Things to know

- The purge request now answers "accepted" (202) with an operation id instead
  of the finished result. Anything calling it directly must read the result
  from the operation.
- Because undo records never expire, kept leftover books stay in the trash
  permanently until their combine is undone.
