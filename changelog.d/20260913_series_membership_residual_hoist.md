### Changed

- Series maintenance: `series-denumber` (dry run and apply) and the series-normalize
  positions pass now read series membership once per operation, not once per
  series. Each per-series read was a full book-table scan once the in-memory index
  was tainted. Both fail closed if membership cannot be loaded. Denumber aborts
  before writing, where it used to count the series as failed and continue.
  Normalize aborts before renaming anything, where it used to rename the series
  and lose the stripped position. Closes SERIES-MEMBERSHIP-RESIDUAL-LOOPS.
