### Fixed

- **Version lists no longer sort a nil-flagged book below the group's primaries.**
  `sortVersions` read `Book.IsPrimaryVersion` as a raw `*bool` (`!= nil && *flag`),
  so a book whose flag was never written sorted as non-primary — while every listing
  filter, and the memdb index it is built from, treat a nil flag as *primary*. The
  comparator now resolves primacy through `EffectiveIsPrimaryVersion`, the helper
  whose doc comment already named this exact hazard.
- **The same comparator was not a valid strict weak ordering.** With two primaries it
  answered `less(i,j)` and `less(j,i)` both true, so `sort.Slice` was free to emit any
  permutation of the tied rows instead of ordering them by title.
