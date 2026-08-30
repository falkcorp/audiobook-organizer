<!-- file: docs/executive-summaries/2026-08-29-the-delete-that-left-its-index-behind-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b6c1e58-9d47-4c02-8a71-51f4b0c9d2ae -->
<!-- last-edited: 2026-08-29 -->

# "Clear the activity log" never cleared all of it

## What was wrong

The activity log — the running history of what the app did to your library, and when —
stores every entry three times over. Once as the entry itself, and twice more as tiny
signposts: one filed under the job that produced the entry, one filed under the book it
touched. The signposts are why opening a book's history, or a job's transcript, is
instant instead of a search through the entire log.

When an entry was deleted, only the entry went. Both signposts stayed. Forever.

Nothing in the app deleted them — not the nightly tidy-up, not the compaction that rolls
old entries into daily digests, and not the "clear the activity log" button, whose entire
job is to leave nothing behind.

## Why it mattered

Two ways.

**The clear button did not clear.** If you wiped your activity log, the entries went, but
a signpost survived for each one — and the signpost is not anonymous. The job identifier
and the book identifier are written into the signpost itself. So a log you asked to be
emptied still held a record of which books had been worked on.

**It grew without limit.** Every entry ever written left two permanent signposts, whether
or not the entry itself survived a week. On the live server the activity log had reached
about 1.34 GB, of which roughly 0.78 GB — around 60% — was job signposts, most of them
pointing at entries deleted months ago. Nothing in the system could remove them, because
nothing knew they were there.

## What changed

**Deleting an entry now deletes its signposts too**, in the same write, on all four paths
that delete entries. The identifiers needed to find the signposts are read from the entry
itself — which those paths were already reading anyway, so nothing got slower. "Clear the
activity log" additionally sweeps both signpost families outright, so it is now true to
its name even for entries too damaged to read.

**The signposts already stranded now have a way out.** A new repair pass looks for
signposts pointing at entries that no longer exist and removes them. It runs as the last
step of the nightly activity-log tidy-up and reports how many it removed. It is safe to
repeat: once the backlog is cleared, each night's run finds nothing and says so. On the
live server the first run is expected to reclaim the bulk of that 0.78 GB.

## What this does not fix

The repair only removes signposts whose entry is gone. It does not add signposts that
were never written — in particular, when the tidy-up replaces a group of old entries with
a single summary row, that summary is still not filed under the job it belongs to, so it
will not appear when you open that job's transcript. That is a separate gap, recorded as
follow-up work rather than changed here, because writing the signpost incorrectly would
have created a fresh version of the problem this change exists to remove.
