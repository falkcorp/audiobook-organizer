<!-- file: docs/executive-summaries/2026-09-09-the-cleanup-that-counted-twice-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9c41e7b2-3d68-4f05-a7de-16b90c2fa834 -->
<!-- last-edited: 2026-09-09 -->

# The cleanup that counted twice

**Pull request:** [#3167](https://github.com/falkcorp/audiobook-organizer/pull/3167)

## Executive Summary

- The app keeps a running history of everything it does — every file scanned, moved,
  renamed or repaired. That history grows fast: three repair jobs run over a single
  weekend added **ten million entries**. To stop it growing forever, a nightly cleanup
  job replaces each old day with a one-line summary — "on this day, 4.8 million things
  happened" — and deletes the individual entries.
- **That cleanup was writing the summary and deleting the entries as two separate
  steps.** If it was stopped in between — and it was being stopped by hand, repeatedly,
  for days — the next attempt found leftover entries, counted them again, and added
  them to a summary that had already counted them. A day of 5,001 entries interrupted
  after 5,000 recorded **10,001**.
- **Nobody would have noticed.** The number was plausible, it was the only record left
  once the entries were deleted, and there was nothing to check it against. The comment
  in the code stated plainly that "the total is preserved and nothing is double-counted."
  That was untrue when it was written, and nothing ever re-checked it.
- **The reason it kept being stopped by hand was a second, separate fault.** Asking the
  cleanup to compact everything made it load all 13.2 million entries into memory at
  once, inside the web request that asked for it. The server ran out of memory and
  died. It looked like a job that was too slow. It was a job that was too greedy.
- **A third fault meant the nightly version had not run since 30 August.** Three things
  had to line up: a crash in an unrelated task cancelled the eight tasks queued behind
  it, seven nights running; the cleanup's own "have I run today?" note was kept only in
  memory, on a server whose average uninterrupted lifetime was **37 minutes**; and the
  manual fallback was the one that ran out of memory.
- **One piece of good news that took real work to establish:** the cleanup was not
  broken all along. For 83 consecutive days it produced exactly one summary per day, and
  its reports of "compacted 0, summarized 0" were honest — there genuinely was nothing
  old enough to compact. The failure was recent and specific, not chronic.
- All of it is fixed. The cleanup now counts and deletes each batch of entries in a
  single indivisible step, so the entries it counted and the entries it removed are the
  same ones by construction — not by careful sequencing that an interruption can undo.

Verified with nine new tests. The double-count was proven by putting the old logic back
and watching the test report 5,002 where it should report 5,001 — the bug reproduced as
a number, not as an argument.

## 1. The summary that grew every time you interrupted it

**What it was.** Compacting a day took two steps: write the summary, then delete the
day's entries. Writing the summary added its count to whatever count was already
recorded for that day. That addition is correct when entries arrive late and genuinely
need adding. It is wrong when the entries being counted are ones a previous, interrupted
attempt already counted and did not manage to delete.

**Why it mattered.** Once the individual entries are gone the summary is the only
surviving record, and an inflated total is indistinguishable from a real one. There is
no second copy to reconcile against. And this was not a theoretical interruption — the
job was being killed by hand, repeatedly, over several days, because of the memory fault
described below. Every one of those kills corrupted the day it was working on.

**The fix.** Counting and deleting now happen together. The job claims a batch of
entries, folds their counts into the summary, and deletes exactly those entries — all
inside one transaction that either completes or does not. At every point where it could
be interrupted, an entry is either still present and not yet counted, or deleted and
counted once. There is no third state. Crucially, the deletion targets the specific
entries that were read, rather than re-running the search that found them; re-running a
search can match a different set than the one you counted.

The same defect existed in both storage backends and both were rewritten the same way.

## 2. Compacting "everything" ran the server out of memory

**What it was.** The cleanup accepts a cutoff — compact anything older than N days.
Asking for everything made it gather all 13.2 million entries into memory in one go,
and it did this inside the web request that triggered it. The server died.

**Why it mattered.** This is the fault that produced the interruptions that corrupted
the summaries in fault 1. It also disguised itself: a job that dies partway through
looks like a job that needs more time, so the natural response is to run it again — which
is exactly the action that caused the double-counting.

**The fix.** It works in bounded batches of five thousand and never holds more than one
batch. When it claims a batch it reads only the two small fields it needs to do the
arithmetic, not the full text of each entry.

## 3. The nightly job that had not run for eleven days

**What it was.** Three independent faults in a row. A different maintenance task
crashed on a null value and took down the eight tasks queued behind it — including this
one — on seven consecutive nights. The job's record of when it last ran was held only in
memory, so any restart reset the clock; against a measured average server lifetime of
37 minutes, a task scheduled every 24 hours almost never reaches its own deadline. And
the manual command someone would reach for instead is the one that ran out of memory.

**Why it mattered.** Each fault alone is survivable. Together they meant a growing
database with no working cleanup and no signal that cleanup had stopped — the schedule
still existed and still claimed the job was scheduled.

**The fix.** The "when did I last run" record is now written down rather than
remembered, so a 24-hour interval survives a server that restarts every half hour.

Two related problems were found and deliberately **not** fixed here, because they are
different faults and quietly folding them in would have hidden them: the index-repair
step that runs alongside this cleanup has no time limit of its own, and one task's crash
can still cancel the others. Both are written down as follow-ups rather than left to be
rediscovered.

## 4. What was checked and found to be working

Two claims that seemed likely were tested and turned out to be false, which is worth
recording because both would have sent the fix in the wrong direction.

The cleanup had **not** been silently failing for months: it produced exactly one
summary per day for 83 consecutive days, and only 78 entries in the entire database were
old enough to qualify for compaction. Its reports of having done nothing were accurate.

And the database does **not** need new indexes for this work. Every query the cleanup
runs was checked against real production data and each one already uses an index; none
of them falls back to sorting. Adding indexes would have cost write performance on the
busiest table in the system and bought nothing.
