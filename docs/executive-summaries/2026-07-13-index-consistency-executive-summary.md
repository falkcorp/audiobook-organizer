<!-- file: docs/executive-summaries/2026-07-13-index-consistency-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c2e5a41-9b83-4d0f-a6e1-2f4b8c9d0e1a -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Deleted Books No Longer Haunt Work and Version Listings

## What changed

To open a book's "other versions" or "same work" list quickly, the app keeps a
small shortcut index that remembers which books belong to each group. To make
those lists load fast, the shortcut stored a little snapshot of each book right
alongside it.

The problem: that snapshot was only refreshed when a book moved to a *different*
group. If a book was deleted, hidden, or merged away — but stayed in the same
group — the snapshot kept the old "this book is here and active" information.
So a book you deleted or merged could keep showing up as a live entry in its
work list and its version list, and an edit to a book's title could fail to
appear in those same lists.

The fix stops trusting the stored snapshot. The lists now look up each book's
real, current record before showing it, and skip anything that's been deleted or
hidden. The shortcut is still used to find *which* books to check, so the lists
stay fast — they just no longer show stale ghosts.

We also found that permanently deleting a book left some of these shortcut
entries behind, which is the same ghost problem from the other direction; those
leftovers are now cleaned up when a book is deleted.

## Why it mattered

These are real lists people see in the app. A merged-away or deleted book
appearing as if it were still live is confusing and makes the library look like
it has duplicates or undead entries it doesn't actually have. No audio files
were ever affected — this was purely about which entries the version and work
lists displayed.

## Also fixed

- **Narrator creation race:** when several books were imported at once, two
  imports could try to create the same narrator at the same moment and end up
  with a duplicate or a half-written narrator record. Narrator creation is now
  done as one locked, all-or-nothing step, so concurrent imports always converge
  on a single clean record.

All fixes ship together in the database layer and are covered by new tests,
including one that deliberately creates the same narrator from many threads at
once to prove the race is gone.
