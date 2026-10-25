<!-- file: docs/executive-summaries/2026-10-25-bulk-delete-progress-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: 2d6f9b84-7a13-4c5e-b0d8-e4a1c37f5962 -->
<!-- last-edited: 2026-10-25 -->

# Bulk delete no longer takes your listening progress with it

## Executive Summary

When several books were deleted at once ("hard delete" in a bulk operation),
the app first moved each book's listening progress (how far you got, whether
you finished it, your place) over to another copy of the same book, and only
then tried to delete the book. A book that still has audio files attached is
never allowed to be deleted, because deleting it would lose track of those
files. So for those books the delete was refused, but the progress had already
been moved away. The book stayed in the library while the progress that
belonged to it had been moved off it onto the other copy listed in
Audiobookshelf, so the book itself no longer showed it.

This change makes the bulk delete look at the files **first**. A book that still
has files is skipped before anything about its progress is touched.

## What changed

- Bulk delete now checks each book for attached audio files before it moves any
  progress or changes anything else.
- A book with files is skipped, and the result for that book says why, in the
  same words the single-book delete uses: it still has files, so soft-delete it
  instead or move its files to another book first. A skipped book is counted as
  not deleted, never as a success.
- The other books in the same bulk delete are handled normally: books with no
  files are deleted, and their progress is still moved to the copy listed in
  Audiobookshelf as before.
- The check is made again at the last moment, inside the same protected step
  that moves the progress, so a file attached to a book a moment after the first
  look is also caught before any progress moves.

## Who was affected

Anyone who used the bulk hard delete on books that still had files and had
listening progress on them. The bulk delete is an API operation; the website
does not offer it today, so the exposure was to direct API use.

## What the owner needs to do

Nothing to deploy beyond the normal release. If a bulk delete was run on books
that kept their files in the past, a book whose progress looks reset had it
moved to the Audiobookshelf-listed copy in its version group. The progress is on
that copy, and is gone only if that copy was later removed.

## Not covered

- A file attached to a book by something other than a merge in the instant after
  the last check could still slip through; that window is now very small and the
  store's own refusal is the final guard. If it happens, the result for that book
  says the progress was moved and names where.
- The single-book delete has the same narrow last-moment window (it checks
  before, not inside, the protected step). It was not part of this change.
