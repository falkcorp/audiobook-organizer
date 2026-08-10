<!-- file: changelog.d/20260810-search-index-reconciliation.md -->
<!-- version: 1.0.0 -->
<!-- guid: c41d90e6-3a75-4b28-91ef-7d5c02a4b8f3 -->
<!-- last-edited: 2026-08-09 -->

### Fixed

- **The search index silently dropped updates and nothing ever repaired them.**
  When the index worker's bounded queue filled up during bulk operations, the
  update was discarded with only a log line — 56,537 dropped operations in the
  seven days to 2026-08-10. Dropped events are now recorded in a durable
  dirty-set and repaired by a reconciler that drains on a 30s ticker, so the
  index converges back to the database instead of diverging permanently.

  Three code comments claimed the drop was safe because "a startup reindex
  will heal any gaps". It does not: `buildSearchIndexIfEmpty` returns early
  unless the index has zero documents, so on a populated library it had never
  run. All three comments are corrected in place rather than deleted, so the
  false claim cannot be reintroduced by someone reading the old reasoning.

### Changed

- The dropped-event log line no longer hardcodes `(delete)` while reporting
  `del=false`, which made every upsert drop read as a deletion. It now names
  the operation correctly and carries a running `dropped_total`, so the
  condition is visible without grepping the journal.
