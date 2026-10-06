<!-- file: docs/executive-summaries/2026-10-06-trash-cleanup-stays-inside-the-library-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: ee2cdb77-40bc-45eb-8b5e-c36d037f7292 -->
<!-- last-edited: 2026-10-06 -->

# Trash cleanup only deletes files inside the library

This follows up the 2026-10-05 change "The trash keeps your listening
progress" (PR #3777) and closes the gaps a review of it found.

## Executive Summary

- **Emptying the trash never deletes a file outside the library folder.**
  The cleanup can also delete a trashed book's audio file from disk (when
  that setting is on). It was meant to do this only inside the library
  folder, but nothing checked. A book pointing at a file somewhere else (a
  folder next to the library, a downloads folder, a shortcut that leads
  outside) could have had that file deleted. Now such a file is always kept,
  and the cleanup report names it.
- **Undoing a merge no longer moves you backwards.** If you kept listening
  while an undo was running, the undo could put your place back to an older
  spot. It now checks for newer listening right before it writes, and keeps
  the newer place.
- **Undoing an Audible import can't wipe your place half way.** Putting back
  the old positions is now one step. Either it all happens or nothing
  changes.
- **Clearer reasons when a book can't be purged.** Some books are refused on
  every run because their link history is damaged. The report now says
  that, instead of a vague "could not read" that looks like it will fix
  itself.
- **Clearer errors in the app.** A book that still owns files, or whose
  progress could not be moved, now gets its own error code, so the trash
  page shows the right message.
