- [ ] **Audit the 10 remaining bare `store.(*PebbleStore)` assertions in production code.**
      `internal/database/store_capability.go` documents that a bare concrete
      assertion fails through the Bleve `indexedStore` decorator, and records two
      prod jobs silently degraded for weeks that way. `AsPebbleStore` is the fix.
      Found 2026-08-19 while removing the interface-shaped twin of this bug (#2580,
      11 sites) — the concrete shape was never swept.

      Each site needs one question answered: **does this value come from
      `Server.Store()`?** Anything bound during `NewServer` holds the bare store and
      is unaffected; anything resolved at request time, op-run time, or in a lazily
      built service gets the wrapped store and is live.

      - `internal/server/wire_abs_routes.go:494` — **check first.** It asserts on
        `s.Store()` directly, and on failure silently skips `ps.WaitForWarmup()`
        before `WarmContributors`. The comment immediately above says building that
        cache against a half-published memdb caches a library that does not exist
        and serves it for the whole TTL. Launched from a goroutine at wire time, so
        whether the decorator is installed yet is a race, not a constant.
      - `internal/server/handlers/diagnostics.go:611` — request-time handler; on
        failure the db-health endpoint silently omits its Pebble section.
      - `internal/server/registry_wire.go:65,199,217` — likely wire-time/bare, verify.
      - `internal/scanner/process_file.go:211`
      - `internal/dedup/lifecycle.go:166`
      - `internal/plugins/acoustid/reset_all.go:69`
      - `internal/activity/register.go:37`
      - `internal/database/migrations.go:574`

      Test-file assertions are out of scope: those build a bare `*PebbleStore`
      locally and never see the decorator.
