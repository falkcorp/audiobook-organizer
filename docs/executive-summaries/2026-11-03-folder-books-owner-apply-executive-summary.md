<!-- file: docs/executive-summaries/2026-11-03-folder-books-owner-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92eaa05b-00eb-46a7-8f2f-ca5b673a14f6 -->
<!-- last-edited: 2026-11-03 -->

# The folder-books repair can now finish the files it used to leave behind

**Date:** 3 November 2026

## What was wrong

Some author folders had audiobook files that belonged to no real book. The
library instead held "folder-books": one book record standing in for a whole
folder. The repair that replaces them with proper books had three problems.

- It decided whether a file was missing by trusting a stored flag, not the disk.
  The flag is wrong in both directions, so the repair could build a book around
  a file that was gone, or leave a good file out.
- It refused outright for folders that spread over more than one shelf, and for
  any file whose title and author already named a live book. In both cases the
  files stayed hidden inside the folder-book and out of the library.
- A repair that was cut off partway left a hidden book behind, and the next run
  then treated that book as a reason to stop.

## What changed

- The repair now checks the disk for each file, and refuses (rather than
  guesses) when a shelf cannot be read.
- Only the owner, using the explicit owner-apply button on a single row, can
  apply a mixed-shelf row. The confirm box now quotes the reason it gives.
- When a file's title and author match exactly one live book (or one group of
  versions), the owner's apply adds the new book to that group as a
  non-primary version. The existing book stays the primary. An ordinary apply
  still skips these files, and its preview now names the book the owner's apply
  would use. If the live book was in no group, one is made for it.
- A cut-off owner apply is now finished by the next one, but only when the
  first operation's record shows it made exactly that book. A reverted apply is
  never finished this way.
- Every write is recorded in the operation before it is made. Reverting the
  operation puts the group membership and primary flag of the live book back
  exactly as they were, and refuses, changing nothing, if that book or the new
  version was changed since.

## Why it mattered

Files were silently left out of the library, or books were built around files
that no longer existed. With the owner-apply path there is now a way to
finish these folders without a manual database edit, and every step can be
undone from the Operations page.

## What to expect

Re-plan the repair after the deploy: older plan rows will show "changed since
plan", which is safe. Read the preview of the one row, apply it as the owner,
then check the operation's record. No audio file is moved, written or deleted,
and no file row is ever deleted.
