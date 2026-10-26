<!-- file: docs/executive-summaries/2026-10-26-hash-backfill-not-found-report-executive-summary.md -->
<!-- version: 1.2.1 -->
<!-- guid: d7aaaf61-bcb5-45c2-947a-ffd1e4655e3c -->
<!-- last-edited: 2026-10-26 -->

# The file-hash backfill now reports missing files instead of warning about each one

- Pull request: [#4170](https://github.com/falkcorp/audiobook-organizer/pull/4170) (replaces #4158, redone as a single clean change)
- Related: the earlier design that marked missing files on each book record was rejected by the owner and is not part of this change

## Executive Summary

- **The noise.** The job that computes a fingerprint (a hash) for every audio file
  used to log a warning for each file it could not find. On the production
  library, about 515,000 files had no fingerprint yet, and every one sampled was
  simply gone from the server. One run would have buried the activity log under
  hundreds of thousands of identical warnings.
- **What it does now.** A file that is not on disk is counted as "not found"
  instead of warned about. Only the first 50 are written to the activity log.
  Every file the run looks at that had no fingerprint is written, one line each,
  to a report file kept in a hidden reports folder inside the library, so a
  person can read what is missing and decide what to do.
- **Nothing is changed on the book records.** A missing file leaves its record
  exactly as it was. The only thing the job ever stores is the fingerprint of a
  file that exists.
- **Interrupted runs now finish with the right answer.** The job takes hours and
  can be restarted by a server restart. A restarted run used to write some report
  lines a second time and restart its totals from zero. Now it picks up the
  earlier report, skips what is already listed, and its final totals cover the
  whole run, with no line listed twice.
- **What a "not found" does and does not prove.** A file outside the library
  folder, or any file when no library folder is configured, is counted separately
  as "unverified", because the check that the library drive is attached says
  nothing about other locations.
- **Refusals.** The job now refuses to start, before it touches anything, in four
  cases (listed below).
- **Verification.** Covered by automated tests, including one that stops a run in
  the middle of a batch, resumes it, and checks that no file is listed twice and
  that the totals match an uninterrupted run. Not yet run against the production
  library.

## Reporting missing files instead of warning about each

- **What it was.** Each missing file produced its own warning, on every run, with
  nothing to say which ones mattered or how many there were.
- **Why it mattered.** The warnings made the activity log unreadable and gave no
  usable list. The earlier attempt to fix it by recording a marker on each book
  record added a way to overwrite a concurrent edit, so it was dropped.
- **The fix.** Missing files are counted and listed in a report. Permission
  errors and drive errors are kept apart from "not found" and are retried on the
  next run, because they do not mean the file is gone.

## Resuming an interrupted run

- **What it was.** The report was written continuously while files were being
  processed, but the "resume from here" bookmark was only saved after each batch
  of 512 files. A run stopped in the middle of a batch had already written part of
  that batch to the report, and the resumed run wrote the same lines again. Its
  totals also started again from zero.
- **Why it mattered.** After a restart the report would list some files twice and
  the final totals would describe only the second part of the run, while the
  report described all of it. Nothing in the database was affected, but the report
  is the thing a person reads to decide what to do about 515,000 files.
- **The fix.** Each batch's report lines are now held back and written only just
  before that batch's bookmark is saved, and the fingerprints for a batch are
  stored at the same point, so a batch that is stopped half way leaves nothing
  behind and is simply redone. When a run resumes it reads the report it already
  wrote, skips the files it lists, and starts its totals from it, which is why the
  report now also lists files that were fingerprinted. If the earlier report is
  missing, the summary says its totals cover only the resumed part.

  Before a batch is stored, the job also writes a small note listing what it decided for that batch and forces it to disk, and removes
  the note only once the batch's report lines are safely on disk. If the server
  is killed at any point in between, the next run finds the note and finishes the
  batch: it lists and counts the files not yet in the report, and does not
  fingerprint again any file whose fingerprint is already stored. So even a hard
  kill part way through a batch leaves the report and the totals exact.

## When the job refuses to start

- **What it was.** The job used to start no matter what. If the library drive was
  not attached, every file looked missing and the report would have said the whole
  library was gone.
- **Why it mattered.** A misleading report of that size could lead to wrong
  clean-up decisions.
- **The fix.** When a library folder is configured, the job refuses to start, a
  preview included, in four cases: the library folder cannot be read; it is not
  written as a full path starting from the top of the drive; it holds nothing but
  hidden entries, which is how a drive that is not attached looks; or the hidden
  reports folder inside it cannot be written. The attached-drive check is coarse.
  It catches an empty or hidden-only folder and nothing more, so it does not prove
  the drive is healthy. The same shared check is now used by the repair tool that
  finds leftover files after consolidation, which therefore also refuses a library
  folder holding only hidden entries, where it used to accept one.
