- [ ] **Finish killing `database.Store` — 18 references left outside `internal/database`.**
      Down from 398-method-wide everywhere; see
      `docs/plans/2026-08-18-decouple-database-layer.md`. The remainder splits into:
      - **7 left by design** — `internal/server/server.go` (the `store` field, `Store()`,
        `NewServer`, and the nil-store error text) and `internal/server/indexed_store.go`
        (the embedded `database.Store`, the `StoreUnwrapper` assert, and `Unwrap()`).
        These are the composition root and the decorator contract; they go away in plan
        phases 3–4 by splitting `PebbleStore` so `database.Store` becomes unreachable,
        not by narrowing them in place.
      - **3 test helpers** — `internal/testutil/integration.go` (rationale verified
        genuine: integration tests poke at any domain a scenario needs) and
        `internal/database/dbtest/invariants.go` ×2.
      - **8 the `Server.Store()` chain** — `internal/plugins/maintenance/deps.go` ×3 and
        `internal/server/server_maintenance_deps.go` ×2 plus their callers. Blocked on
        `Server.Store()` itself. ⚠️ `deps.go` forwards into `missing_file_repair.go` /
        `missing_file_audit.go`, which run against prod and are a separate hands-off
        lane — do not touch those without asking.
