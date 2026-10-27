<!-- file: docs/executive-summaries/2026-10-27-version-purge-guards-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4b7e2c90-1d58-4a36-9f0e-c3a8d51e6b27 -->
<!-- last-edited: 2026-10-27 -->

# Deleting old versions of a book is now much harder to get wrong

## Executive Summary

A book can keep more than one version of itself, for example two recordings of
the same story. When a version is put in the trash, the app deletes its audio
files from disk 14 days later, either during the nightly clean-up or when you
press "Purge now". That is the one place where the app removes your files for
good, and a review found it was less careful than it should be. This change
makes it refuse in every case where it is not sure, and it says so instead of
getting stuck or claiming it finished.

## What changed

- **Versions the library has set aside can no longer be thrown away by mistake.**
  Some versions are "retired": the book keeps them on the side and does not list
  them as part of itself. Those can no longer be trashed, restored, purged or
  picked automatically as the book's main version. The page answers with a clear
  message instead.
- **A trashed book that is only a shell is not brought back empty.** If a book
  in the trash holds nothing but a retired version that still has its files,
  restoring it is refused. The message says the book stays in the trash and the
  files are safe. Before, you could get back a book with no files.
- **Books that hold retired versions are reported as exactly that.** When the
  app declines to delete such a book, it now says the book is holding
  retired versions, rather than the general "this book owns files". The purge
  preview, the results and the messages on the page all use the new wording.
- **Files that other books use are never deleted.** Before each file is
  removed, the app checks again whether another version or another book uses
  it, and it waits for any running merge to finish first, so the two cannot
  cross paths. If a merge is running, "Purge now" says to try again in a moment
  and nothing starts.
- **Files that must be kept are protected.** A trashed version can no longer
  delete a file in an import folder, in the iTunes library or in a `.failed`
  folder, or any file outside the library folder. If the app cannot read its own
  list of import folders, it leaves the file alone rather than guessing.
- **A library that is not mounted deletes nothing.** If the library drive is not
  available, every file looks "already gone", and the old clean-up believed it.
  Now it stops and deletes nothing, and "Purge now" says the library does not
  look mounted. This also holds if the drive disappears in the middle of a run,
  or while the clean-up is waiting for a merge to finish: that case is treated as
  "try again later", not as a final answer. It also holds when a purge that was
  interrupted is retried. The retry used to take the folders of every file still
  waiting to be deleted as folders the earlier attempt had emptied, so an
  unmounted folder could be mistaken for an emptied one. The app now writes down
  each file it actually removed, as it goes, and a retry only trusts those.
- **A version that cannot be emptied no longer gets stuck.** A file that can
  never be deleted (a permissions problem, a read-only drive) used to leave the
  version half-purged and failing every night. It now ends in a clear "purge
  failed" state that lists the file and the reason, and the Trash tab offers
  Retry Purge or Hard Delete. Problems that may pass on their own, such as a
  folder that briefly was not empty, are retried instead.
- **Hard Delete refuses while a purge is running.** A version whose purge is
  under way can no longer be hard-deleted out from under it; the page says to
  wait or retry.
- **The Purge now message is accurate.** If the nightly clean-up already purged
  the version, the page now says it was already purged, rather than saying it
  was restored.
- **Files that were kept are reported.** When some files stay because another
  version or book uses them, the clean-up says so ("purged 3 versions, 1 of them
  with files kept") and the version page lists which files stayed and why.
  That summary is now written to the log on a night that also had a failure.
- **A merge that blocks the clean-up night after night is noticed.** If a merge
  or combine is running so long that the clean-up has to stop early, it carries
  on the next night. After three nights in a row the log and the operation
  result show a warning, and the operation page says the trash is not being
  purged, so a stuck merge does not go unnoticed. The operation itself still
  shows as successful.
- **The Trash and Purged tabs now load.** The version page asked the server for
  these two lists, but the server had never been given them, so both tabs could
  not list anything.

## What to do after the update

Nothing. Existing trashed versions are handled the next time the clean-up runs.
A version that ends in "purge failed" appears on the Trash tab with the file and
the reason; fix the cause (permissions, drive) and press **Retry Purge**, or
press **Hard Delete** to drop the record and leave the file where it is. No
version is retired yet, so day-to-day behavior does not change until a later
step starts retiring versions.
