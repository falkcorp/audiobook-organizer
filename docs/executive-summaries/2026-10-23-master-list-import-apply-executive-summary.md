<!-- file: docs/executive-summaries/2026-10-23-master-list-import-apply-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b1d8e47-3c92-4a60-b7f5-d04e9a2c6813 -->
<!-- last-edited: 2026-10-24 -->

# An uploaded reading list can now update what a person has read

## Executive Summary

The master-list import lets an admin upload a spreadsheet of books (titles,
authors, ISBN and Audible codes, and whether each was finished or half
listened to) and have the library's reading status match it. The first stage
only showed a preview. This stage lets the admin apply it, safely.

- **Applying is a rehearsal first.** Nothing is written unless the admin turns
  the rehearsal off, and every change that is made can be undone from the
  operation history.
- **Only clear-cut rows are written.** A row is applied only when the book was
  matched by a code with nothing against it. The import never marks a finished
  book unfinished, never overrides something the person set by hand, and never
  overwrites more recent listening done in the library itself.
- **Books the library does not have are remembered, not invented.** A row that
  matches nothing is kept as a "read elsewhere" note, listed on its own page,
  and never becomes a book.
- **Uploaded lists no longer pile up.** An admin can delete a stored list, and
  each uploader keeps only their ten newest lists by default.
- **Doubtful rows wait for a person.** A row for a book the library lacks is
  only filed as "read elsewhere" automatically when the row itself is sound; a
  bad code, a finish date in the future or an unreadable date puts it in the
  review queue instead. The same book is never filed twice because two lists
  name it by different codes.
- **The write double-checks.** Just before a change is made the import looks
  again at the person and the book: if the person was removed, or the book was
  deleted or merged into another in the meantime, nothing is written and the
  row says so.
- **Five matching gaps found in review are closed**: an ISBN-10 ending in X,
  disagreeing ISBN columns, one badly typed row spoiling a whole JSON file,
  an Audible code on two editions that the ISBN could settle, and an ISBN that
  only a merged-away copy carried.

## Why it mattered

Reading history is personal data that took years to build. A bulk import that
guessed wrong, or that replaced real listening with an older spreadsheet,
would be hard to notice and hard to repair. The rules above (rehearsal by
default, undo, never un-finish, newer listening wins) make a wrong import
visible and reversible.

## Not covered yet

The older Audible-only import is still in place; moving it onto this one is the
next stage. While the import is previewed, anyone who may view the library can
still read the preview rows (list titles, authors and the person's reading
status): closing that is a separate fix, PR #4148. The admin-only gate on the
import's routes now works when sign-in is switched off; the same gate on a few
unrelated admin routes (cache and debug tools, the wipe routes) is unchanged
and tracked.
