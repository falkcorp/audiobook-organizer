<!-- file: docs/executive-summaries/2026-10-25-bulk-delete-progress-executive-summary.md -->
<!-- version: 1.4.0 -->
<!-- guid: 2d6f9b84-7a13-4c5e-b0d8-e4a1c37f5962 -->
<!-- last-edited: 2026-10-25 -->

# Deleting books no longer takes your listening progress with it

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
has files is skipped before anything about its progress is touched. The delete
of one book at a time and the purge of trashed books had the same weak spot in a
much smaller form (see below) and get the same treatment. All three now share
one check, so they cannot disagree about what counts as "still has files".

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
- The shared check also counts files kept for older versions of a book ("retired"
  files). Those cannot be moved to another book, so a book that has only those is
  refused too, and the message says they cannot be moved instead of suggesting
  to move them. Before, such a book passed the first look, had its progress
  moved, and was then refused.
- The purge of trashed books (the nightly clean-up and "discard progress and
  purge") does the same last-moment check before it moves any progress, and its
  very first look at the files now uses the shared check too. A trashed book that
  has only older-version files is refused before anything about it is touched, and
  the nightly report counts each book once.
- If a file does slip in after that last check, the message now says plainly that
  the progress was moved to another copy and the book was kept, instead of saying
  nothing was deleted.
- Bulk hard delete now needs the "delete" permission, the same as deleting one
  book. Before, anyone allowed to edit metadata could permanently delete books
  through the bulk route.
- Moving books to the trash in bulk now needs the "delete" permission, and taking
  them out of the trash in bulk needs the "organize" permission, the same as doing
  it to one book. Before, anyone allowed to edit metadata could trash or restore
  books in bulk, on both bulk routes. Ordinary bulk edits of titles, authors and
  the like still need only the edit permission. The library page now greys out
  "Delete Selected" and "Restore Selected" for people who lack the permission and
  says which one is missing, and shows the server's message if a bulk action is
  refused.
- Closing a loophole in the same rule: a bulk edit that only changes a book's
  "library state" to a normal value (for example "organized") on a book that is in
  the trash quietly takes it out of the trash. Each book is now checked after it
  is loaded: taking a trashed book out of the trash needs the "organize"
  permission and putting a book in the trash needs "delete", whatever the request
  looks like. A book the person may not change is reported as not changed, naming
  the missing permission, and the other books in the request are handled
  normally.
- "Discard progress and purge" erases everyone's progress on a trashed book and
  then deletes it. If the delete is refused at the very last moment (a file
  slipped in), the progress is already gone, and the app used to say nothing
  changed and keep no record. It now keeps a record of what was erased, says in
  the message that the progress was erased and the book stayed, and the page
  refreshes so the book stops offering the discard.
- A bulk edit sent without any permission information is now refused when it
  would trash or restore a book, instead of being let through unchecked. Nothing
  in the app sends such a request today; this closes a door a future feature
  could otherwise walk through by accident.
- On the library page, the greyed-out bulk buttons now show their reason when you
  hover over them (before, the reason never appeared). On a brand-new install
  with no accounts yet, the buttons are enabled, as the server allows.
- The purge and discard error messages no longer tell you to soft-delete a book
  that is already in the trash.
- Deleting a single book now does the same last-moment check. Before, it
  looked at the files once, then took the protected step without looking again,
  so a file attached in that gap could still end with the progress moved and the
  book kept. Now the delete stops before moving anything and nothing is changed,
  including the "block this file from being re-imported" mark.

## Who was affected

Anyone who used the bulk hard delete on books that still had files and had
listening progress on them. The bulk delete is an API operation; the website
does not offer it today, so the exposure was to direct API use. The missing
permission checks on bulk trash and restore could be used by any account that
could edit metadata, and it was reachable only by calling the API directly: the
website's bulk edit sends only metadata fields, and its bulk delete and restore
buttons use the one-book routes, which were already protected.

## What the owner needs to do

Nothing to deploy beyond the normal release. If a bulk delete was run on books
that kept their files in the past, a book whose progress looks reset had it
moved to the Audiobookshelf-listed copy in its version group. The progress is on
that copy, and is gone only if that copy was later removed.

## Not covered

- A file attached to a book by something other than a merge in the instant after
  the last check (the folder scanner or an import, which do not use the protected
  step) could still slip through. That window is very small and the store's own refusal
  is the final guard. If it happens, the message for that book says the progress
  was moved and names where.
- Other clean-up jobs (the duplicate clean-up and the iTunes regroup) check for
  files in a similar way and have the same retired-files gap and last-moment
  gap. They are filed as follow-up work, not changed here.
