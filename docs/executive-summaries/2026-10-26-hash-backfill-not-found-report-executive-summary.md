<!-- file: docs/executive-summaries/2026-10-26-hash-backfill-not-found-report-executive-summary.md -->
<!-- version: 1.7.0 -->
<!-- guid: d7aaaf61-bcb5-45c2-947a-ffd1e4655e3c -->
<!-- last-edited: 2026-10-26 -->

# The file-hash backfill now reports missing files instead of warning about each one

- Pull request: #4174 (replaces #4158 and #4170)
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
  person can read what is missing and decide what to do. A restarted run of the
  same job skips the files already listed; a brand new run checks them again, so
  a file that has come back is fingerprinted then.
- **Nothing is changed on the book records.** A missing file leaves its record
  exactly as it was. The only thing the job ever stores is the fingerprint of a
  file that exists.
- **Interrupted runs now finish with the right answer.** The job takes hours and
  can be restarted by a server restart. A restarted run used to write some report
  lines a second time and restart its totals from zero. Now it picks up the
  earlier report, skips what is already listed, and its final totals cover the
  whole run, with no line listed twice, and a restart can no longer wipe the
  earlier part of the report.
- **The report cannot be used to alter book records.** The report folder sits
  inside the shared library. When a restarted job reads its report back it only
  uses it to skip and count files, so a doctored report cannot make the job write
  a fingerprint to any record, and the notes the job keeps for crash recovery are
  held in the database rather than beside the report.
- **A fingerprint is only saved on the right record.** If a book's file record
  was changed while the job was working on it, the job no longer saves a
  fingerprint that belongs to the old file. Such a record is reported as "busy"
  and picked up again on the next run.
- **What a "not found" does and does not prove.** A file outside the library
  folder, or any file when no library folder is configured, is counted separately
  as "unverified", because the check that the library drive is attached says
  nothing about other locations.
- **Refusals.** The job now refuses to start, before it touches anything, in four
  cases (listed below).
- **Verification.** Covered by automated tests, including one that stops a run in
  the middle of a batch, resumes it, and checks that no file is listed twice and
  that the totals match an uninterrupted run, and others that stop it at each
  point where a restart could lose progress. Not yet run against the production
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
  totals also started again from zero. Worse, if the bookmark was missing or
  could not be read, the resumed run started a fresh report over the same name and
  erased what the earlier attempt had listed.
- **Why it mattered.** After a restart the report would list some files twice, or
  lose the earlier part entirely, and the final totals would describe only the
  second part of the run, while a person reads the report to decide what to do
  about 515,000 files. Nothing in the database was affected.
- **The fix.** The job no longer relies on a bookmark to know where to restart.
  Every run starts from the first file, skips any file that already has a
  fingerprint or is already listed in the report, and starts its totals from the
  report, so the final totals cover the whole job. This is about restarting the
  same job: a new run starts with a new report and looks at missing files again. A report belonging to an
  earlier attempt is never started over. Each batch's report lines are written only
  after the batch is safely recorded, so a batch that is stopped half way leaves
  nothing behind and is simply redone. If the job knows it was started before but
  finds no earlier report, or cannot read what it saved, the summary says its
  totals may cover only part of the job. A report whose last line is missing its
  line break, or that was saved with Windows-style line breaks, is still read.

  Before a batch is stored, the job also writes a small note listing the
  fingerprints it is about to save for that batch (nothing is noted for files that
  are missing or unreadable, since nothing is saved for them, and a preview writes
  no note) and forces it to disk, and removes the note only once
  the batch's report lines are safely on disk. The note is kept in the job's own
  saved record in the database, not in a file. If the server is killed at any
  point in between, the next run finds the note and finishes the batch: it lists
  and counts the files not yet in the report, and does not fingerprint again any
  file whose fingerprint is already stored. So even a hard kill part way through
  a batch leaves the report and the totals exact. The note goes away with the
  job's saved record, so there is nothing left to clean up.

  The report itself is kept in a folder inside the library, which other people
  with access to the library may be able to edit. When a restarted job reads it
  back, it uses it only to decide which files to skip and what to count. It never
  takes a fingerprint, a file location or anything else from it, and a line that
  does not look like a real entry stops the job with an error. A doctored report
  can therefore make the job skip a file, but never write anything to a book
  record. The report is created with the standard file permissions the rest of the
  application uses (owner and group can write), and the job will not write through a shortcut (symbolic link) left in its place.
  Before it reads a report back, it checks that the file is an ordinary file
  made by the same user the server runs as, that it is not unreasonably large,
  and that no line is unreasonably long. The reports folder itself is checked
  the same way before anything is written to it: if it is a shortcut to somewhere
  else or belongs to another user, the job refuses to use it, and if it belongs
  to the server's own user but anyone at all can edit it, the job removes only that
  "anyone can edit" permission and records that it did. Group access and every
  other setting on the folder are left as they were, so people who could use it
  before still can. This check is shared by the other jobs that write reports;
  on a folder that is not open to everyone (the production one is not) it changes
  nothing, and nobody has to do anything by hand before the deploy.

  Saving the fingerprints also updates each book's totals (such as its overall
  size and duration). The job used to refresh a book's totals once for every file
  it fingerprinted; it now refreshes each book once per batch, however many of its
  files were fingerprinted in that batch. If that refresh fails the job stops and
  the next run repeats it, and a run that was stopped after saving fingerprints but
  before refreshing the totals still refreshes them when it is resumed.

  While running, the job also checks again that the library drive is still
  attached whenever a batch finds files missing. If the drive drops out partway
  through, the job stops with an error and keeps the earlier part of the report,
  instead of listing the rest of the library as missing.

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
