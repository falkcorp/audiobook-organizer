- [ ] **01-P81K-PID-REPAIR-APPLY** Capability gap from 01-P81k: there is no longer
      any way to APPLY the duplicate iTunes PID repair. `POST /api/v1/itunes/pid-repair`
      (clear the PID from every duplicate owner except one keeper, database
      fields only) now answers 410, and `itunes.ApplyPIDRepairPlan` was deleted
      with it because nothing else called it. The read-only census
      (`GET /api/v1/itunes/pid-integrity`) and the plan preview
      (`pid-census --repair`) are kept. The 2026-08-14 census had already
      dropped to 2 ambiguous groups with `files_to_clear=0`, so the apply was a
      no-op; the open "apply the backfill repair" entry in the TODO is moot
      unless the census shows duplicates again. If it does, rebuild the apply
      as an operation with a preview mode (`opmode.ParseDryRun`), the clear
      step from git history (`internal/itunes/pid_repair.go` before 01-P81k).
