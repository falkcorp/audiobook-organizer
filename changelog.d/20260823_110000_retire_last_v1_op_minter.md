### Changed

- Maintenance jobs now record a single operation instead of two. Starting a job used to
  create a row in the old operations table *and* a row in the current one; the old row was
  never shown anywhere — the operations timeline and the activity bell both stopped reading
  it some time ago — but it was still swept on every restart, which is how finished jobs
  could reappear as "pending" and be resumed a second time. The job's own operation ID is now
  the only one, and it is the ID the API returns, so an operation you start is one you can
  actually look up.
- An interrupted maintenance job now resumes with the `dry_run` the operator actually chose,
  read back from the operation itself. It previously had to be reconstructed from a separate
  saved copy, falling back to the job's advertised default when that copy was missing.

### Fixed

- `GET /api/v1/maintenance/scan-composer-tags/:id` and
  `GET /api/v1/maintenance/repair-missing-files/:id` now resolve an operation ID from either
  era. Runs started before this release keep working, and runs started after it stop
  returning 404.

### Removed

- The `audiobook_organizer_maintenance_resume_params_fallback_total` counter, which measured
  a fallback that no longer exists. It was never part of a release; no alert or dashboard
  referenced it.
