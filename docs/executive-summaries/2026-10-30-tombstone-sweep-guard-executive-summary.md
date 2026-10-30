<!-- file: docs/executive-summaries/2026-10-30-tombstone-sweep-guard-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b3e5d27-91c4-4f06-a2d8-6e1f07c3b954 -->
<!-- last-edited: 2026-10-30 -->

# The clean-up of leftover deletion records can no longer delete a file it should not

## Executive Summary

When a book is permanently deleted, the app first writes a small "deletion record"
so a half-finished delete can be sorted out later. There is an administrator
action that clears those leftover records, and until now it also deleted the audio
file each record pointed at, without checking anything first. If another book,
even one sitting in the trash that could still be restored, was using the same
file, that file was lost. The same was true of a file outside the library folder
or in a folder the app promises never to touch (the import folders and the iTunes
library). This change makes the clean-up ask the same questions the real delete
asks, and keep the file whenever any answer is "no".

## What changed

- **The clean-up now checks before it deletes.** It refuses a file outside the
  library folder, a file in a protected folder, a file any book still uses
  (current, in the trash, or kept as an old version), and a file in a library that
  looks unmounted.
- **It only deletes the file the delete had decided on.** When a delete commits to
  removing a file, it now notes the file's identity (size, modification time and
  where it sits on the disk) with the deletion record. If a different file has
  appeared at that location since, the clean-up leaves it alone.
- **Older records are never used to delete files.** Records written before this
  change, and records of deletes that were told to keep the files, carry no such
  note. Their files are kept and the record stays.
- **Refusals are visible.** Every refusal is logged and listed in the result of
  the clean-up with its reason, together with a count, so a record left behind is
  explained rather than silent.

## What to know

- Nothing changes for a delete that finishes normally: it still removes its own
  record and its own file.
- A record the clean-up keeps will be reported again each time it is run, until
  its cause is fixed or the record is removed by hand.
- If the clean-up is run on a server set up without this safeguard, it still
  clears records but removes no files.
