### Changed

- Maintenance jobs now register one operation definition each (`maintenance.<job-id>`)
  instead of sharing a single `maintenance.job` definition. Each job's resume policy,
  timeout, concurrency key, liveness mode and capabilities now come from the job's own
  `Policy()` declaration rather than one hardcoded set applied to all 37. Per-job
  variation was structurally impossible before this.

### Removed

- The `maintenance.job` operation ID. An in-flight operation still naming it at restart
  is dropped with a warning, which is the same path it took before — the shared
  definition's policy was already "drop on restart", so behaviour across the upgrade is
  unchanged.
