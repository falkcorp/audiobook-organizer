<!-- file: docs/executive-summaries/2026-10-14-startup-gc-churn-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d2f4a91-3c68-4e05-b8a7-1f9e6c0d5b24 -->
<!-- last-edited: 2026-10-14 -->

# The server stops spending its first half hour cleaning up after itself

Branch `task/06-p13` (06-P13).

## Executive Summary

- **Startup no longer burns four processor cores on housekeeping.** For about
  30 minutes after every restart, a background job kept telling the
  program's memory manager to stop and tidy up, over and over, while the
  library was still loading. Those forced clean-ups are gone; the manager
  now runs on its own schedule, and the job asks for at most one.
- **The memory-growth safety check measures the right thing.** The job that
  warms the library list pauses itself if memory grows too far. It used to
  read a number that included garbage waiting to be collected, which is why
  it kept forcing clean-ups to get a trustworthy reading. It now reads the
  amount of memory actually in use, which needs no clean-up.
- **The "how many primary books" count comes from memory, not from a disk
  scan.** The count that drives the library totals used to read every book
  from disk on each refresh. It is now answered from the copy of the library
  already held in memory, and still falls back to the disk scan whenever
  that copy is not loaded or is known to be incomplete.
- **The author and series lists wait their turn at startup.** They used to
  start reading from disk at the same moment the in-memory copy was loading,
  doubling the work. They now wait for the load to finish.
- **Reading file records from disk got cheaper.** When the disk path is
  still needed, it now reads only the fields the caller uses and skips the
  large audio fingerprints.
- **Outcome.** These are the expected results, not measured ones. They
  have been checked by tests, not on the live server. The measurements to
  take after the next deploy are listed below.

## Forced clean-ups after every query

**What it was.** While the library list was being pre-computed after a
restart, the job ran a full memory clean-up and handed freed memory back to
the operating system after each query. The server holds a very large number
of small objects in memory, so each clean-up had to walk all of them.

**Why it mattered.** A profile taken after a restart showed the memory
manager using most of four processor cores for roughly 30 minutes. That
slowed every other startup task, kept the interface sluggish, and pushed the
memory ceiling closer than it needed to be.

**The fix.** The per-query and eager clean-ups, and the hand-back of memory,
were removed. One clean-up remains, taken once to record a baseline just
before the pre-computing starts. A test counts forced clean-ups across a
whole run and fails if there is more than one.

## A growth check that needed clean-ups to work

**What it was.** The safety check that stops the pre-computing if memory
grows too much compared the total allocated memory, which includes garbage
not yet collected. That figure only becomes meaningful right after a
clean-up, which is why the code kept forcing them.

**Why it mattered.** The check and the forced clean-ups were one problem:
removing the clean-ups would have made the check trip falsely or never trip.

**The fix.** The check now reads the runtime's own figure for memory in use
after the last natural clean-up. If that figure is not available yet, the
check stays off rather than guessing. The setting that controls the allowed
growth keeps its name and default; its description now says what it measures.
One thing to know: with the current memory limit on the live server, the
default allowed growth is larger than the headroom, so this check is unlikely
to trigger in practice. Lowering the setting is the owner's decision.

## The primary-book count, the author and series lists, and file records

**What it was.** Three startup readers went to disk for data that the server
already holds in memory, or decoded more than they used. The primary-book
count scanned every book. The author and series lists started reading before
the in-memory copy finished loading. The disk path for file records decoded
every stored fingerprint even though no caller reads one.

**Why it mattered.** Each one added disk reads and temporary objects during
the busiest window after a restart, which is exactly when the memory manager
was already struggling.

**The fix.** The count is answered from memory when that copy is complete,
including the tally of titles that begin with a number, and falls back to the
disk scan otherwise. The author and series lists wait for the load to finish,
for at most five minutes, and give up cleanly if the server is shutting down.
The disk path for file records reads only the fields callers need. A test
compares the memory answer with the disk answer on a synthetic library.

## What to check after the next deploy

These are expected outcomes and have not been measured:

- In a processor profile taken 10 to 30 minutes after a restart, the
  memory manager's background workers should use under 10% of CPU.
- A memory profile at 25 minutes should show the heap within 6.0 GiB.
- The count of forced clean-ups reported by the runtime should stay flat
  after the one baseline clean-up.
