### Fixed

- **Series and import-path counts included books in the trash during startup.**
  `GetAllSeriesBookCounts`, `GetAllSeriesFileCounts` and `CountBooksByPathPrefix`
  each have two backing implementations, and the memdb ones excluded soft-deleted
  books while the Pebble ones counted them. For the roughly two minutes it takes
  memdb to warm up after a restart, a series therefore reported more books and
  files than it has, and an import path reported more books than it holds.

  This is the same defect class as the soft-deleted rows that leaked into
  `GetAllBooksCore`, but leaking from the other store — which is the argument for
  testing the two implementations against each other rather than auditing either
  one: whichever path a reviewer reads, the bug is in the one they did not.

- **`CountBooksByPathPrefix` could never reach its own Pebble path.** It chose an
  implementation based on whether memdb was published, ignoring the flag that is
  supposed to select between them. Besides making the fallback dead code whenever
  memdb was up, this quietly reduces any conformance test to comparing memdb
  against itself. Repairing the selector is what allowed the count bug above to be
  observed at all.
