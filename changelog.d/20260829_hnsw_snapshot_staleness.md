### Fixed

- **The HNSW embedding snapshot was discarded on every restart.** The staleness
  guard compared the in-memory graph size against the raw PebbleDB row count, but
  the graph is a *filtered* projection of those rows — hydration skips empty
  vectors, stale-model rows, orphaned entities, and non-primary versions. In
  production that meant 17,706 vectors against a truth count of 39,658 (44.6%), so
  the check was true by construction and fired on every boot. The snapshot is now
  validated against the row count recorded when it was exported, so a legitimately
  filtered graph is kept and only a genuinely outdated snapshot is rebuilt.
