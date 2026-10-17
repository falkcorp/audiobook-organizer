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
