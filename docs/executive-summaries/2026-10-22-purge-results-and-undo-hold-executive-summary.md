<!-- file: docs/executive-summaries/2026-10-22-purge-results-and-undo-hold-executive-summary.md -->
<!-- version: 1.9.0 -->
<!-- guid: 7c1e9a3f-52d8-4b60-8e14-d3a09f6b2c75 -->
<!-- last-edited: 2026-10-24 -->

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
  submitted again, and if the page loses contact with the server meanwhile it
  says it is still checking and keeps them locked instead of calling the work
  failed. Requests that cannot succeed (a book that is gone, in the
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
  shares its file. This is now fixed at the root: the library keeps one
  "is this file already here" entry per book instead of one shared entry per
  file, so no book's change can erase another's. A repair operation (a preview
  by default, and run automatically on every start) writes any entry that is
  missing, including the ones the 21 October purge may have removed.
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
- Deleting or editing a book used to be able to erase another book's "is this
  file already in the library" lookup entry when the two shared a file
  fingerprint. The lookup now keeps a separate entry for every book, and a
  lookup that needs one answer gives the oldest live book holding the file.
  Checks that ask whether ANOTHER book has the same file (organizing a book,
  the duplicate scan) now look at every book holding it, so the oldest copy is
  no longer told "that is you" and let through.
- **Right after the update, a lookup can miss a book.** On the first start the
  server writes the missing entries and then removes the old shared ones. Until
  that first repair finishes, a lookup that needs the new entries can miss a
  book (the old shared entry names only one book per file, and the new entries
  for the others are not written yet), so a duplicate check in that window can
  say "not found" for a file that is in the library. The repair also removes any
  stray entry left behind by an earlier version, after checking that no book
  holds the file it names. If one book record is damaged and cannot be read, the
  entries are not marked complete and the old ones stay: the repair operation
  reports the damaged record and fails until it is fixed or removed.
- **Going back to an older version after that repair finds no matches at all.**
  Once the repair has removed the old shared entries, an older build cannot
  find any book by its file fingerprint (every "is this file already here"
  check passes) until you go forward again; the repair runs again on the next
  start and rewrites what that build did not.
- Restoring a book, or deleting one permanently, while a combine or merge is
  running used to wait on it for as long as the combine took. It now takes the
  merge lock for up to 3 seconds and, if a combine holds it that whole time,
  answers "a merge or combine is running, try again in a moment" without
  changing anything (the same for batch restores and deletes, which wait once
  for the whole batch). Once it has the lock it keeps it for the whole action,
  so a combine that starts a moment later waits for the restore or delete, not
  the other way round, and the request cannot hang behind it. The page keeps
  following merge, combine and purge operations while you move between pages
  or reload, and says so when it loses track of one instead of hanging.
- A combine that runs out of time or is stopped by a server shutdown now stops
  and offers Undo for what it had already moved. Files are moved for every
  absorbed book in one step, so once that step has happened the combine
  finishes retiring every book whose files moved (leaving none live with no
  files); books it had not touched yet stay exactly as they were, and the
  message says how many of each.
- If a record of a past combine is damaged and cannot be read, the purge now
  stops and deletes nothing, because that record might be an undo that still
  needs one of the trashed books. The message names the damaged record, and a
  new read-only check ("combine journal check") lists every damaged one. The
  cost is that one damaged record pauses all purging, the nightly one
  included, until someone fixes or removes it.
- The "Purge" button now deletes only what the check in front of you showed.
  The check records the moment it was taken; the purge it starts carries that
  moment and leaves alone any book put in the trash later (a merge that trashes
  its losers, someone else deleting a book), including a book that was restored
  and trashed again. It also leaves alone a book the check showed as KEPT that
  has since become purgeable (its undo ran out, say): the screen you approved
  did not say it would go. A purge started without a check is refused, and so
  is one whose check the server no longer remembers (after a restart, or once
  more than four newer checks were run): run the check again. With "also delete
  files" ticked, the check now lists the files that would be deleted and how
  many stay (outside the library folder, protected, or also named by another
  book, even one that stays in the trash), and the purge deletes exactly those:
  a file is never removed while any trashed book still names it. The check also
  counts the books still inside the retention window, so its numbers add up to
  the trash total. The nightly purge is unaffected: it has no screen to match,
  and keeps purging by its retention window.
- Every way a book can be put in the trash now records when, including the
  diagnostics "delete orphan" fix, and the database refuses to store a trashed
  book without it. A book put in the trash again after an earlier restore used
  to keep its old trash time and could be purged at once, skipping the
  retention window; it now gets a fresh one. A trashed book whose time is
  unknown (written before this) is judged by its last change instead of being
  treated as long overdue.
- The nightly purge used to report success even when it had refused to run (a
  damaged undo record, or the operation journal not ready) or had stopped
  halfway. It now fails visibly with the reason and logs the same full summary
  as the manual purge.
