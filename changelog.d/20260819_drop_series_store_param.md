### Changed

- `SeriesRunners.ExecuteSeriesPrune` and `ExecuteSeriesNormalizeCore` no longer
  take a store parameter. They threaded a 398-method `database.Store` from the
  caller purely so the implementation could reach a store it already held; the
  parameter is removed rather than narrowed, and the nil-store guard moved to the
  implementation with it.
