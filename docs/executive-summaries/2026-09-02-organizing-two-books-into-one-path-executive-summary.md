<!-- file: docs/executive-summaries/2026-09-02-organizing-two-books-into-one-path-executive-summary.md -->
<!-- version: 3.0.0 -->
<!-- guid: 6d2f8b41-3a9e-4c57-b8e1-9f0c2d7a5e63 -->
<!-- last-edited: 2026-09-02 -->

# Organizing two books into one path

**Pull request:** [#3046](https://github.com/falkcorp/audiobook-organizer/pull/3046)

## Executive Summary

- When the organizer moves files into their tidy `Author/Title/` folders, two different
  books can work out to the *same* destination — the classic shape of a duplicate that
  has not been merged yet. The organizer runs many books at once, so two of them
  reaching the same spot at the same moment is not rare.
- Until now that collision could go wrong in five different ways, none of which produced
  an error. Two copies could write into one shared scratch file and leave a file that was
  a mix of both (reproduced 30 times out of 30). The book that lost the race could record
  the other book's file as its own. When the organizer wrote the new book's record, it
  re-guessed where each file *should* be and adopted whatever it found there — so a file
  that never actually copied could still be recorded as if it had, pointing at a
  stranger's audio. If that record write failed, the cleanup deleted the *whole* target
  folder, including files belonging to another book that happened to share it. And the
  "move" path used a rename that quietly replaces whatever is already at the destination.
- All five are closed. Each copy writes to a private scratch file no other copy can open.
  The final step of every move and copy now refuses to overwrite anything already at the
  destination — the operating system enforces this, not a check a moment earlier. The
  record now carries what *actually landed*: files that did not land keep their old
  location, and a book with several files that somehow landed as one file is rejected
  outright rather than recorded wrongly. Cleanup removes only the files this run itself
  created, never a folder.
- Three different parts of the app each had their own copy of the "is this a one-file
  book or a many-file book?" decision, and they had drifted apart — one of them sent
  every multi-chapter book whose record points at its first chapter down the one-file
  path. There is now one decision, used everywhere.
- A multi-chapter book is placed whole or not at all. Review of the first version of this
  change found a gap: if only *some* of a book's chapters made it into the destination
  folder (because another book had already claimed the rest), the book was still recorded
  as living in that folder — a folder it now shared. The next time either book was
  renamed, the whole folder moved with it, taking the other book's chapters along. Now,
  if any chapter cannot be placed, the ones that were are removed again and the book is
  reported as failed with the reason for each file. A chapter the library already knows
  is missing is simply left out; a chapter that has vanished without the library noticing
  fails the book until a scan records it.
- Recovering a half-finished rename now checks the parked file's size against what the
  library recorded for that chapter before adopting it. A parked file of the wrong size,
  or one with no recorded size to check against, is left where it is and reported, rather
  than published under the chapter's name.
- This closes the door; it does not look for files already damaged by it. A mixed file
  has the right name and size, so only a content check could tell, and none has been run.
- Every safety check was tested by removing it and confirming a test fails. The tests
  deliberately plant the *other* book's file at the contested location first, because an
  empty folder cannot show a replacement. Sixteen such removals were tried; the results
  are in the pull request.

## What this does not cover

Two books that legitimately share one destination are still two books; this change stops
them damaging each other's files, it does not merge them. That is the dedup lane's job.
On filesystems that do not support hard links (some network shares, exFAT), the move
falls back to the old rename with its tiny race window and says so in the log.
