- [ ] **05-PR9 follow-up** `POST /api/v3/ops/runs/{id}/approve` answers 501
      after all its checks: nothing records a decision against a run yet.
      Wire it once `ops.Approval` is attached to `ops.Common` / `ops.Compiled`
      and the runner writes a `database.ApprovalRecord` (then pass an
      `opsv3.Approver` in `internal/server/wire_ops_v3_routes.go`).
- [ ] **05-PR9 follow-up** The v3 params schema check
      (`internal/server/handlers/opsv3/schema.go`) is an in-tree subset of
      JSON Schema, because the repo imports no JSON Schema library and a new
      dependency is the owner's call. Owner decision: keep the subset, or
      adopt a maintained library (and drop the checker).
- [ ] **05-PR9 follow-up** Two writing ops have no single preview key, so
      `/api/v3/ops` gives them no preview and a `mode: "live"` start with `{}`
      silently previews (params pass through untouched):
      `itunes.clone-into-library` (`apply` gates only the clone mode;
      `rollback`/`repair` write without it) and
      `maintenance.repoint-version-primary` (writes need `apply=true` and
      `dry_run=false`). Owner decision: give each one gate (fold the second
      key into `dry_run`), or keep them live-only with the caller sending
      every key. Listed in `previewOnlyElsewhere`
      (`internal/server/wire_ops_v3_routes_test.go`).
- [ ] **05-PR9 follow-up** Five read-only maintenance jobs (relink-report,
      scan-duplicate-files, scan-duration-mismatch, scan-metadata-hash-dups,
      scan-chapter-groups) declare `DefaultPolicy()` capabilities, which
      include library.write, so `/api/v3/ops` treats them as writers without
      a preview and requires `mode: "live"`. Give them read-only capabilities.
