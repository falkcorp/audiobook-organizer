### Decide the fate of the v1 stale-operation reaper (and `operation_timeout_minutes`)

`internal/server/server_lifecycle.go` runs a background goroutine on a **1-minute
ticker** (`:629-644`, gated on `config.AppConfig.OperationTimeoutMinutes > 0`, which
defaults to **30**, so it is running in prod right now) that calls
`collectStaleOperations` → `failStaleOperations`. That pair is **inert on both ends**:

- **Input.** `collectStaleOperations` (`:1727-1752`) reads
  `s.Ops().GetRecentOperations(500)` — the v1 `operation:` keyspace. The v1 id minter
  was retired 2026-08-23, so it returns nothing an operator has run since.
- **Output.** `failStaleOperations` (`:1754-1778`) calls
  `s.Ops().UpdateOperationError(op.ID, msg)`, which resolves via `GetOperationByID`
  and writes `operation:<id>` (`internal/database/pebble_store_operations.go:167`) —
  the same keyspace phase 1 of the v1 purge deletes.

So this is **dead code, not a disabled safety net**, and the safety it used to provide
is already owned by the v2 watchdog (`internal/operations/registry/watchdog.go`), which
has three strike types (`uncheckpointed`, `stuck`, `never_reported`) and **cancels**
stuck ops (`:139-141`).

**Do NOT simply repoint `collectStaleOperations` at v2.** That would stand up a second,
dumber reaper next to the watchdog: a flat `OperationTimeoutMinutes` cutoff with no
liveness signal and no awareness of `interrupted_quiesced` / `interrupted_ask` /
`interrupted_restart` — all three of which stamp `completed_at` while still awaiting
resume. It would force-fail operations the watchdog is legitimately holding. That is a
regression, not a migration.

Three decisions, and the third is the only one that is not obvious:

1. **Delete the reaper** — `failStaleOperations`, its ticker goroutine, and the
   `collectStaleOperations` helper behind it. Superseded by the v2 watchdog.
2. **Decide what `operation_timeout_minutes` means.** It is a documented, persisted
   config key (`internal/config/config.go:958`, `:2097`, `:2670`, default 30 at
   `:2798`; settable at runtime via `internal/config/persistence.go:1062`). With the
   reaper gone it controls nothing. Either wire it to the v2 watchdog's own timeout or
   deprecate it explicitly — leaving a live knob attached to nothing is the shape of
   bug this whole v1 sweep keeps finding.
3. **`GET` stale-operations listing.** `internal/server/handlers/operations/handler.go:444`
   also calls `h.collectStale(...)` to serve a read-only
   `{timeout_minutes, count, operations}` payload, which today always answers
   `count: 0`. Repoint it at v2 (read-only, so no reaper hazard) or delete the endpoint
   with the rest.

Filed rather than fixed because deleting a reaper that has been wired into server
startup since before the v2 migration is a production call, and because (2) is a
user-visible config deprecation.
