<!-- file: docs/executive-summaries/2026-10-10-import-orphaned-versions-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 8c3f6a25-d4e1-4b97-a0c2-5e9d17b8f463 -->
<!-- last-edited: 2026-10-10 -->

# Import: every imported file is now tied to its version and carries its checksum

Branch `fix/import-orphaned-versions`.

## Executive Summary

- **Every import left a loose end.** When a file was imported, either by hand
  or from a torrent client, the app recorded a "version" of the book (where
  the copy came from, and which torrent it belongs to) and a separate record
  of the file itself. The two were supposed to point at each other. They
  never did, because the version was recorded before the file record existed,
  so there was nothing for it to attach to.
- **The file record was also missing its checksum.** The step that should
  have stored the file's content checksum (a short code computed from the
  file's bytes, used to spot exact duplicates and to confirm a file is the one
  a torrent delivered) was the same step that failed to attach, so imported
  files were stored without one.
- **The torrent cleanup tool could not vouch for imported files.** Without the
  checksum and the link, every imported file showed up as "cannot be
  verified" until a separate background job filled the checksum in later,
  and duplicate detection could not match an imported file to an identical
  copy until then.
- **The order is now fixed.** The file is checksummed once, its record is
  saved with that checksum, and only then is the version recorded and
  attached to it, with the torrent link written in the same step as the
  attachment.
- **Failures leave something repairable, never something to delete.** If
  recording the version fails, the file record is kept with its checksum
  and simply has no version yet; a later repair pass can attach one. If the
  version was recorded but attaching it failed, a later repair pass can attach
  that same version instead of recording a second one, and the version step
  leaves a file that already has a version unchanged.
- **Files imported before this fix are not repaired yet.** Each still has a
  version that points at nothing. Because the version step now picks up such a
  stranded version instead of duplicating it, a one-time repair pass over them
  can build on it; it has not been written or run.
- **Two imports of the same torrent at once can no longer both claim it.** A
  double click or a retried request used to be able to attach one torrent to
  two books, or to leave a second, unlinked copy of the book behind. Imports of
  the same torrent now take turns, and the second one is turned away (the same
  "already in the library" answer a plain retry gets) before it creates
  anything. The version step also refuses on its own to take a torrent another
  live version holds.
- **Verified by tests that failed before the fix**, against the real database
  engine rather than a stand-in, which is how the problem had gone unnoticed.

## The fix in brief

**What it was.** Two records that must point at each other were written in the
wrong order, so the link and the checksum were silently skipped on every
import.

**Why it mattered.** Imported files could not be verified by the torrent
cleanup tool and could not be matched as exact duplicates until a later
background pass ran. Nothing reported the problem except a warning in the log.

**The fix.** The file record is now written first, with its checksum, and
the version is attached to it immediately afterwards. The version step also
became safe to repeat: it reuses a version that was recorded but never
attached, so a half-finished import can be completed without removing or
duplicating anything.
