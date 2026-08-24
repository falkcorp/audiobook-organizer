### Fixed

#### cleanup-series stops refusing merges it is safe to make

The `cleanup-series` job compared an **unfiltered** reference count against a
**filtered** membership read. Those answer different questions, so any series
holding a non-primary version (an alternate rip of a book already in it) failed
the comparison and was refused — not once, but on every run, forever.

The guard was never wrong; it was misaligned, and refusing was the safe side to
err on. Reading the complete-set getter aligns the two so the guard now fires
only on rows the run genuinely cannot reach: trashed books (both series getters
skip soft-deleted rows) and rows the memdb counts but Pebble can no longer
hydrate. Those still refuse, and the tests that pin that behaviour are unchanged.

Two paths were affected:

- **Merging duplicate series** (`csMergeSeriesGroup`) — now repoints every
  version before removing the series row.
- **Collapsing 1-book series** — previously compared the reference count against
  the literal `1`, so "one primary book plus its alternate rips" always refused.
  It now unlinks the complete set and compares against that.

**Behaviour change worth noting:** the second path will now collapse series it
previously kept. That is the job doing what it was written to do — a series
holding one book and its alternate rips *is* a one-book series — but it does mean
more series rows are removed than before. Candidate selection still uses the
filtered getter, so alternate rips cannot make a 1-book series look like a
multi-book one and thereby escape collapsing.

`csUnlinkAndDeleteSeries` now takes the full set and is fail-closed: the series
row is deleted only after every unlink has succeeded, because deleting after a
partial unlink would strand exactly the rows that failed.
