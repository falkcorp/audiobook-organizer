### Fixed

- `POST /api/v3/ops/runs/:id/cancel` now lets a holder of `settings.manage` cancel a legacy Repairs run that
  names no fixer. Such a run is readable by nobody, so the cancel was refused with the Repairs 403 even to an
  admin who did not start it, while the v1 cancel and discard routes (`DELETE /operations/v2/:id` and `/record`,
  both `settings.manage`) already worked on it. The run stays unreadable; a run that does name a fixer is
  unchanged.
