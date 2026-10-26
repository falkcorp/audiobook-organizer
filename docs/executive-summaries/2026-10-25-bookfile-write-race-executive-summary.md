<!-- file: docs/executive-summaries/2026-10-25-bookfile-write-race-executive-summary.md -->
<!-- version: 1.0.6 -->
<!-- guid: 3e8f1a5c-6b72-4d09-9c4e-d2a7f0b1c853 -->
<!-- last-edited: 2026-10-26 -->

# Stopping two library jobs from silently undoing each other's changes

- Pull request: [#4169](https://github.com/falkcorp/audiobook-organizer/pull/4169) (merge commit to be filled in when it merges)

## Executive Summary

- **What could go wrong.** The library keeps one record for every audio file:
  its location, its content fingerprint, its length, and so on. Several jobs
  update those records, and some of them work from a copy they read a moment
  earlier. If a second job changed the same record in that moment, the first
  job's save could quietly put the old values back.
- **What it looked like.** A change a scan or an import had just made could
  disappear with no error, and the lookup tables that find files by content or
  by iTunes ID could be left pointing at values the record no longer held. The
  only symptom would be a file that later looked "unscanned" or a duplicate that
  was not recognised.
- **Was anything lost?** No loss has been found in production. The window was
  small and it needed two jobs to touch the same file at the same instant, which
  is why it went unnoticed. It was found in code review, not from a report.
- **What this change does.** Before a job saves a record, the library now checks
  that the record is still exactly what the job read. If it changed, the job
  reads it again, redoes its own change on top of the newer record, and tries
  again. If the record keeps changing, the job stops and reports that the file
  was busy rather than writing something out of date.
- **Maintenance sweeps.** The two whole-library sweeps (clearing fingerprints
  and removing leftover fingerprint columns) used to write without any of this
  protection and could put back a record exactly as it was minutes earlier. They
  now save in small groups with the same check. A file they cannot update is left
  as it was and counted, and the segment-cleanup job no longer marks itself
  finished when it had to skip files, so the next run picks them up.
- **Checked by.** Tests that deliberately make a second job change a record in
  the gap, then confirm the second job's changes and lookup entries survive and
  the first job's change still lands, plus a test that a record changing on every
  attempt ends in a clear "busy" error with nothing written. Removing the check
  makes those tests fail.

## The gap between reading a record and saving it

**What it was.** A job reads a file's record, changes one field, and saves it
back. The library protected the save with a lock on the book, but the job had
read the record before it took that lock. The library only checked that the
record still existed, not that it was unchanged.

**Why it mattered.** Jobs such as the import that claims an iTunes ID for a
file, the moves of files between books, deletions and the fingerprint sweeps do
not take that lock, so they could change the record inside the gap. The slower job then
saved its older copy over the newer one and left the lookup entries from the
newer copy behind.

**The fix.** The save now compares the stored record with the copy the job
read, while holding the lock. A mismatch makes the job start over from the
current record; the lookup entries are always worked out from that current
record. The same rule now covers every job that updates a file record: hash
and metadata updates, field patches, intro transcripts, scan bookkeeping, file
moves between books, and retiring or restoring old versions. It also covers
deleting file records, which had the same gap in the other direction: a delete
that worked from an older copy could remove the old lookup entries and leave the
newer ones pointing at a record that no longer existed.

## The whole-library sweeps

**What it was.** Two maintenance jobs read every file record up front and
rewrote them minutes later, saving without any lock and without checking that
the record was unchanged.

**Why it mattered.** Over a run of several minutes, any record a scan touched
meanwhile could be reverted to its state at the start of the run.

**The fix.** The sweeps now save in small groups under the book locks with the
same check, re-read and re-judge any record that changed (following it if it was
moved to another book meanwhile), and give a record that keeps changing one more
pass at the end. A record still busy after that is left as it was and counted. If
the fingerprint reset is left with such records, its final message says the reset
is incomplete, it keeps its search index, and it asks for the reset to be run
again. (The job itself still finishes without an error; the message is where the
incomplete state is reported.)

## Follow-up review fixes

- **Deleting several file records at once** now names the book each record is
  expected to belong to. A record that was moved to a different book in the
  meantime is left alone and the delete is refused, because the reason for
  deleting it (a duplicate inside the first book) no longer applies; it may now
  be the only copy of its file there. The duplicate-removal job also keeps its
  undo log accurate when a delete removed nothing, or removed a slightly different
  version of a record than the one it logged.
- **A busy batch no longer gives up completely.** When some of a batch's records
  keep changing, those are refused and the others are saved, instead of
  abandoning every record. Between attempts the batch now waits a short, random
  time and re-reads only the records that changed.
- **Very large deletes** are now carried out 64 records at a time, so a
  delete of thousands of records cannot use a gigabyte of memory. Every reason to
  refuse is still checked for the whole delete before the first record goes; if a
  later group of 64 then fails, the records already removed are reported along
  with the error.
- **Combining two books** when some files are busy is now answered as "try again
  later" before any file moves, instead of leaving the combine half done.
- **A damaged record** that is filed under a different book than it names is now
  reported by name instead of being treated as missing and retried until it
  looks busy.
