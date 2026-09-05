### Fixed

- **ABS search no longer returns series that have no books.** A series row nobody
  references renders in the phone app as a black tile; on 2026-09-05 "primal hunter"
  returned 25 series of which 16 were empty duplicates of the one real row, which sat
  ninth. Search now drops series whose tile would be empty (judged by the same book grouping the tile is drawn from, not the store's count, which also counts unorganized and quarantined books), matches article-insensitively ("The
  Primal Hunter" is an exact match for "primal hunter"), and ranks the most-populated
  row first within a tier. If the grouping source fails the unfiltered list is served as a
  degraded, uncached document rather than hiding every series.
