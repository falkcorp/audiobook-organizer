- [ ] Decide whether maintenance jobs should take per-job store interfaces instead of the
      shared `maintenance.JobStore`. Measured 2026-08-18 after narrowing JobStore to 52
      methods: **23 of the 37 directly-called methods are used by exactly one job**, and
      only five are used by more than four (`GetAllBooksCore` 18 files, `GetBookByID` 12,
      `UpdateBook` 10, `GetAllBookFilesCore` 10, `GetBookFiles` 8). So most of the shared
      contract is not shared.
      The blocker is structural, not conceptual: `Run` is a method on `MaintenanceJob`,
      so every job must accept the same parameter type, and jobs register themselves at
      `init()` via `Register(job)` with no store in scope. Per-job stores means
      constructing jobs with their store instead — `All(store)` rather than `All()` — which
      touches the registry and both call sites (`maintenance_job_op.go:64`,
      `maintenance_dispatcher.go:26`). The second is deleted by phase 1 of
      `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`, so this is cheaper
      to do after the v1 retirement than before it.
