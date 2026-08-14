### Fixed

- **The store-consistency guard that protects merges could never fire in any merge
  test.** Invariant (a) — no book is both a live primary version and marked for
  deletion — exists to catch a merge that half-applied. The shared helper enumerated
  books through a listing that excludes soft-deleted rows, so the contradictory book
  was never even looked at: the assertion ran, passed, and examined nothing. A
  white-box version of the check does work, but it is reachable only from tests
  inside the database package, which perform no merges, while every merge, combine
  and regroup test in the tree uses the shared helper. The guard was live only where
  nothing could violate it and blind everywhere the hazard actually is.

  The helper now enumerates the live and soft-deleted listings together. Both are
  public, so no white-box access was needed — the original reasoning stopped one
  exported method short of the fix. A new test constructs the contradictory state
  and asserts the invariant reports it, and was confirmed to fail when the union is
  removed.
