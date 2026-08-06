<!-- file: changelog.d/20260806_005000_metadata_results_swr.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e2b41c0-9738-4d5a-b0e1-27fa5c8039d4 -->
<!-- last-edited: 2026-08-06 -->

### Fixed

- **The metadata-results list no longer stalls for ~30 seconds every minute.**
  The cache expired after 60s and made the next caller rebuild synchronously — a
  build that costs ~30s on this library. Measured on production: **39.4s** on the
  first request after a restart and **28.9s** on a request 70s later with no
  restart involved. Anyone not clicking at least once a minute paid the full
  build every single time.

  Pre-warming at boot (#2152) fixed exactly one occurrence; the cliff returned
  sixty seconds later. That change alone did not deliver what it claimed.

  The cache is now **stale-while-revalidate**: an expired entry is served
  immediately while a single background rebuild runs. A stampede guard means many
  concurrent callers trigger at most one rebuild rather than one each.

  Serving stale is safe because the TTL was never the correctness mechanism —
  `invalidateMetadataResultsCache()` already fires on apply/reject/unreject, the
  only events that make an entry misleading. Freshness comes from that explicit
  invalidation; the TTL now only decides when to refresh in the background. An
  invalidation still forces the next caller onto the synchronous path, because
  after it there is no trustworthy set left to serve.
