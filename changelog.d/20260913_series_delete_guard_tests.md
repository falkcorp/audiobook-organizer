### Fixed

- Added regression tests for the last two series-delete paths that had no
  failed-reassignment coverage. `maintenance.series-denumber` now has a test in
  which one book's reassignment fails while the reference guard passes, and
  the source series must survive. `mergeSeriesGroupHelper` (series-normalize)
  has a matching test for a failed `UpdateBook`. The guards these tests pin
  (#2983, #3189, #3190) were already merged, and every series-delete path now
  has an all-trashed test and a failed-reassignment test. This closes #2908,
  #3027 and #3028.
