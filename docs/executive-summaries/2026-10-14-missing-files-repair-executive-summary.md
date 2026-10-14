<!-- file: docs/executive-summaries/2026-10-14-missing-files-repair-executive-summary.md -->
<!-- version: 1.0.0 -->
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
  matched file is still on disk, and no other book already uses it. The check
  runs again at apply time, and a row that changed in between is refused rather
  than applied.
- **iTunes is left alone.** A book that came from iTunes, or a match onto a
  file inside the iTunes library, is listed as skipped with the reason "iTunes"
  and is never applied.
- **Applying uses the existing apply job, with only the rows picked.** Applying
  starts one "Reconcile Apply" job carrying exactly the selected matches,
  never the rest of the scan. Very large selections are split into several jobs
  so none of them exceeds the safety limit on bulk changes. That job's result,
  including any match it could not apply, is on the Operations page. No file
  record is deleted.

## What is not changed

- The scan and the apply job themselves are unchanged. The apply job updates
  only the book's main file location, not the book's per-file records; that gap
  is recorded as a follow-up in the TODO list rather than changed here.
