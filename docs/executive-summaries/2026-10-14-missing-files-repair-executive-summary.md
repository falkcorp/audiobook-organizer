<!-- file: docs/executive-summaries/2026-10-14-missing-files-repair-executive-summary.md -->
<!-- version: 1.5.0 -->
<!-- guid: 9b3fe5ba-cb9d-49bf-b3da-0b4222fff839 -->
<!-- last-edited: 2026-10-15 -->

# Repair: books whose files went missing, reviewed one row at a time

Branch `task/03-pr9b`. Spec: `docs/proposals/2026-10-holistic/03-dedup-page-retirement.md`
(section 3.2, G8, rows C36 to C38) and task brief 03-PR9b.

## Executive Summary

- **The old Reconcile tab moves to the Repairs tab.** The "Match missing files"
  scan looks for books whose file is no longer on disk and tries to find that
  file somewhere else (by its contents, or failing that by its name). Its
  results used to be reviewed on a tab of the old duplicates page. They are now
  a "Missing files" repair, so the duplicates page can be retired.
- **One row per book.** Each book the scan found a file for is a row showing
  where the book used to point, the file it was matched to, how it was matched
  and how sure the match is. Each book the scan found nothing for is listed too,
  marked "no candidate file", so every missing book can still be counted and
  clicked.
- **It reads the same scan the old tab showed.** The repair uses the newest
  scan that finished and saved readable results, by the same rule as the old
  tab, including the nightly scans recorded under that job's old name. A newer
  scan that was stopped or failed does not hide the last good one.
- **Every match is checked again before it is used.** A match is offered only
  while the book still points at the missing file, that file is still gone, the
  matched file is still on disk (and has not been changed), and no other book
  already uses it. A folder that has come back counts as present, and a place
  the server cannot read is reported as an error, never as "missing". The
  check runs again when you apply, and once more just before anything is
  written; a row that changed in between is refused with nothing written.
- **iTunes is left alone.** A book that came from iTunes, or a match onto a
  file inside the iTunes library, is listed as skipped with the reason "iTunes"
  and is never applied.
- **Applying fixes the book and then its file record, and can be undone.**
  (Owner decision 2026-10-14.) Applying a row makes two checked changes, one
  after the other: it points the book at the matched file, then moves the
  book's file record to it. Each change is recorded so that undoing the apply
  on the Operations page puts both back. If the apply is stopped between the
  two (cancelled, or the server restarts), resuming it, or starting a new
  apply of the same row, finishes the second change instead of refusing the
  row. A new apply that finishes an earlier one takes the whole change over
  from it: undoing the new apply puts the book fully back, and undoing the
  earlier one changes nothing for that book. Whichever order they are undone
  in, the book and its file record never end up pointing at different files.
  A book only counts as half done when this repair's own record says so.
  If something else already pointed the book at the matched file (for
  example the old "Reconcile Apply" job), the row is shown as changed and
  is not applied.
  Nothing is moved on disk and no file record is created or deleted. The
  repair no longer goes through the old "Reconcile Apply" job, which only
  updated the book and left its file record pointing at the missing file.
- **Right after a restart it waits instead of guessing.** For a few minutes
  after the server starts, it cannot yet tell for certain whether another book
  already uses a file. During that time the repair says "file index warming
  up; retry in a few minutes" instead of offering rows it cannot check.
- **Only administrators can run it.** Repointing books at files needs the
  "manage settings" permission. The old apply job declares that same
  permission, but its own button checks only the weaker "start scans"
  permission; which of the two is right is listed for the owner to decide.
  Repairs can now ask for a stronger permission than the usual "edit
  metadata" one, and this is the first that does.
- **One file is never given to two books.** If the scan matched the same file
  to more than one book, every one of those rows is skipped for a person to
  decide, rather than the first one winning.
- **Fingerprints follow the file.** When the match was made by the file's
  contents, the book keeps the content fingerprints it had. When it was made
  some other way, the fingerprints that described the old file are cleared, so
  the duplicate finder does not compare the book on content it no longer
  points at. Undoing the apply puts them back. They are not refilled on
  their own yet: the file-level fingerprint comes back when someone runs the
  "Backfill File Hashes" job (it is not scheduled, and it previews by
  default), but the book-level ones stay empty until a scan re-reads the
  file or the book is organized. Whether the repair should fill them itself
  is on the TODO list for the owner to decide.

## What is not changed

- The scan itself is unchanged, and so is the old "Reconcile Apply" job used
  by the old Reconcile tab: it still updates only the book's main file
  location. That gap, and a mismatch between the permission that job declares
  and the one its own button checks, are recorded in the TODO list for the
  owner to decide.
