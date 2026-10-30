<!-- file: docs/decisions/2026-10-30-throttle-reset-op.md -->
<!-- version: 1.0.1 -->
<!-- guid: ab938e48-3c70-4c05-9fc8-5d67bba3db14 -->
<!-- last-edited: 2026-10-30 -->

# maintenance.throttle-reset

## Decision log

- Mode: preview by default, `dry_run=false` to release, via `opmode.ResolveDryRun`
  (owner rule 2026-09-25), the same shape as `maintenance.book-hash-index-repair`.
  Rejected: an `apply` bool (the older `clear-apply-rename-failures` shape),
  which the preview-default guard forbids for new ops.
- Unknown provider: the registry only knows providers with a hold, so "unknown"
  means "no active hold". That is an error (preview and apply) naming the held
  providers, rather than a silent zero, so a typo is not read as success.
  Rejected: validating against the configured provider list, which would
  need a new dependency on the metadata config for no extra safety.
- Registry API: `ReleaseHold(provider)` only (commented as existing for this op); the
  op loops it for the all case. A `ReleaseAll` was dropped as dead code by the
  deadcode ratchet. `ReleaseHold` takes `persistMu` so it cannot interleave
  with `RecordFailure`, the same discipline as `RecordSuccess`.
  Rejected: reviving `Clear`/`ClearAll` unchanged, which skipped that lock.
- Admin-only: no per-op permission field exists; op starts are admin-gated by
  the ops routes, as for every other maintenance op. No route added.
- Capabilities: `CapLibraryRead` + `CapLibraryWrite`, because the registry's holds persist in the store and the op deletes them (same set as `clear-apply-rename-failures`); `TestMaintenancePlugin_AllOpsHaveCapabilities` requires a non-empty set. No narrower capability exists.
- Out of scope, follow-up: the Throttle panel's "reset now" button.
