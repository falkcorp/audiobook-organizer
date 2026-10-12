<!-- file: docs/executive-summaries/2026-10-11-series-prune-fixer-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9655a812-fe48-4e5f-89a7-6584be12a076 -->
<!-- last-edited: 2026-10-11 -->

# Repair: prune duplicate and empty series, one reviewed row at a time

Branch `task/03-pr9a`. Spec: `docs/proposals/2026-10-holistic/03-dedup-page-retirement.md`
(section 3.2, G8, row C34) and task brief 03-PR9a.

## Executive Summary

- **The old "Prune Series" button moves to the Repairs tab.** The Series tab
  of the old duplicates page offered a preview and one big confirm that merged
  every duplicate series and deleted every empty one at once. The Repairs tab
  now has a "Prune series" repair that lists each change as its own row, so the
  owner can read and pick them before anything is written.
- **Two kinds of row.** A duplicate row is two or more series with the same
  name (ignoring capitals and stray spaces) and the same author; applying it
  moves every book of the extra copies to the kept series, other versions of a
  book included, and removes the emptied copies. An empty-series row is a
  series no book is listed in; applying it removes that series and changes no
  book.
- **It uses the nightly job's rules, not the old preview's.** The nightly
  prune already skipped series kept for an undo and refused to delete a series
  any book still names. The old preview did neither, so it showed changes the
  job would not make. The new rows show what applying will really do.
- **Nothing a book still points at is ever removed.** A series is only removed
  when no book in any state, trashed books and other versions included, names
  it. Rows that fail this are listed as "still referenced" and cannot be
  applied, because removing them would leave books naming a series that no
  longer exists (the damage behind 6,893 missing series found in August).
- **Series repairs never run at the same time as other series changes.** The
  repair and the nightly prune now wait for the series rename, normalize and
  merge jobs (and those wait for them), and each removal re-counts, right
  before it happens, whether any book has started pointing at that series in
  the meantime. Each step is written to the change log before it is made, so
  a run stopped in between never leaves a change with no record.
- **The nightly job and the repair share one piece of code.** The merge and
  delete steps were moved out of the nightly job, unchanged, so both use them.
  A recording of the job's full output taken before the move still matches
  after it.
- **Applying is recorded, not undoable.** Each moved book and removed series
  is written to the apply's change log, but a removed series cannot be brought
  back under its old number, so the repair's description makes no undo
  promise.

## Moving the prune into Repairs

- **What it was.** The prune ran over the whole library in one step, either
  nightly or from one confirm button. There was no way to look at a single
  merge and decide against it.
- **Why it mattered.** Merging series changes which series books belong to,
  and removing a series cannot be reversed. One button for every change, with
  a preview built on different rules than the job, asked the owner to approve
  changes he could not see.
- **The fix.** A Repairs repair plans every duplicate group and empty series
  as its own row, re-checks each row right before writing it (a row whose
  books or counts changed since the plan is refused), writes through the same
  guarded path as the other repairs, and refreshes the series list when the
  apply finishes so the result shows without waiting a day for the cached list to
  expire.

## One copy of the merge and delete steps

- **What it was.** The merge and delete logic lived inside the nightly job and
  could only run for the whole library.
- **Why it mattered.** A second copy for the repair would drift from the
  first, and the job's safety checks were added one production incident at a
  time; a copy that missed one would bring the incident back.
- **The fix.** The steps were moved out unchanged and both the nightly job and
  the repair call them. The job's existing tests and a before-and-after
  recording of its output confirm nothing about the nightly run changed, except
  one new warning line if the change log ever refuses to record a step.
