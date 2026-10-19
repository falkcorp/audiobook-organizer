<!-- file: docs/executive-summaries/2026-10-18-listening-progress-lock-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: e84972e5-ef31-44af-8de7-9053b3fa1de1 -->
<!-- last-edited: 2026-10-18 -->

# Every place that saves listening progress now waits its turn

PR #4073 (branch `fix/user-state-writers-take-lock`), not yet merged.
There is no separate spec. It closes TODO item ARS-LOCK.

## Executive Summary

- **The problem.** Several parts of the app save where a reader is in a book:
  the phone apps' sync, the web player, the iTunes import, merging two copies of
  a book, and undoing a merge. Most of them took turns using a per-book lock.
  Four did not: the web player, the iTunes import (and its one-off backfill),
  and the step of a merge that moves progress from one copy to the other. When
  one of those saved at the same moment as another, one save could wipe out the
  other. A position saved by the web player while an undo was running could
  simply vanish, and a phone sync landing during a merge could be thrown away
  along with the merged-away copy.
- **The fix.** All of them now take the same per-book lock before they read the
  saved progress and keep it until they have written. Two saves for the same
  book now happen one after the other, so the later one builds on the earlier
  one instead of overwriting it.
- **Undoing a merge no longer forgets a save made during the merge.** When
  books are merged, the app writes down where each reader was before and after,
  so the merge can be undone. A save landing between those two notes could
  be left out of both and then wiped by the undo. The merge now holds the locks
  from the first note to the second.
- **Merges take two locks safely.** Moving progress between two copies needs
  both copies locked at once. A new helper takes the two locks in a fixed order,
  so two merges going in opposite directions cannot wait on each other forever.
- **The iTunes import got faster at the same time.** It used to walk the whole
  library one book at a time, twice. It now works on several books at once,
  one per processor, with each book locked only while it is being updated.
- **What was checked.** Each fix has a test that recreates the collision and
  fails if the lock is removed. A stress test runs every kind of save together
  on the same book and fails if they ever get stuck waiting on each other.

## What it means for listeners

Your place in a book is no longer lost or moved backwards because two devices,
or a device and a background job, saved at the same moment.
