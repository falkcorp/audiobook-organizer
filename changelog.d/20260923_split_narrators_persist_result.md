### Fixed

- `maintenance.split-joined-narrators` now saves its report as the operation result, so `GET /api/v1/operations/:id/result` returns the counts and the per-book preview. The run wrapper had been discarding the report.
