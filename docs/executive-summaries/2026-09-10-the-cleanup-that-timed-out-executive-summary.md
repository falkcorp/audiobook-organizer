<!-- file: docs/executive-summaries/2026-09-10-the-cleanup-that-timed-out-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e7d1c2a-9b48-4f03-8a6e-2c1f7d9b4e60 -->
<!-- last-edited: 2026-09-10 -->

# The cleanup that timed out, and the database it never cleaned

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3214

## Executive Summary

The app keeps a running diary of everything it does: every scan, every metadata
fetch, every file moved. That diary grows by millions of lines and has to be
folded down regularly into one summary line per day, or it fills the disk and
slows every page that reads it. There is a **Compact** button on the Activity
page for exactly that, and a nightly job that is supposed to do it unattended.

Neither worked the way it looked like it did.

- **The button timed out.** Pressing it made the browser wait for the whole
  fold-down to finish before answering. On a diary the size of production's,
  that takes longer than any browser will wait, so the user saw an error and
  had no way of knowing whether the work was still going, had stopped, or had
  never started.
- **Only one of the two diaries was ever compacted.** During the move to the
  new diary storage the app writes every line to both the old store and the new
  one. The compaction, though, ran on whichever store was marked "active" and
  never touched the other. The old store kept growing.
- **The nightly job was being killed in silence.** The system has a watchdog
  that cancels any job that goes five minutes without reporting progress. The
  nightly cleanup reported nothing while it worked, so on any night with more
  than five minutes of backlog the watchdog cancelled it, logged it as
  "never reported", and nothing told anyone. It has not been possible to say
  how many nights this happened.

## What changed

Pressing **Compact** now starts a background job and answers immediately. The
job appears in the operations list at the top of the Activity page with a live
log: which cutoff it is using, each database as it finishes with how many days
were folded and how many lines removed, and a final total. It can be cancelled
between chunks. If a compaction is already running, the button says so and
names it rather than starting a second one.

The compaction itself now runs on **every** diary store that is receiving
writes, and reports the combined result. If one store fails, the other is still
compacted and the failure is reported rather than hidden.

Both the button's job and the nightly job now report progress after every chunk
of work, so the watchdog can tell a slow job from a stuck one. The watchdog
limit for this job was raised from five to twenty minutes because a single
chunk on the production store has been measured in minutes.

## How it was checked

Automated tests were written before the fix and fail against the old code:
one shows the second store untouched after a compaction and the result
reporting only one store's numbers; one shows a store emitting no progress at
all, which is what the watchdog would have killed. Both pass after the change.
Further tests cover the new job, the button's new responses, and the nightly
job's progress reporting. The change has not yet been run against production
data.

## Something to decide before running it on production

The old diary store deletes nothing immediately. Removed lines become markers
that are cleared when the store rewrites its files, and on the production disk,
where snapshots hold old copies of every file, each rewrite takes new space
before any is freed. The first "everything" compaction should be run after the
snapshot purge that is already planned, or with free space watched while it
runs. The job can be cancelled at any chunk.

## Found but not fixed

Two neighbouring maintenance routines, the digest re-derivation and the index
repair, have the same one-store-only shape. They are recorded as follow-up
work rather than changed here.
