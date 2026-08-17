- [ ] **`OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete the code that *is* doing the enforcing**

  `internal/operations/registry/types.go:78` documents the field as "user perms required to
  trigger via API". Measured 2026-08-17: the **only** read of `def.Permissions` anywhere in the
  repo is `json.Marshal` at `internal/operations/registry/registry.go:509`, which writes it into
  an `op_definitions_v2` column. No handler, middleware, or registry path ever compares it against
  the caller. The v2 operations handler package contains zero references to it. It is a field that
  reads like a gate and behaves like a comment.

  The gate that actually exists is route-level and **uniform across every v2 op**:

  - `internal/server/wire_operations_routes.go:27` — `POST /operations/v2` requires
    `auth.PermScanTrigger`, whatever the op is.
  - `internal/server/maintenance_dispatcher.go:91-96` — the **v1** maintenance route requires
    `auth.PermSettingsManage`, or the job's own `PermissionAware.Permission()` when it implements
    one.

  Exactly one job implements `PermissionAware`: `bulkFetchMetadataJob`
  (`internal/maintenance/jobs/bulk_fetch_metadata.go:43` → `library.edit_metadata`).

  **The gap has a named role on each side.** From `internal/auth/seed.go:37-49`, the seeded
  `editor` role holds `scan.trigger` but **not** `settings.manage`. So an editor cannot run, say,
  `cleanup-backups` through the v1 maintenance route, but can run it through
  `POST /operations/v2` with op `maintenance.cleanup-backups`.

  **This is not a regression from PR #2533.** The `maintenance.job` bridge was registered on the
  same registry behind the same `scan.trigger` route and took the job as a `job_id` parameter, so
  the identical bypass existed with one generic door. What #2533 changed is that there are now 37
  named, enumerable, catalogue-listed doors instead of one door with a parameter — the gap is
  unchanged in kind but far more discoverable.

  **Why this is PR-3's problem specifically:** PR-3 retires the legacy v1 registry and dispatcher.
  The per-job enforcement at `maintenance_dispatcher.go:95-96` is *the only* per-job permission
  check in the system, and it lives on the code PR-3 deletes. Retiring v1 without first wiring
  `Permissions` into the v2 trigger path silently drops `bulk-fetch-metadata`'s
  `library.edit_metadata` requirement and leaves all 37 maintenance ops behind a blanket
  `scan.trigger`.

  Order that matters: enforce `def.Permissions` in `TriggerOperationV2` (falling back to the
  route-level permission when the slice is empty) **before** PR-3 deletes the v1 dispatcher — not
  after. Then the 37 `Permissions: settings.manage` declarations that
  `internal/server/maintenance_job_op.go` already writes become load-bearing instead of decorative,
  and `bulkFetchMetadataJob` needs its `PermissionAware` value threaded into its `OperationDef`
  rather than the hardcoded default.

  Instrument note: the first grep for readers of this field returned four hits that were all
  `role.Permissions` — a different type on the auth side. The finding is the count *after*
  separating the two types, not the raw grep.
