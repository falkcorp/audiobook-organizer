### Changed

- Series merge and cleanup maintenance (cleanup-series job, series prune
  phase 1, series normalize merge pass, dedup series, and MergeSeries) now
  read the complete book membership of every series they touch ONCE per
  operation, through the new `SeriesMembershipStore` capability
  (`database.SeriesMembershipAllVersions`), instead of once per series inside
  the loop. With a tainted memdb each per-series read was a full `book:`
  Pebble scan, so these loops were O(series x books) on the nightly window;
  the bulk read falls through to a single scan. The answer is identical to
  the per-series getter (non-primary versions included, trashed books
  excluded, same order), fails closed on error, and is kept current as the
  loop moves books (#2902).
