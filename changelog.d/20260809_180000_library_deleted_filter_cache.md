<!-- file: changelog.d/20260809_180000_library_deleted_filter_cache.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f8e1d62-94a7-4c05-b1e3-72d0ac549f86 -->
<!-- last-edited: 2026-08-09 -->

### Fixed

- **Library: the "Deleted" state filter did nothing once the page had been
  loaded.** Selecting Deleted in the filter sidebar left the full, unfiltered
  library on screen — while the Filters button still showed a count, so the
  filter looked applied. It only ever worked on a cold cache (a hard reload, or
  the very first visit). Deleted is filtered on the client, so the query sends
  no state to the server; that same empty value was also going into the results
  cache key, which made "Deleted" and "no state filter" indistinguishable to
  the cache. A cache hit returned the unfiltered list and skipped the
  client-side filtering step entirely.
