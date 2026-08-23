- [ ] **TODO-052-UNDOC** `docs/api/openapi.json` has no entry at all for two
      live, permission-gated routes discovered while TASK-052 triaged the 15
      stale `POST /maintenance/{job-name}` paths (PR for TODO L296):
      `GET /maintenance/jobs` (the maintenance job catalogue —
      `internal/server/maintenance_dispatcher.go`'s `listMaintenanceJobs`,
      wired in `internal/server/server_lifecycle.go`) and
      `POST /maintenance/wipe` (admin-only, `s.handleWipe`, same file). Neither
      was ever documented, so unlike the 15 deleted paths there is no stale
      entry blocking this — it is pure addition. `POST /maintenance/jobs/{job_id}`
      (added by TASK-052) references `GET /maintenance/jobs` in its
      description as the live source of truth for the job_id enum; that
      cross-reference is currently undocumented itself.
