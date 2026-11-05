### Fixed

- `/operations?op=<id>` now opens that operation on the Activity page. The
  redirect used to drop the query string and land on a bare `/activity`.
  `/maintenance` now opens the System page's Maintenance tab instead of System
  Info.
- The Library keeps a query parameter it does not manage (for example a tracking
  parameter on a pasted link) when it rewrites the address after a filter,
  search or page change.
