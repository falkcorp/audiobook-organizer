### Changed

- Maintenance jobs now receive a `maintenance.JobStore` instead of the full
  `database.Store`. `Store` resolves to 398 methods across 40 sub-interfaces; the 37 jobs
  between them use 12 of those sub-interfaces — 187 methods, a 53% smaller contract. A job
  that needs a genuinely new database capability now has to add a line to `JobStore`, which
  makes widening it a visible decision rather than a silent one.
- Two metadata-cache helpers in the database package (`GetCachedMetadataFetchWithMaxAge`,
  `PutCachedMetadataFetch`) now take the `RawKVStore` they were already limited to using,
  rather than the whole store.

### Removed

- The write-only package-level store global in `internal/maintenance` (`InjectStore` /
  `GetStore`). It was set once at startup and never read by anything.
