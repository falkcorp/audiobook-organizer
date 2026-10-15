<!-- file: docs/executive-summaries/2026-10-14-missing-files-repair-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9b3fe5ba-cb9d-49bf-b3da-0b4222fff839 -->
<!-- last-edited: 2026-10-14 -->

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
- **Applying fixes the book and its file record together, and can be undone.**
  (Owner decision 2026-10-14.) Applying a row points the book at the matched
  file and moves the book's file record to it in the same step, each change
  recorded so that undoing the apply on the Operations page puts both back.
  Nothing is moved on disk and no file record is created or deleted. The
  repair no longer goes through the old "Reconcile Apply" job, which only
  updated the book and left its file record pointing at the missing file.
- **Right after a restart it waits instead of guessing.** For a few minutes
  after the server starts, it cannot yet tell for certain whether another book
  already uses a file. During that time the repair says "file index warming
  up; retry in a few minutes" instead of offering rows it cannot check.
- **Only administrators can run it.** Repointing books at files needs the
  "manage settings" permission, the same as the old apply job. Repairs can now
  ask for a stronger permission than the usual "edit metadata" one, and this
  is the first that does.

## What is not changed

- The scan itself is unchanged, and so is the old "Reconcile Apply" job used
  by the old Reconcile tab: it still updates only the book's main file
  location. That gap, and a mismatch between the permission that job declares
  and the one its own button checks, are recorded in the TODO list for the
  owner to decide.
