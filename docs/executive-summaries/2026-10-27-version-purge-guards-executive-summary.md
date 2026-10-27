<!-- file: docs/executive-summaries/2026-10-27-version-purge-guards-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b7e2c90-1d58-4a36-9f0e-c3a8d51e6b27 -->
<!-- last-edited: 2026-10-27 -->

# Emptying a book's trashed versions no longer risks files it should keep

## Executive Summary

A book can keep several versions of itself (for example two recordings). When
one is moved to the trash, the nightly clean-up and the "Purge now" button
delete that version's files from disk after 14 days. A review of that deletion
found it was less careful than the deletion of a whole book, in ways that could
have lost files silently. It now follows the same safety rules as the book
purge, and when it cannot finish it says so instead of getting stuck or
reporting success.

## What changed

- **Files that must never be deleted are now protected.** A trashed version can
  no longer delete a file under an import folder, the iTunes library or a
  `.failed` folder, or any file outside the library folder.
- **An unmounted library deletes nothing.** If the library drive is not
  mounted, every file looks "already gone"; the old deletion believed it. Now
  it stops, deletes nothing, and "Purge now" says the library does not look
  mounted.
- **Files are re-checked right before each deletion.** If another book started
  using the same file after the clean-up had decided, the file used to be
  deleted anyway. The clean-up now asks again for every file, and it waits for
  any running merge to finish first, so the two cannot cross. If a merge is
  running, "Purge now" answers "try again in a moment" and nothing starts. The
  nightly clean-up does the same without holding up the night: if a merge keeps
  the lock for ten minutes it stops there, says so in its log, and finishes the
  rest the next night.
- **A version that cannot be emptied no longer gets stuck.** A file that can
  never be deleted (a permissions problem or a read-only drive) used to leave
  the version half-purged, unable to be restored or removed, and failing every
  night. It now ends in a clear "purge failed" state that lists the file and the
  reason. On the Trash tab it can be retried or hard-deleted, and the nightly
  clean-up reports it as needing attention instead of retrying it forever.
- **Files that were kept are reported.** When some of a version's files are
  left in place because another version or book uses them, the version used to
  be reported as plainly purged. The clean-up's log and result now say, for
  example, "purged 3 versions, 1 of them with files kept", and the version page
  lists which files stayed and why.
- **The Trash and Purged tabs now load.** The version page asked the server for
  these two lists, but the server had never been given them, so both tabs could
  not list anything.

## What to do after the update

Nothing. Existing trashed versions are handled the next time the clean-up runs.
A version that ends in "purge failed" appears on the Trash tab with the file and
the reason; fix the cause (permissions, drive) and press **Retry Purge**, or
press **Hard Delete** to drop the record and leave the file where it is.
