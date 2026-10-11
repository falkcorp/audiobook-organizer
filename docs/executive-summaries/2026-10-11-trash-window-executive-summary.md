<!-- file: docs/executive-summaries/2026-10-11-trash-window-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 13386b3f-d4c6-4717-8df2-72fc4af69037 -->
<!-- last-edited: 2026-10-11 -->

# Trashed book versions now really get 14 days

Branch `fix/trash-cleanup-from-trashed-at`. Found in code review.

## Executive Summary

- **What went wrong.** When you send one copy (a "version") of a book to the
  trash, the app promises to keep it for 14 days before deleting its files, so
  you can change your mind. It did not keep that promise for older copies. The
  daily clean-up at 6 a.m. measured the 14 days from when the copy was first
  *added to the library*, not from when it was *trashed*. So any copy that had
  been in the library for more than two weeks was deleted from disk the very
  next morning after you trashed it.
- **The fix.** Each copy now records the moment it went into the trash, and the
  clean-up counts the 14 days from that moment. Restoring a copy clears the
  record, and trashing it again starts a fresh 14 days.
- **What is already in the trash is safe.** Copies trashed before this fix have
  no record of when they were trashed. Rather than guess, the first clean-up
  after this update gives each of them a full 14 days, starting from that
  run. Nothing that is in the trash today is deleted on the first morning
  after the update.
- **"Purge now" is limited to the trash.** The "Purge now" action deletes a
  copy's files immediately. Behind the scenes it would accept *any* copy,
  including the one you actually listen to, which would have deleted the
  book's playable files. The trash page only ever offered it for trashed
  copies, but the server now refuses anything else too.
- **What this does not do.** It cannot bring back copies that the old clean-up
  already deleted. Their library records were kept, but the audio files are
  gone.

## Details

- **When the problem could happen:** any time a copy more than 14 days old was
  trashed. It was deleted at the next 6 a.m. clean-up, anywhere from a few
  minutes to 24 hours later.
- **Until the update is installed:** the old clean-up keeps running every
  morning at 6 a.m. Pausing the "Purge trashed book versions" job until the
  update is in place stops it from deleting anything more.
- **After the update:** the first clean-up deletes nothing from the existing
  trash; it only records the start of each copy's 14 days. The earliest any of
  those copies can be deleted is 14 days after that first run.
- **Library records are never removed.** Deleting a trashed copy removes its
  audio files from disk. The records describing those files stay in the
  library, as they always have.
- **The trash page** now shows the date each copy was trashed, instead of the
  date it was added.
