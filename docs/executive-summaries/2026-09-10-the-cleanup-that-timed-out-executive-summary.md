<!-- file: docs/executive-summaries/2026-09-10-the-cleanup-that-timed-out-executive-summary.md -->
<!-- version: 1.2.0 -->
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

## Follow-up, same day: the other four cleanups had the same flaw

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3218

The "only one of the two diaries" problem above was not limited to compaction.
The nightly job runs four more passes after it: folding old change entries
into summaries, deleting old debug lines, re-deriving the labels on old daily
summaries, and cleaning up dangling index entries. Every one of them ran on
the active diary store only, while every new line was still written to both.
So the second store kept every debug line past 30 days and every change line
past 90 days forever, and once reads move to the new store, the old store's
index leak would never have been cleaned again. All four now run on both
stores, the same way compaction does.

Doing that uncovered a second problem before it could bite. While the app
copies the old diary into the new one in the background, it copies a batch
and then checks that every line landed by presenting the batch a second time:
any line it has to insert again is treated as a copy that failed, which makes
the whole copy start over on the next restart. A cleanup deleting a line in
the moment between those two steps would look exactly like a failed copy. The
copy and the cleanups now take turns: a cleanup waits for the batch in flight
to finish its check, and a batch waits for a cleanup to finish, so neither can
misread the other. This also protects the compaction that the change above had
already put on the new store.

The nightly job was also taught to report progress through the passes that
just got twice as long, so the watchdog can tell slow from stuck, and the
administrator's "recompact digests" action now starts a background job and
answers immediately, the same way the Compact button does.

Tests were written before each fix and fail against the old code: one per
pass showing the second store untouched; one showing a cleanup deleting under
a copy in flight; one showing a pass reporting no progress.
