### Changed

- Aggregate-recomputation log lines now name the subsystem that triggered them. A
  production sample counted 126,928 `RecomputeBookAggregates updated` lines across 5,595
  books (worst book: 1,189 recomputes) with no way to tell which of thirteen possible
  originators issued them. Each line now carries a `caller` field such as
  `internal/merge.(*Service).mergeBooks:438`, which is the prerequisite for coalescing the
  redundant work — and for measuring whether the coalescing helped.
